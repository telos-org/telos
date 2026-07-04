package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/telos-org/telos/internal/config"
	"github.com/telos-org/telos/internal/executor/providercore"
	"github.com/telos-org/telos/internal/game"
	"github.com/telos-org/telos/internal/gatewaycred"
	"github.com/telos-org/telos/internal/oauthcred"
)

type httpProviderAdapter struct {
	client          *http.Client
	cfg             nativeProviderConfig
	maxOutputTokens int
	logger          *nativeSessionLogger
}

func newProviderCoreAdapter(httpClient *http.Client, cfg nativeProviderConfig, maxOutputTokens int, logger *nativeSessionLogger) providercore.Adapter {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &httpProviderAdapter{client: httpClient, cfg: cfg, maxOutputTokens: maxOutputTokens, logger: logger}
}

func (a *httpProviderAdapter) Complete(ctx context.Context, request providercore.Request, sequence int) (providercore.Response, error) {
	switch a.cfg.Protocol {
	case gatewaycred.ProviderAnthropic:
		return a.completeAnthropic(ctx, request)
	case gatewaycred.ProviderGemini:
		return a.completeGemini(ctx, request)
	case gatewaycred.ProviderCodex:
		return a.completeCodex(ctx, request, sequence)
	default:
		return providercore.Response{}, newExecutorError(errConfig, "unsupported provider "+string(a.cfg.Protocol))
	}
}

func (a *httpProviderAdapter) completeAnthropic(ctx context.Context, request providercore.Request) (providercore.Response, error) {
	body, err := json.Marshal(anthropicRequest(request))
	if err != nil {
		return providercore.Response{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(a.cfg.BaseURL, "/")+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return providercore.Response{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", a.cfg.APIKey)
	req.Header.Set("anthropic-version", firstNonEmpty(a.cfg.Headers["anthropic-version"], "2023-06-01"))
	for _, key := range sortedMapKeys(a.cfg.Headers) {
		if strings.EqualFold(key, "anthropic-version") {
			continue
		}
		req.Header.Set(key, a.cfg.Headers[key])
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return providercore.Response{}, classifyProviderError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return providercore.Response{}, classifyHTTPBody(resp, a.cfg.APIKey)
	}
	var parsed anthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return providercore.Response{}, retryableExecutorError(errProviderUnavailable, "decode anthropic response: "+err.Error())
	}
	return normalizeAnthropicResponse(parsed), nil
}

func anthropicRequest(request providercore.Request) map[string]any {
	messages := make([]map[string]any, 0, len(request.Messages))
	for _, msg := range request.Messages {
		switch msg.Role {
		case providercore.RoleTool:
			messages = append(messages, map[string]any{
				"role": "user",
				"content": []map[string]any{{
					"type":        "tool_result",
					"tool_use_id": msg.ToolCallID,
					"content":     msg.Content,
				}},
			})
		case providercore.RoleAssistant:
			var blocks []map[string]any
			if strings.TrimSpace(msg.Content) != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": msg.Content})
			}
			for _, call := range msg.ToolCalls {
				blocks = append(blocks, map[string]any{
					"type":  "tool_use",
					"id":    call.ID,
					"name":  call.Name,
					"input": parseObject(call.Arguments),
				})
			}
			if len(blocks) > 0 {
				messages = append(messages, map[string]any{"role": "assistant", "content": blocks})
			}
		default:
			messages = append(messages, map[string]any{"role": "user", "content": msg.Content})
		}
	}
	tools := make([]map[string]any, 0, len(request.Tools))
	for _, tool := range request.Tools {
		tools = append(tools, map[string]any{
			"name":         tool.Name,
			"description":  tool.Description,
			"input_schema": tool.Parameters,
		})
	}
	body := map[string]any{
		"model":      request.Model,
		"system":     request.System,
		"messages":   messages,
		"max_tokens": request.MaxOutputTokens,
	}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	if request.MaxOutputTokens <= 0 {
		body["max_tokens"] = defaultMaxOutputTokens
	}
	return body
}

type anthropicResponse struct {
	ID         string `json:"id"`
	StopReason string `json:"stop_reason"`
	Content    []struct {
		Type  string          `json:"type"`
		Text  string          `json:"text"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	} `json:"content"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

func normalizeAnthropicResponse(parsed anthropicResponse) providercore.Response {
	collector := providercore.NewCollector()
	for _, block := range parsed.Content {
		switch block.Type {
		case "text":
			collector.Apply(providercore.Event{Type: providercore.EventTextDelta, Text: block.Text})
		case "tool_use":
			collector.Apply(providercore.Event{Type: providercore.EventToolCallStart, ToolCallID: block.ID, ToolName: block.Name})
			collector.Apply(providercore.Event{Type: providercore.EventToolCallDelta, ToolCallID: block.ID, ArgumentsFragment: string(block.Input)})
			collector.Apply(providercore.Event{Type: providercore.EventToolCallEnd, ToolCallID: block.ID})
		}
	}
	status := providercore.StatusCompleted
	if parsed.StopReason == "max_tokens" {
		status = providercore.StatusIncomplete
	}
	collector.Apply(providercore.Event{Type: providercore.EventUsage, Usage: providercore.Usage{InputTokens: parsed.Usage.InputTokens, OutputTokens: parsed.Usage.OutputTokens}})
	collector.Apply(providercore.Event{Type: providercore.EventDone, ResponseID: parsed.ID, Status: status, StopReason: parsed.StopReason})
	return collector.Response()
}

func (a *httpProviderAdapter) completeGemini(ctx context.Context, request providercore.Request) (providercore.Response, error) {
	body, err := json.Marshal(geminiRequest(request))
	if err != nil {
		return providercore.Response{}, err
	}
	model := strings.TrimPrefix(strings.TrimSpace(request.Model), "models/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(a.cfg.BaseURL, "/")+"/v1beta/models/"+url.PathEscape(model)+":generateContent", bytes.NewReader(body))
	if err != nil {
		return providercore.Response{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", a.cfg.APIKey)
	for _, key := range sortedMapKeys(a.cfg.Headers) {
		req.Header.Set(key, a.cfg.Headers[key])
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return providercore.Response{}, classifyProviderError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return providercore.Response{}, classifyHTTPBody(resp, a.cfg.APIKey)
	}
	var parsed geminiResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return providercore.Response{}, retryableExecutorError(errProviderUnavailable, "decode gemini response: "+err.Error())
	}
	return normalizeGeminiResponse(parsed), nil
}

func geminiRequest(request providercore.Request) map[string]any {
	contents := make([]map[string]any, 0, len(request.Messages))
	for _, msg := range request.Messages {
		switch msg.Role {
		case providercore.RoleTool:
			contents = append(contents, map[string]any{"role": "user", "parts": []map[string]any{{
				"functionResponse": map[string]any{
					"id":       msg.ToolCallID,
					"name":     firstNonEmpty(msg.ToolName, msg.ToolCallID),
					"response": map[string]any{"result": msg.Content},
				},
			}}})
		case providercore.RoleAssistant:
			var parts []map[string]any
			if strings.TrimSpace(msg.Content) != "" {
				parts = append(parts, map[string]any{"text": msg.Content})
			}
			for _, call := range msg.ToolCalls {
				parts = append(parts, map[string]any{"functionCall": map[string]any{"id": call.ID, "name": call.Name, "args": parseObject(call.Arguments)}})
			}
			if len(parts) > 0 {
				contents = append(contents, map[string]any{"role": "model", "parts": parts})
			}
		default:
			contents = append(contents, map[string]any{"role": "user", "parts": []map[string]any{{"text": msg.Content}}})
		}
	}
	declarations := make([]map[string]any, 0, len(request.Tools))
	for _, tool := range request.Tools {
		declarations = append(declarations, map[string]any{
			"name":        tool.Name,
			"description": tool.Description,
			"parameters":  sanitizeGeminiSchema(tool.Parameters),
		})
	}
	body := map[string]any{
		"contents": contents,
		"systemInstruction": map[string]any{
			"parts": []map[string]any{{"text": request.System}},
		},
		"generationConfig": map[string]any{"maxOutputTokens": request.MaxOutputTokens},
	}
	if request.MaxOutputTokens <= 0 {
		body["generationConfig"] = map[string]any{"maxOutputTokens": defaultMaxOutputTokens}
	}
	if len(declarations) > 0 {
		body["tools"] = []map[string]any{{"functionDeclarations": declarations}}
	}
	return body
}

type geminiResponse struct {
	Candidates []struct {
		FinishReason string `json:"finishReason"`
		Content      struct {
			Parts []struct {
				Text         string `json:"text"`
				FunctionCall *struct {
					ID   string          `json:"id"`
					Name string          `json:"name"`
					Args json.RawMessage `json:"args"`
				} `json:"functionCall"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount        int `json:"promptTokenCount"`
		CandidatesTokenCount    int `json:"candidatesTokenCount"`
		CachedContentTokenCount int `json:"cachedContentTokenCount"`
		ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
	} `json:"usageMetadata"`
}

func normalizeGeminiResponse(parsed geminiResponse) providercore.Response {
	collector := providercore.NewCollector()
	status := providercore.StatusCompleted
	stopReason := ""
	for _, candidate := range parsed.Candidates {
		stopReason = candidate.FinishReason
		if candidate.FinishReason == "MAX_TOKENS" {
			status = providercore.StatusIncomplete
		}
		for i, part := range candidate.Content.Parts {
			if part.Text != "" {
				collector.Apply(providercore.Event{Type: providercore.EventTextDelta, Text: part.Text})
			}
			if part.FunctionCall != nil {
				id := part.FunctionCall.ID
				if id == "" {
					id = fmt.Sprintf("gemini_tool_%d", i+1)
				}
				collector.Apply(providercore.Event{Type: providercore.EventToolCallStart, ToolCallID: id, ToolName: part.FunctionCall.Name})
				collector.Apply(providercore.Event{Type: providercore.EventToolCallDelta, ToolCallID: id, ArgumentsFragment: string(part.FunctionCall.Args)})
				collector.Apply(providercore.Event{Type: providercore.EventToolCallEnd, ToolCallID: id})
			}
		}
	}
	collector.Apply(providercore.Event{Type: providercore.EventUsage, Usage: providercore.Usage{
		InputTokens:       parsed.UsageMetadata.PromptTokenCount,
		OutputTokens:      parsed.UsageMetadata.CandidatesTokenCount + parsed.UsageMetadata.ThoughtsTokenCount,
		CachedInputTokens: parsed.UsageMetadata.CachedContentTokenCount,
		ReasoningTokens:   parsed.UsageMetadata.ThoughtsTokenCount,
	}})
	collector.Apply(providercore.Event{Type: providercore.EventDone, Status: status, StopReason: stopReason})
	return collector.Response()
}

func (a *httpProviderAdapter) completeCodex(ctx context.Context, request providercore.Request, sequence int) (providercore.Response, error) {
	_ = sequence
	body, err := json.Marshal(codexRequest(request))
	if err != nil {
		return providercore.Response{}, err
	}
	endpoint := strings.TrimRight(a.cfg.BaseURL, "/") + "/backend-api/codex/responses"
	req, err := a.codexHTTPRequest(ctx, endpoint, body, a.cfg.APIKey)
	if err != nil {
		return providercore.Response{}, err
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return providercore.Response{}, classifyProviderError(err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		_ = resp.Body.Close()
		token, refreshErr := a.refreshCodexToken(ctx)
		if refreshErr == nil {
			req, err = a.codexHTTPRequest(ctx, endpoint, body, token.AccessToken)
			if err != nil {
				return providercore.Response{}, err
			}
			resp, err = a.client.Do(req)
			if err != nil {
				return providercore.Response{}, classifyProviderError(err)
			}
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return providercore.Response{}, classifyHTTPBody(resp, a.cfg.APIKey)
	}
	events := make(chan providercore.Event, 16)
	go func() {
		defer close(events)
		scanResponsesSSE(resp.Body, events)
	}()
	result := providercore.Collect(ctx, events)
	if result.Error != nil {
		return providercore.Response{}, coreError(result.Error)
	}
	return result, nil
}

func (a *httpProviderAdapter) codexHTTPRequest(ctx context.Context, endpoint string, body []byte, accessToken string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("session_id", "telos-native")
	for _, key := range sortedMapKeys(a.cfg.Headers) {
		req.Header.Set(key, a.cfg.Headers[key])
	}
	return req, nil
}

func (a *httpProviderAdapter) refreshCodexToken(ctx context.Context) (oauthcred.Token, error) {
	path := oauthcred.StorePath(config.ConfigPath())
	token, err := oauthcred.Load(path)
	if err != nil {
		return oauthcred.Token{}, err
	}
	next, err := oauthcred.Refresh(ctx, a.client, token)
	if err != nil {
		return oauthcred.Token{}, err
	}
	if err := oauthcred.Save(path, next); err != nil {
		return oauthcred.Token{}, err
	}
	return next, nil
}

func codexRequest(request providercore.Request) map[string]any {
	input := make([]map[string]any, 0, len(request.Messages))
	for _, msg := range request.Messages {
		switch msg.Role {
		case providercore.RoleTool:
			input = append(input, map[string]any{"type": "function_call_output", "call_id": msg.ToolCallID, "output": msg.Content})
		case providercore.RoleAssistant:
			if strings.TrimSpace(msg.Content) != "" {
				input = append(input, map[string]any{"type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": msg.Content}}})
			}
			for _, call := range msg.ToolCalls {
				input = append(input, map[string]any{"type": "function_call", "id": call.ID, "call_id": call.ID, "name": call.Name, "arguments": call.Arguments})
			}
		default:
			input = append(input, map[string]any{"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": msg.Content}}})
		}
	}
	tools := make([]map[string]any, 0, len(request.Tools))
	for _, tool := range request.Tools {
		tools = append(tools, map[string]any{"type": "function", "name": tool.Name, "description": tool.Description, "parameters": tool.Parameters})
	}
	body := map[string]any{
		"model":             request.Model,
		"instructions":      request.System,
		"input":             input,
		"stream":            true,
		"store":             false,
		"max_output_tokens": request.MaxOutputTokens,
	}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	return body
}

func scanResponsesSSE(r io.Reader, events chan<- providercore.Event) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	var data strings.Builder
	flush := func() bool {
		raw := strings.TrimSpace(data.String())
		data.Reset()
		if raw == "" || raw == "[DONE]" {
			return true
		}
		var payload responsesStreamEvent
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			events <- providercore.Event{Type: providercore.EventError, Error: providercore.Classify(0, "malformed response stream: "+err.Error())}
			return false
		}
		return emitResponsesEvent(payload, events)
	}
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			if !flush() {
				return
			}
			continue
		}
		if value, ok := strings.CutPrefix(line, "data:"); ok {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimSpace(value))
		}
	}
	_ = flush()
}

type responsesStreamEvent struct {
	Type   string `json:"type"`
	Delta  string `json:"delta"`
	ItemID string `json:"item_id"`
	Item   *struct {
		Type      string `json:"type"`
		ID        string `json:"id"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"item"`
	Response *struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Usage  struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
			InputDetails struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"input_tokens_details"`
			OutputDetails struct {
				ReasoningTokens int `json:"reasoning_tokens"`
			} `json:"output_tokens_details"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	} `json:"response"`
	Message string `json:"message"`
	Code    string `json:"code"`
}

func emitResponsesEvent(payload responsesStreamEvent, events chan<- providercore.Event) bool {
	switch payload.Type {
	case "response.output_text.delta":
		events <- providercore.Event{Type: providercore.EventTextDelta, Text: payload.Delta}
	case "response.reasoning_summary_text.delta":
		events <- providercore.Event{Type: providercore.EventReasoning, Reasoning: payload.Delta}
	case "response.output_item.added":
		if payload.Item != nil && payload.Item.Type == "function_call" {
			id := firstNonEmpty(payload.Item.CallID, payload.Item.ID, payload.ItemID)
			events <- providercore.Event{Type: providercore.EventToolCallStart, ToolCallID: id, ToolName: payload.Item.Name}
			if payload.Item.Arguments != "" {
				events <- providercore.Event{Type: providercore.EventToolCallDelta, ToolCallID: id, ArgumentsFragment: payload.Item.Arguments}
			}
		}
	case "response.function_call_arguments.delta":
		events <- providercore.Event{Type: providercore.EventToolCallDelta, ToolCallID: payload.ItemID, ArgumentsFragment: payload.Delta}
	case "response.output_item.done":
		if payload.Item != nil && payload.Item.Type == "function_call" {
			id := firstNonEmpty(payload.Item.CallID, payload.Item.ID, payload.ItemID)
			events <- providercore.Event{Type: providercore.EventToolCallEnd, ToolCallID: id}
		}
	case "response.completed", "response.incomplete":
		if payload.Response != nil {
			events <- providercore.Event{Type: providercore.EventUsage, Usage: providercore.Usage{
				InputTokens:       payload.Response.Usage.InputTokens,
				OutputTokens:      payload.Response.Usage.OutputTokens,
				CachedInputTokens: payload.Response.Usage.InputDetails.CachedTokens,
				ReasoningTokens:   payload.Response.Usage.OutputDetails.ReasoningTokens,
			}}
			status := providercore.StatusCompleted
			if payload.Type == "response.incomplete" || payload.Response.Status == "incomplete" {
				status = providercore.StatusIncomplete
			}
			events <- providercore.Event{Type: providercore.EventDone, ResponseID: payload.Response.ID, Status: status, StopReason: payload.Response.Status}
		}
		return false
	case "response.failed", "response.error":
		message := firstNonEmpty(payload.Message, payload.Code, "codex response failed")
		if payload.Response != nil && payload.Response.Error != nil {
			message = firstNonEmpty(payload.Response.Error.Message, message)
		}
		events <- providercore.Event{Type: providercore.EventError, Error: providercore.Classify(500, message)}
		return false
	}
	return true
}

func classifyHTTPBody(resp *http.Response, secrets ...string) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	message := resp.Status
	var parsed struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &parsed) == nil {
		message = firstNonEmpty(parsed.Error.Message, parsed.Message, parsed.Error.Type, message)
	} else if trimmed := strings.TrimSpace(string(body)); trimmed != "" {
		message = trimmed
	}
	for _, secret := range secrets {
		secret = strings.TrimSpace(secret)
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[REDACTED]")
		}
	}
	return coreError(providercore.Classify(resp.StatusCode, message))
}

func coreTerminalError(result providercore.Response) error {
	switch result.Status {
	case "", providercore.StatusCompleted:
		return nil
	case providercore.StatusIncomplete:
		return nil
	case providercore.StatusCancelled:
		return newExecutorError(errStopped, "response_cancelled")
	case providercore.StatusFailed:
		return newExecutorError(errProviderUnavailable, firstNonEmpty(result.StopReason, "response_failed"))
	default:
		return retryableExecutorError(errProviderUnavailable, "response_not_terminal:"+string(result.Status))
	}
}

func coreError(err *providercore.Error) error {
	if err == nil {
		return nil
	}
	code := errProviderUnavailable
	switch err.Class {
	case providercore.ErrorContextLimit:
		code = errProviderContextLimit
	case providercore.ErrorInvalidRequest:
		code = errProviderInvalidRequest
	case providercore.ErrorRateLimited:
		code = errProviderRateLimited
	case providercore.ErrorTimeout:
		code = errProviderTimeout
	case providercore.ErrorCancelled:
		code = errStopped
	}
	out := &executorError{Code: code, Message: err.Message, Retryable: err.Retryable, StatusCode: err.StatusCode}
	return out
}

func coreToolCalls(calls []providercore.ToolCall) []nativeToolCall {
	out := make([]nativeToolCall, 0, len(calls))
	for _, call := range calls {
		out = append(out, nativeToolCall{ID: call.ID, Name: call.Name, Arguments: call.Arguments})
	}
	return out
}

func statsFromCoreUsage(model string, usage providercore.Usage) game.TurnStats {
	return game.TurnStats{
		Model:           model,
		InputTokens:     usage.InputTokens,
		OutputTokens:    usage.OutputTokens,
		CacheReadTokens: usage.CachedInputTokens,
		CostUSD:         usage.CostUSD,
		CostUnavailable: !usage.CostKnown,
	}
}

func parseObject(raw string) map[string]any {
	if strings.TrimSpace(raw) == "" {
		return map[string]any{}
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil || parsed == nil {
		return map[string]any{}
	}
	return parsed
}

var geminiSchemaFields = map[string]bool{
	"type": true, "format": true, "title": true, "description": true,
	"nullable": true, "enum": true, "items": true, "properties": true,
	"required": true, "anyOf": true, "propertyOrdering": true, "default": true,
	"minimum": true, "maximum": true, "minItems": true, "maxItems": true,
	"minLength": true, "maxLength": true, "minProperties": true,
	"maxProperties": true, "pattern": true, "example": true,
}

func sanitizeGeminiSchema(schema map[string]any) map[string]any {
	if schema == nil {
		return nil
	}
	out := make(map[string]any, len(schema))
	for key, value := range schema {
		if !geminiSchemaFields[key] {
			continue
		}
		switch key {
		case "properties":
			if props, ok := value.(map[string]any); ok {
				cleaned := make(map[string]any, len(props))
				for name, sub := range props {
					if subMap, ok := sub.(map[string]any); ok {
						cleaned[name] = sanitizeGeminiSchema(subMap)
					} else {
						cleaned[name] = sub
					}
				}
				value = cleaned
			}
		case "items":
			if subMap, ok := value.(map[string]any); ok {
				value = sanitizeGeminiSchema(subMap)
			}
		case "anyOf":
			if variants, ok := value.([]any); ok {
				cleaned := make([]any, len(variants))
				for i, variant := range variants {
					if subMap, ok := variant.(map[string]any); ok {
						cleaned[i] = sanitizeGeminiSchema(subMap)
					} else {
						cleaned[i] = variant
					}
				}
				value = cleaned
			}
		}
		out[key] = value
	}
	return out
}

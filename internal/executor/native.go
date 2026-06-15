package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/telos-org/telos/internal/game"
	"github.com/telos-org/telos/internal/platform"
)

const (
	defaultMaxToolLoops    = 30
	defaultMaxOutputTokens = 8192
	defaultToolTimeoutSec  = 120
)

// NativeExecutor runs one PVG turn with Telos' built-in coding harness.
type NativeExecutor struct {
	Platform *platform.LocalPlatform
	Model    string
	Thinking string
	Timeout  int
	Client   *http.Client
}

// NewNativeExecutor creates a native Go coding-agent executor.
func NewNativeExecutor(p *platform.LocalPlatform, model, thinking string, timeout int) *NativeExecutor {
	if thinking == "" {
		thinking = "medium"
	}
	return &NativeExecutor{
		Platform: p,
		Model:    model,
		Thinking: thinking,
		Timeout:  timeout,
		Client:   http.DefaultClient,
	}
}

// ExecuteTurn runs one Telos-native agent turn.
func (ne *NativeExecutor) ExecuteTurn(task string, role string, turnState *game.TurnState) game.TurnResult {
	started := time.Now()
	stats := game.TurnStats{Model: ne.Model}
	sessionPath := ""
	var stopRequested func() bool
	if turnState != nil {
		sessionPath = turnState.PiSessionPath()
		stopRequested = turnState.StopRequested
	}

	ctx := context.Background()
	var cancel context.CancelFunc
	if ne.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(ne.Timeout)*time.Second)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	defer cancel()
	if stopRequested != nil {
		go func() {
			ticker := time.NewTicker(200 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if stopRequested() {
						cancel()
						return
					}
				}
			}
		}()
	}

	cfg, err := resolveNativeProvider(ne.Model)
	if err != nil {
		return recoverableTurn(role, stats, "agent_config_error:"+err.Error())
	}
	logger := newNativeSessionLogger(sessionPath, ne.Platform.Workspace)
	if err := logger.start(); err != nil {
		return recoverableTurn(role, stats, "native_session_unavailable:"+err.Error())
	}
	_ = logger.user(task)

	tools := newNativeTools(ne.Platform, stopRequested)
	client := nativeAPIClient{
		http:     ne.Client,
		cfg:      cfg,
		thinking: ne.Thinking,
		tools:    tools,
		logger:   logger,
	}
	logs, extraStats, err := client.run(ctx, task, role)
	stats = mergeTurnStats(stats, extraStats)
	stats.DurationMS = int(time.Since(started).Milliseconds())
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return recoverableTurn(role, stats, fmt.Sprintf("native_timeout:%d", ne.Timeout))
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return recoverableTurn(role, stats, "native_interrupted:stop_requested")
		}
		return recoverableTurn(role, stats, err.Error())
	}
	if strings.TrimSpace(logs) == "" {
		return recoverableTurn(role, stats, "agent_no_output")
	}
	return game.TurnResult{
		Role:   role,
		Status: game.ExtractStatus(logs),
		Logs:   logs,
		Stats:  stats,
	}
}

// WorkspaceState returns the workspace state from the platform.
func (ne *NativeExecutor) WorkspaceState() string {
	return ne.Platform.WorkspaceState()
}

// CheckpointWorkspace creates a workspace checkpoint.
func (ne *NativeExecutor) CheckpointWorkspace(dest string) bool {
	return ne.Platform.CheckpointWorkspace(dest)
}

func recoverableTurn(role string, stats game.TurnStats, reason string) game.TurnResult {
	return game.TurnResult{
		Role:        role,
		Status:      game.StatusContinue,
		Logs:        reason,
		Stats:       stats,
		Error:       reason,
		Recoverable: true,
	}
}

type providerStyle string

const (
	providerChat      providerStyle = "openai-chat"
	providerResponses providerStyle = "openai-responses"
	providerAnthropic providerStyle = "anthropic"
)

type nativeProviderConfig struct {
	Provider string
	Model    string
	BaseURL  string
	APIKey   string
	Style    providerStyle
}

func resolveNativeProvider(model string) (nativeProviderConfig, error) {
	if model == "" {
		return nativeProviderConfig{}, fmt.Errorf("model is required")
	}
	if base := firstEnv("TELOS_API_BASE_URL", "TELOS_BASE_URL"); base != "" {
		key := firstEnv("TELOS_API_KEY")
		if key == "" {
			return nativeProviderConfig{}, fmt.Errorf("TELOS_API_KEY is required when TELOS_API_BASE_URL is set")
		}
		return nativeProviderConfig{
			Provider: "telos",
			Model:    stripProviderPrefix(model),
			BaseURL:  strings.TrimRight(base, "/"),
			APIKey:   key,
			Style:    providerStyle(orDefault(firstEnv("TELOS_API_STYLE"), string(providerChat))),
		}, nil
	}

	provider, providerModel := splitProviderModel(model)
	switch provider {
	case "silares":
		return providerFromEnv(provider, providerModel, "https://api.silares.com/v1", "SILARES_API_KEY", providerResponses)
	case "sail-research":
		return providerFromEnv(provider, providerModel, "https://api.sailresearch.com/v1", "SAIL_API_KEY", providerChat)
	case "moonshot":
		return providerFromEnv(provider, providerModel, "https://api.moonshot.ai/v1", "MOONSHOT_API_KEY", providerChat)
	case "xai":
		return providerFromEnv(provider, providerModel, "https://api.x.ai/v1", "XAI_API_KEY", providerChat)
	case "openai", "openai-codex":
		return providerFromEnv(provider, providerModel, "https://api.openai.com/v1", "OPENAI_API_KEY", providerChat)
	case "anthropic":
		return providerFromEnv(provider, providerModel, "https://api.anthropic.com/v1", "ANTHROPIC_API_KEY", providerAnthropic)
	}
	if strings.HasPrefix(model, "claude-") {
		return providerFromEnv("anthropic", model, "https://api.anthropic.com/v1", "ANTHROPIC_API_KEY", providerAnthropic)
	}
	if provider != "" {
		envPrefix := providerEnvPrefix(provider)
		base := firstEnv(envPrefix + "_BASE_URL")
		keyName := envPrefix + "_API_KEY"
		key := firstEnv(keyName)
		if base == "" || key == "" {
			return nativeProviderConfig{}, fmt.Errorf("unknown provider %q; set %s_BASE_URL and %s", provider, envPrefix, keyName)
		}
		return nativeProviderConfig{
			Provider: provider,
			Model:    providerModel,
			BaseURL:  strings.TrimRight(base, "/"),
			APIKey:   key,
			Style:    providerStyle(orDefault(firstEnv(envPrefix+"_API_STYLE"), string(providerChat))),
		}, nil
	}
	return providerFromEnv("openai", model, "https://api.openai.com/v1", "OPENAI_API_KEY", providerChat)
}

func providerFromEnv(provider, model, defaultBase, keyName string, style providerStyle) (nativeProviderConfig, error) {
	envPrefix := providerEnvPrefix(provider)
	base := firstEnv(envPrefix + "_BASE_URL")
	if base == "" {
		switch provider {
		case "openai", "openai-codex":
			base = firstEnv("OPENAI_BASE_URL")
		case "xai":
			base = firstEnv("XAI_BASE_URL")
		}
	}
	if base == "" {
		base = defaultBase
	}
	key := firstEnv(keyName)
	if key == "" {
		return nativeProviderConfig{}, fmt.Errorf("%s is required for model provider %q", keyName, provider)
	}
	if override := firstEnv(envPrefix + "_API_STYLE"); override != "" {
		style = providerStyle(override)
	}
	return nativeProviderConfig{
		Provider: provider,
		Model:    model,
		BaseURL:  strings.TrimRight(base, "/"),
		APIKey:   key,
		Style:    style,
	}, nil
}

func splitProviderModel(model string) (string, string) {
	parts := strings.SplitN(model, "/", 2)
	if len(parts) != 2 {
		return "", model
	}
	return parts[0], parts[1]
}

func stripProviderPrefix(model string) string {
	_, m := splitProviderModel(model)
	return m
}

func providerEnvPrefix(provider string) string {
	s := strings.ToUpper(provider)
	s = strings.NewReplacer("-", "_", ".", "_").Replace(s)
	return s
}

func firstEnv(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

type nativeAPIClient struct {
	http     *http.Client
	cfg      nativeProviderConfig
	thinking string
	tools    *nativeTools
	logger   *nativeSessionLogger
}

func (c nativeAPIClient) run(ctx context.Context, task, role string) (string, game.TurnStats, error) {
	switch c.cfg.Style {
	case providerResponses:
		return c.runResponses(ctx, task, role)
	case providerAnthropic:
		return c.runAnthropic(ctx, task, role)
	default:
		return c.runChat(ctx, task, role)
	}
}

func nativeSystemPrompt(role string) string {
	return strings.Join([]string{
		"You are Telos' built-in coding harness running inside the benchmark workspace.",
		"The user message is the complete assignment for this turn. Do not ask the operator what to build or what to do next.",
		"If the spec names a deliverable, implement that deliverable exactly. An empty or minimal workspace means you should create the required files, not switch to a generic sample task.",
		"Keep your work anchored to the current spec text and live files. Ignore unrelated task ideas, default assistant personas, and prior benchmark examples that are not present in the current spec.",
		"Use the available tools directly. Do not ask for permission before inspecting or changing files required by the task.",
		"Prefer the first useful concrete workspace mutation over long planning. For file-producing tasks, create or edit the required files before summarizing.",
		"Available tool names are read, write, edit, bash, ls, grep, and find. Do not call unavailable tool names such as write_file, ReadFile, Edit, apply_patch, or shell.",
		"Keep all actionable content in visible assistant text. Do not put the final answer only in hidden reasoning.",
		"After tool work, end with a concise visible final response listing changed files and checks run. Include any XML tags required by the Telos turn instructions.",
		fmt.Sprintf("Current Telos role: %s.", role),
	}, "\n")
}

func nativeCorrectionPrompt(task string) string {
	return strings.Join([]string{
		"The assignment is already fully specified above. Do not ask what to build or what to do next.",
		"Implement the deliverable named in the assignment now. If the workspace is empty, create the required files directly.",
		"",
		"# Assignment",
		"",
		task,
	}, "\n")
}

func shouldRetryUnproductiveFinal(text string) bool {
	normalized := strings.ToLower(strings.TrimSpace(text))
	if normalized == "" {
		return true
	}
	for _, marker := range []string{
		"what would you like me to work on",
		"what would you like me to do",
		"what would you like me to build",
		"how can i help you",
		"just describe what you need",
		"ready to help",
		"workspace is currently empty",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

// -- OpenAI Chat Completions -------------------------------------------------

type chatMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type chatToolCall struct {
	ID       string           `json:"id,omitempty"`
	Type     string           `json:"type,omitempty"`
	Function chatToolFunction `json:"function"`
}

type chatToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func (c nativeAPIClient) runChat(ctx context.Context, task, role string) (string, game.TurnStats, error) {
	messages := []chatMessage{
		{Role: "system", Content: nativeSystemPrompt(role)},
		{Role: "user", Content: task},
	}
	var stats game.TurnStats
	stats.Model = c.cfg.Model
	for i := 0; i < defaultMaxToolLoops; i++ {
		var response struct {
			Choices []struct {
				Message      chatMessage `json:"message"`
				FinishReason string      `json:"finish_reason"`
			} `json:"choices"`
			Usage chatUsage `json:"usage"`
			Error apiError  `json:"error"`
		}
		req := map[string]interface{}{
			"model":       c.cfg.Model,
			"messages":    messages,
			"tools":       nativeToolSchemasForChat(),
			"tool_choice": "auto",
			"max_tokens":  defaultMaxOutputTokens,
		}
		if err := c.postJSON(ctx, "/chat/completions", req, &response); err != nil {
			return "", stats, err
		}
		if response.Error.Message != "" {
			return "", stats, fmt.Errorf("provider_error:%s", response.Error.Message)
		}
		stats = mergeTurnStats(stats, statsFromChatUsage(c.cfg.Model, response.Usage))
		if len(response.Choices) == 0 {
			return "", stats, fmt.Errorf("provider_error:no_choices")
		}
		msg := response.Choices[0].Message
		if msg.Role == "" {
			msg.Role = "assistant"
		}
		_ = c.logger.assistant(msg.Content, c.cfg.Provider, c.cfg.Model, response.Choices[0].FinishReason, statsFromChatUsage(c.cfg.Model, response.Usage))
		if len(msg.ToolCalls) == 0 {
			if shouldRetryUnproductiveFinal(msg.Content) && i+1 < defaultMaxToolLoops {
				messages = append(messages, msg, chatMessage{Role: "user", Content: nativeCorrectionPrompt(task)})
				continue
			}
			return msg.Content, stats, nil
		}
		messages = append(messages, msg)
		results := c.executeToolCalls(msg.ToolCalls)
		stats.NumTurns += len(results)
		for _, result := range results {
			_ = c.logger.tool(result)
			messages = append(messages, chatMessage{
				Role:       "tool",
				ToolCallID: result.CallID,
				Content:    result.Output,
			})
		}
	}
	return "", stats, fmt.Errorf("agent_tool_loop_exceeded:%d", defaultMaxToolLoops)
}

func (c nativeAPIClient) executeToolCalls(calls []chatToolCall) []nativeToolResult {
	results := make([]nativeToolResult, 0, len(calls))
	for _, call := range calls {
		results = append(results, c.tools.execute(nativeToolCall{
			ID:        call.ID,
			Name:      call.Function.Name,
			Arguments: call.Function.Arguments,
		}))
	}
	return results
}

type chatUsage struct {
	PromptTokens       int `json:"prompt_tokens"`
	CompletionTokens   int `json:"completion_tokens"`
	PromptTokenDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

func statsFromChatUsage(model string, usage chatUsage) game.TurnStats {
	return game.TurnStats{
		Model:           model,
		InputTokens:     usage.PromptTokens,
		OutputTokens:    usage.CompletionTokens,
		CacheReadTokens: usage.PromptTokenDetails.CachedTokens,
	}
}

// -- OpenAI Responses --------------------------------------------------------

type responseItem struct {
	Type      string                   `json:"type"`
	ID        string                   `json:"id,omitempty"`
	CallID    string                   `json:"call_id,omitempty"`
	Name      string                   `json:"name,omitempty"`
	Arguments string                   `json:"arguments,omitempty"`
	Content   []map[string]interface{} `json:"content,omitempty"`
}

func (c nativeAPIClient) runResponses(ctx context.Context, task, role string) (string, game.TurnStats, error) {
	input := []interface{}{
		map[string]interface{}{"role": "user", "content": nativeSystemPrompt(role) + "\n\n# Assignment\n\n" + task},
	}
	var previousID string
	var stats game.TurnStats
	stats.Model = c.cfg.Model
	for i := 0; i < defaultMaxToolLoops; i++ {
		var response struct {
			ID     string         `json:"id"`
			Output []responseItem `json:"output"`
			Text   struct {
				Format map[string]interface{} `json:"format"`
			} `json:"text"`
			Usage responsesUsage `json:"usage"`
			Error apiError       `json:"error"`
		}
		req := map[string]interface{}{
			"model":             c.cfg.Model,
			"input":             input,
			"tools":             nativeToolSchemasForResponses(),
			"max_output_tokens": defaultMaxOutputTokens,
		}
		if previousID != "" {
			req["previous_response_id"] = previousID
		}
		if c.thinking != "" {
			req["reasoning"] = map[string]interface{}{"effort": c.thinking}
		}
		if err := c.postJSON(ctx, "/responses", req, &response); err != nil {
			return "", stats, err
		}
		if response.Error.Message != "" {
			return "", stats, fmt.Errorf("provider_error:%s", response.Error.Message)
		}
		previousID = response.ID
		turnStats := statsFromResponsesUsage(c.cfg.Model, response.Usage)
		stats = mergeTurnStats(stats, turnStats)
		text, calls := parseResponseOutput(response.Output)
		_ = c.logger.assistant(text, c.cfg.Provider, c.cfg.Model, "stop", turnStats)
		if len(calls) == 0 {
			if shouldRetryUnproductiveFinal(text) && i+1 < defaultMaxToolLoops {
				input = []interface{}{
					map[string]interface{}{"role": "user", "content": nativeCorrectionPrompt(task)},
				}
				continue
			}
			return text, stats, nil
		}
		input = nil
		results := make([]nativeToolResult, 0, len(calls))
		for _, call := range calls {
			results = append(results, c.tools.execute(call))
		}
		stats.NumTurns += len(results)
		for _, result := range results {
			_ = c.logger.tool(result)
			input = append(input, map[string]interface{}{
				"type":    "function_call_output",
				"call_id": result.CallID,
				"output":  result.Output,
			})
		}
	}
	return "", stats, fmt.Errorf("agent_tool_loop_exceeded:%d", defaultMaxToolLoops)
}

func parseResponseOutput(output []responseItem) (string, []nativeToolCall) {
	var textParts []string
	var calls []nativeToolCall
	for _, item := range output {
		switch item.Type {
		case "message":
			for _, content := range item.Content {
				if text, _ := content["text"].(string); strings.TrimSpace(text) != "" {
					textParts = append(textParts, text)
				}
			}
		case "function_call":
			calls = append(calls, nativeToolCall{
				ID:        item.CallID,
				Name:      item.Name,
				Arguments: item.Arguments,
			})
		}
	}
	return strings.Join(textParts, ""), calls
}

type responsesUsage struct {
	InputTokens       int `json:"input_tokens"`
	OutputTokens      int `json:"output_tokens"`
	InputTokenDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
}

func statsFromResponsesUsage(model string, usage responsesUsage) game.TurnStats {
	return game.TurnStats{
		Model:           model,
		InputTokens:     usage.InputTokens,
		OutputTokens:    usage.OutputTokens,
		CacheReadTokens: usage.InputTokenDetails.CachedTokens,
	}
}

// -- Anthropic Messages ------------------------------------------------------

type anthropicBlock struct {
	Type      string      `json:"type"`
	Text      string      `json:"text,omitempty"`
	ID        string      `json:"id,omitempty"`
	Name      string      `json:"name,omitempty"`
	Input     interface{} `json:"input,omitempty"`
	ToolUseID string      `json:"tool_use_id,omitempty"`
	Content   string      `json:"content,omitempty"`
	IsError   bool        `json:"is_error,omitempty"`
}

type anthropicMessage struct {
	Role    string           `json:"role"`
	Content []anthropicBlock `json:"content"`
}

func (c nativeAPIClient) runAnthropic(ctx context.Context, task, role string) (string, game.TurnStats, error) {
	messages := []anthropicMessage{{Role: "user", Content: []anthropicBlock{{Type: "text", Text: task}}}}
	var stats game.TurnStats
	stats.Model = c.cfg.Model
	for i := 0; i < defaultMaxToolLoops; i++ {
		var response struct {
			ID         string           `json:"id"`
			Type       string           `json:"type"`
			Role       string           `json:"role"`
			Content    []anthropicBlock `json:"content"`
			StopReason string           `json:"stop_reason"`
			Usage      struct {
				InputTokens              int `json:"input_tokens"`
				OutputTokens             int `json:"output_tokens"`
				CacheReadInputTokens     int `json:"cache_read_input_tokens"`
				CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			} `json:"usage"`
			Error apiError `json:"error"`
		}
		req := map[string]interface{}{
			"model":      c.cfg.Model,
			"system":     nativeSystemPrompt(role),
			"messages":   messages,
			"tools":      nativeToolSchemasForAnthropic(),
			"max_tokens": defaultMaxOutputTokens,
		}
		if err := c.postAnthropic(ctx, "/messages", req, &response); err != nil {
			return "", stats, err
		}
		if response.Error.Message != "" {
			return "", stats, fmt.Errorf("provider_error:%s", response.Error.Message)
		}
		turnStats := game.TurnStats{
			Model:               c.cfg.Model,
			InputTokens:         response.Usage.InputTokens,
			OutputTokens:        response.Usage.OutputTokens,
			CacheReadTokens:     response.Usage.CacheReadInputTokens,
			CacheCreationTokens: response.Usage.CacheCreationInputTokens,
		}
		stats = mergeTurnStats(stats, turnStats)
		text, calls := parseAnthropicOutput(response.Content)
		_ = c.logger.assistant(text, c.cfg.Provider, c.cfg.Model, response.StopReason, turnStats)
		if len(calls) == 0 {
			if shouldRetryUnproductiveFinal(text) && i+1 < defaultMaxToolLoops {
				messages = append(messages,
					anthropicMessage{Role: "assistant", Content: response.Content},
					anthropicMessage{Role: "user", Content: []anthropicBlock{{Type: "text", Text: nativeCorrectionPrompt(task)}}},
				)
				continue
			}
			return text, stats, nil
		}
		messages = append(messages, anthropicMessage{Role: "assistant", Content: response.Content})
		results := make([]nativeToolResult, 0, len(calls))
		for _, call := range calls {
			results = append(results, c.tools.execute(call))
		}
		stats.NumTurns += len(results)
		resultBlocks := make([]anthropicBlock, 0, len(results))
		for _, result := range results {
			_ = c.logger.tool(result)
			resultBlocks = append(resultBlocks, anthropicBlock{
				Type:      "tool_result",
				ToolUseID: result.CallID,
				Content:   result.Output,
				IsError:   result.IsError,
			})
		}
		messages = append(messages, anthropicMessage{Role: "user", Content: resultBlocks})
	}
	return "", stats, fmt.Errorf("agent_tool_loop_exceeded:%d", defaultMaxToolLoops)
}

func parseAnthropicOutput(blocks []anthropicBlock) (string, []nativeToolCall) {
	var textParts []string
	var calls []nativeToolCall
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if strings.TrimSpace(block.Text) != "" {
				textParts = append(textParts, block.Text)
			}
		case "tool_use":
			args, _ := json.Marshal(block.Input)
			calls = append(calls, nativeToolCall{ID: block.ID, Name: block.Name, Arguments: string(args)})
		}
	}
	return strings.Join(textParts, ""), calls
}

// -- HTTP helpers ------------------------------------------------------------

type apiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

func (c nativeAPIClient) postJSON(ctx context.Context, endpoint string, body interface{}, out interface{}) error {
	return c.post(ctx, endpoint, body, out, map[string]string{"Authorization": "Bearer " + c.cfg.APIKey})
}

func (c nativeAPIClient) postAnthropic(ctx context.Context, endpoint string, body interface{}, out interface{}) error {
	return c.post(ctx, endpoint, body, out, map[string]string{
		"x-api-key":         c.cfg.APIKey,
		"anthropic-version": "2023-06-01",
	})
}

func (c nativeAPIClient) post(ctx context.Context, endpoint string, body interface{}, out interface{}, headers map[string]string) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("provider_request_encode:%w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("provider_request_create:%w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	httpClient := c.http
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("provider_request_failed:%w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("provider_response_read:%w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("provider_http_%d:%s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("provider_response_decode:%w", err)
	}
	return nil
}

// -- Tool schemas ------------------------------------------------------------

func nativeToolSchemasForChat() []map[string]interface{} {
	defs := nativeToolDefinitions()
	out := make([]map[string]interface{}, 0, len(defs))
	for _, def := range defs {
		out = append(out, map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name":        def.Name,
				"description": def.Description,
				"parameters":  def.Parameters,
			},
		})
	}
	return out
}

func nativeToolSchemasForResponses() []map[string]interface{} {
	defs := nativeToolDefinitions()
	out := make([]map[string]interface{}, 0, len(defs))
	for _, def := range defs {
		out = append(out, map[string]interface{}{
			"type":        "function",
			"name":        def.Name,
			"description": def.Description,
			"parameters":  def.Parameters,
		})
	}
	return out
}

func nativeToolSchemasForAnthropic() []map[string]interface{} {
	defs := nativeToolDefinitions()
	out := make([]map[string]interface{}, 0, len(defs))
	for _, def := range defs {
		out = append(out, map[string]interface{}{
			"name":         def.Name,
			"description":  def.Description,
			"input_schema": def.Parameters,
		})
	}
	return out
}

type nativeToolDefinition struct {
	Name        string
	Description string
	Parameters  map[string]interface{}
}

func nativeToolDefinitions() []nativeToolDefinition {
	stringProp := func(desc string) map[string]interface{} {
		return map[string]interface{}{"type": "string", "description": desc}
	}
	boolProp := func(desc string) map[string]interface{} {
		return map[string]interface{}{"type": "boolean", "description": desc}
	}
	intProp := func(desc string) map[string]interface{} {
		return map[string]interface{}{"type": "integer", "description": desc}
	}
	obj := func(required []string, props map[string]interface{}) map[string]interface{} {
		return map[string]interface{}{
			"type":                 "object",
			"required":             required,
			"properties":           props,
			"additionalProperties": false,
		}
	}
	return []nativeToolDefinition{
		{"read", "Read a UTF-8 file. Relative paths resolve inside the workspace; absolute paths are used as-is.", obj([]string{"path"}, map[string]interface{}{"path": stringProp("Relative workspace path or absolute container path.")})},
		{"write", "Create or overwrite a UTF-8 file. Relative paths resolve inside the workspace; absolute paths are used as-is.", obj([]string{"path", "content"}, map[string]interface{}{"path": stringProp("Relative workspace path or absolute container path."), "content": stringProp("Complete file content to write.")})},
		{"edit", "Replace text in an existing UTF-8 file. Relative paths resolve inside the workspace; absolute paths are used as-is.", obj([]string{"path", "old_string", "new_string"}, map[string]interface{}{"path": stringProp("Relative workspace path or absolute container path."), "old_string": stringProp("Exact text to replace."), "new_string": stringProp("Replacement text."), "replace_all": boolProp("Replace every occurrence instead of only the first.")})},
		{"bash", "Run a shell command in the workspace.", obj([]string{"command"}, map[string]interface{}{"command": stringProp("Command to run with bash -lc."), "timeout_seconds": intProp("Optional timeout, capped by Telos.")})},
		{"ls", "List files in a directory. Relative paths resolve inside the workspace; absolute paths are used as-is.", obj([]string{}, map[string]interface{}{"path": stringProp("Directory path, defaults to workspace root.")})},
		{"grep", "Search text files with a regular expression. Relative paths resolve inside the workspace; absolute paths are used as-is.", obj([]string{"pattern"}, map[string]interface{}{"pattern": stringProp("Go regular expression."), "path": stringProp("Directory or file path, defaults to workspace root."), "max_matches": intProp("Maximum matches to return.")})},
		{"find", "Find files by glob pattern. Relative paths resolve inside the workspace; absolute paths are used as-is.", obj([]string{"pattern"}, map[string]interface{}{"pattern": stringProp("Glob pattern matched against relative paths and basenames."), "path": stringProp("Directory path, defaults to workspace root."), "max_matches": intProp("Maximum paths to return.")})},
	}
}

// -- Tools -------------------------------------------------------------------

type nativeToolCall struct {
	ID        string
	Name      string
	Arguments string
}

type nativeToolResult struct {
	CallID  string
	Name    string
	Output  string
	IsError bool
}

type nativeTools struct {
	platform      *platform.LocalPlatform
	stopRequested func() bool
}

func newNativeTools(p *platform.LocalPlatform, stopRequested func() bool) *nativeTools {
	return &nativeTools{platform: p, stopRequested: stopRequested}
}

func (t *nativeTools) execute(call nativeToolCall) nativeToolResult {
	if call.ID == "" {
		call.ID = call.Name
	}
	args := map[string]interface{}{}
	if strings.TrimSpace(call.Arguments) != "" {
		if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
			return nativeToolResult{CallID: call.ID, Name: call.Name, Output: "invalid tool arguments: " + err.Error(), IsError: true}
		}
	}
	output, err := t.executeParsed(call.Name, args)
	if err != nil {
		return nativeToolResult{CallID: call.ID, Name: call.Name, Output: err.Error(), IsError: true}
	}
	return nativeToolResult{CallID: call.ID, Name: call.Name, Output: output}
}

func (t *nativeTools) executeParsed(name string, args map[string]interface{}) (string, error) {
	switch name {
	case "read":
		return t.read(argString(args, "path"))
	case "write":
		return t.write(argString(args, "path"), argString(args, "content"))
	case "edit":
		return t.edit(argString(args, "path"), argString(args, "old_string"), argString(args, "new_string"), argBool(args, "replace_all"))
	case "bash":
		return t.bash(argString(args, "command"), argInt(args, "timeout_seconds"))
	case "ls":
		return t.ls(argString(args, "path"))
	case "grep":
		return t.grep(argString(args, "pattern"), argString(args, "path"), argInt(args, "max_matches"))
	case "find":
		return t.find(argString(args, "pattern"), argString(args, "path"), argInt(args, "max_matches"))
	default:
		return "", fmt.Errorf("unknown tool %q; available tools are read, write, edit, bash, ls, grep, and find", name)
	}
}

func (t *nativeTools) read(p string) (string, error) {
	full, err := t.resolvePath(p)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (t *nativeTools) write(p, content string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("path is required")
	}
	full, err := t.resolvePath(p)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		return "", err
	}
	return "wrote " + t.displayPath(full), nil
}

func (t *nativeTools) edit(p, oldString, newString string, replaceAll bool) (string, error) {
	if oldString == "" {
		return "", fmt.Errorf("old_string is required")
	}
	full, err := t.resolvePath(p)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	text := string(data)
	count := strings.Count(text, oldString)
	if count == 0 {
		return "", fmt.Errorf("old_string not found in %s", p)
	}
	n := 1
	if replaceAll {
		n = -1
	}
	updated := strings.Replace(text, oldString, newString, n)
	if err := os.WriteFile(full, []byte(updated), 0o644); err != nil {
		return "", err
	}
	if !replaceAll {
		count = 1
	}
	return fmt.Sprintf("edited %s (%d replacement%s)", p, count, plural(count)), nil
}

func (t *nativeTools) bash(command string, timeout int) (string, error) {
	if strings.TrimSpace(command) == "" {
		return "", fmt.Errorf("command is required")
	}
	if timeout <= 0 || timeout > defaultToolTimeoutSec {
		timeout = defaultToolTimeoutSec
	}
	result := t.platform.Run([]string{"bash", "-lc", command}, "", nil, timeout, t.stopRequested, nil)
	var parts []string
	if stdout := strings.Join(result.RawLines, "\n"); stdout != "" {
		parts = append(parts, "[stdout]\n"+stdout)
	}
	if stderr := strings.TrimSpace(result.Stderr); stderr != "" {
		parts = append(parts, "[stderr]\n"+stderr)
	}
	if result.InfraError != "" {
		parts = append(parts, "[error]\n"+result.InfraError)
		return strings.Join(parts, "\n"), errors.New(strings.Join(parts, "\n"))
	}
	if result.ReturnCode != 0 {
		parts = append(parts, fmt.Sprintf("[exit_code]\n%d", result.ReturnCode))
	}
	if len(parts) == 0 {
		return "command completed with no output", nil
	}
	return strings.Join(parts, "\n"), nil
}

func (t *nativeTools) ls(p string) (string, error) {
	full, err := t.resolvePath(defaultString(p, "."))
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		return "", err
	}
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}
		lines = append(lines, name)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n"), nil
}

func (t *nativeTools) grep(pattern, p string, maxMatches int) (string, error) {
	if pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	if maxMatches <= 0 || maxMatches > 500 {
		maxMatches = 100
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", err
	}
	root, err := t.resolvePath(defaultString(p, "."))
	if err != nil {
		return "", err
	}
	var matches []string
	visit := func(file string) error {
		if len(matches) >= maxMatches {
			return nil
		}
		info, err := os.Stat(file)
		if err != nil || info.IsDir() || info.Size() > 2<<20 {
			return nil
		}
		data, err := os.ReadFile(file)
		if err != nil || bytes.IndexByte(data, 0) >= 0 {
			return nil
		}
		rel := t.displayPath(file)
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if re.MatchString(line) {
				matches = append(matches, fmt.Sprintf("%s:%d:%s", rel, i+1, line))
				if len(matches) >= maxMatches {
					break
				}
			}
		}
		return nil
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		_ = visit(root)
	} else {
		_ = filepath.WalkDir(root, func(file string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() && shouldSkipDir(d.Name()) {
				if d != nil && d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.IsDir() {
				_ = visit(file)
			}
			return nil
		})
	}
	if len(matches) == 0 {
		return "no matches", nil
	}
	return strings.Join(matches, "\n"), nil
}

func (t *nativeTools) find(pattern, p string, maxMatches int) (string, error) {
	if pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	if maxMatches <= 0 || maxMatches > 1000 {
		maxMatches = 200
	}
	root, err := t.resolvePath(defaultString(p, "."))
	if err != nil {
		return "", err
	}
	var matches []string
	_ = filepath.WalkDir(root, func(file string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && shouldSkipDir(d.Name()) {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		rel := t.displayPath(file)
		base := path.Base(rel)
		if ok, _ := path.Match(pattern, rel); ok {
			matches = append(matches, rel)
		} else if ok, _ := path.Match(pattern, base); ok {
			matches = append(matches, rel)
		}
		return nil
	})
	sort.Strings(matches)
	if len(matches) > maxMatches {
		matches = matches[:maxMatches]
	}
	if len(matches) == 0 {
		return "no matches", nil
	}
	return strings.Join(matches, "\n"), nil
}

func (t *nativeTools) resolvePath(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("path is required")
	}
	workspace, err := filepath.Abs(t.platform.Workspace)
	if err != nil {
		return "", err
	}
	var full string
	if filepath.IsAbs(p) {
		full, err = filepath.Abs(filepath.Clean(p))
		if err != nil {
			return "", err
		}
		return full, nil
	} else {
		full = filepath.Join(workspace, p)
	}
	full, err = filepath.Abs(full)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(workspace, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside workspace %q", p, workspace)
	}
	return full, nil
}

func (t *nativeTools) displayPath(full string) string {
	workspace, err := filepath.Abs(t.platform.Workspace)
	if err == nil {
		if rel, relErr := filepath.Rel(workspace, full); relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(full)
}

func shouldSkipDir(name string) bool {
	switch name {
	case ".git", ".telos", "__pycache__", "node_modules", ".venv", "venv":
		return true
	default:
		return false
	}
}

func argString(args map[string]interface{}, key string) string {
	value, _ := args[key].(string)
	return value
}

func argBool(args map[string]interface{}, key string) bool {
	value, _ := args[key].(bool)
	return value
}

func argInt(args map[string]interface{}, key string) int {
	switch value := args[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	default:
		return 0
	}
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func plural(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}

// -- Session logging ---------------------------------------------------------

type nativeSessionLogger struct {
	path      string
	workspace string
}

func newNativeSessionLogger(path, workspace string) *nativeSessionLogger {
	return &nativeSessionLogger{path: path, workspace: workspace}
}

func (l *nativeSessionLogger) start() error {
	if l.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return err
	}
	return l.append(map[string]interface{}{
		"type":      "session",
		"version":   1,
		"id":        fmt.Sprintf("native-%d", time.Now().UnixNano()),
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"cwd":       l.workspace,
		"runtime":   "telos-native",
	})
}

func (l *nativeSessionLogger) user(text string) error {
	return l.message("user", map[string]interface{}{"content": text})
}

func (l *nativeSessionLogger) assistant(text, provider, model, stopReason string, stats game.TurnStats) error {
	msg := map[string]interface{}{
		"provider":   provider,
		"model":      model,
		"stopReason": stopReason,
		"content": []map[string]string{{
			"type": "text",
			"text": text,
		}},
		"usage": map[string]interface{}{
			"input":      stats.InputTokens,
			"output":     stats.OutputTokens,
			"cacheRead":  stats.CacheReadTokens,
			"cacheWrite": stats.CacheCreationTokens,
			"cost":       map[string]float64{"total": stats.CostUSD},
		},
	}
	return l.message("assistant", msg)
}

func (l *nativeSessionLogger) tool(result nativeToolResult) error {
	msg := map[string]interface{}{
		"toolCallId": result.CallID,
		"toolName":   result.Name,
		"isError":    result.IsError,
		"content": []map[string]string{{
			"type": "text",
			"text": result.Output,
		}},
	}
	return l.message("toolResult", msg)
}

func (l *nativeSessionLogger) message(role string, fields map[string]interface{}) error {
	if l.path == "" {
		return nil
	}
	msg := map[string]interface{}{
		"role":      role,
		"timestamp": time.Now().UnixMilli(),
	}
	for k, v := range fields {
		msg[k] = v
	}
	return l.append(map[string]interface{}{
		"type":      "message",
		"id":        fmt.Sprintf("%s-%d", role, time.Now().UnixNano()),
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"message":   msg,
	})
}

func (l *nativeSessionLogger) append(entry map[string]interface{}) error {
	if l.path == "" {
		return nil
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	return enc.Encode(entry)
}

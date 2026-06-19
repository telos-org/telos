package executor

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/responses"
	"github.com/telos-org/telos/internal/game"
)

// -- OpenAI-compatible transport (openai-go Responses API) -------------------

// openaiTransport drives the agent loop against an OpenAI-compatible endpoint
// using the official openai-go SDK. Telos talks to a LiteLLM proxy, so a single
// Responses transport serves every provider the proxy fronts: it streams over
// SSE, threads conversation state server-side via previous_response_id, and
// carries reasoning effort natively.
type openaiTransport struct {
	client          openai.Client
	model           string
	instructions    string
	reasoning       openai.ReasoningEffort
	maxOutputTokens int
	tools           []responses.ToolUnionParam
	state           *conversationState
	logger          *nativeSessionLogger
	sequence        int
	lastCostUSD     float64
	lastCostKnown   bool
}

func newOpenAITransport(httpClient *http.Client, cfg nativeProviderConfig, thinking string, maxOutputTokens int, task, role string, logger *nativeSessionLogger) *openaiTransport {
	initial := responses.ResponseInputParam{
		responses.ResponseInputItemParamOfMessage(task, responses.EasyInputMessageRoleUser),
	}
	reasoning := reasoningEffort(thinking)
	if cfg.Capability.SupportsReasoning != nil && !*cfg.Capability.SupportsReasoning {
		reasoning = ""
	}
	stateMode := cfg.Capability.StateMode
	if stateMode == "" {
		stateMode = "server_chain"
	}
	tools := nativeToolsForOpenAI()
	if cfg.Capability.SupportsFunctionCalling != nil && !*cfg.Capability.SupportsFunctionCalling {
		tools = nil
	}
	t := &openaiTransport{
		model:           cfg.Model,
		instructions:    nativeSystemPrompt(role),
		reasoning:       reasoning,
		maxOutputTokens: maxOutputTokens,
		tools:           tools,
		state:           newConversationState(initial, stateMode),
		logger:          logger,
	}
	opts := []option.RequestOption{
		option.WithAPIKey(cfg.APIKey),
		option.WithBaseURL(cfg.BaseURL),
		option.WithMaxRetries(0),
		option.WithMiddleware(t.captureResponseHeaders),
	}
	if httpClient != nil {
		opts = append(opts, option.WithHTTPClient(httpClient))
	}
	t.client = openai.NewClient(opts...)
	return t
}

func (t *openaiTransport) send(ctx context.Context) (agentTurn, error) {
	t.sequence++
	seq := t.sequence
	params := t.params()
	_ = t.logger.modelRequest(t.modelRequestLogData(seq, t.state.previousResponseID()))
	var final responses.Response
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		t.lastCostKnown = false
		t.lastCostUSD = 0
		final, err = t.streamResponse(ctx, params)
		if err == nil {
			break
		}
		var execErr *executorError
		if !errors.As(err, &execErr) {
			_ = t.logger.errorEvent(seq, err)
			return agentTurn{}, err
		}
		if !execErr.Retryable && isChainSpecificError(err) {
			break
		}
		if !execErr.Retryable {
			_ = t.logger.errorEvent(seq, err)
			return agentTurn{}, err
		}
		if attempt == 2 {
			break
		}
		delay := retryDelay(attempt)
		_ = t.logger.retry(seq, attempt+1, delay, execErr)
		select {
		case <-ctx.Done():
			return agentTurn{}, ctx.Err()
		case <-time.After(delay):
		}
	}
	if err != nil && t.state.mode == conversationStateServerChain && t.state.previousResponseID() != "" && isChainSpecificError(err) {
		t.state.fallbackToStatelessHistory()
		_ = t.logger.retry(seq, 1, 0, newExecutorError(errProviderUnavailable, "falling back to stateless_history"))
		_ = t.logger.modelRequest(t.modelRequestLogData(seq, ""))
		t.lastCostKnown = false
		t.lastCostUSD = 0
		final, err = t.streamResponse(ctx, t.params())
		if err != nil {
			_ = t.logger.errorEvent(seq, err)
			return agentTurn{}, err
		}
	}
	if err != nil {
		_ = t.logger.errorEvent(seq, err)
		return agentTurn{}, err
	}
	t.state.recordResponseID(final.ID)
	calls := responseToolCalls(final.Output)
	t.state.recordAssistantToolCalls(calls)
	stats := t.statsFromResponse(final)
	_ = t.logger.modelResponse(seq, final.ID, responseStopReason(final), stats)
	if final.Status == responses.ResponseStatusIncomplete && len(calls) > 0 && !toolCallsHaveCompleteArguments(calls) {
		reason := final.IncompleteDetails.Reason
		if reason == "" {
			reason = "unknown"
		}
		err := newExecutorError(errAgentIncomplete, reason+":incomplete_tool_arguments")
		_ = t.logger.errorEvent(seq, err)
		return agentTurn{stats: stats}, err
	}
	if final.Status == responses.ResponseStatusIncomplete && len(calls) == 0 {
		reason := final.IncompleteDetails.Reason
		if reason == "" {
			reason = "unknown"
		}
		err := newExecutorError(errAgentIncomplete, reason)
		_ = t.logger.errorEvent(seq, err)
		return agentTurn{stats: stats}, err
	}
	return agentTurn{
		text:       final.OutputText(),
		calls:      calls,
		stopReason: responseStopReason(final),
		stats:      stats,
	}, nil
}

func (t *openaiTransport) captureResponseHeaders(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
	resp, err := next(req)
	if resp != nil {
		if cost, ok := costFromResponseHeaders(resp.Header); ok {
			t.lastCostUSD = cost
			t.lastCostKnown = true
		}
	}
	return resp, err
}

func (t *openaiTransport) params() responses.ResponseNewParams {
	params := responses.ResponseNewParams{
		Model:        openai.ResponsesModel(t.model),
		Instructions: openai.String(t.instructions),
		Input:        responses.ResponseNewParamsInputUnion{OfInputItemList: t.state.requestInput()},
		Tools:        t.tools,
	}
	if previousID := t.state.previousResponseID(); previousID != "" {
		params.PreviousResponseID = openai.String(previousID)
	}
	if t.maxOutputTokens > 0 {
		params.MaxOutputTokens = openai.Int(int64(t.maxOutputTokens))
	}
	if t.reasoning != "" {
		params.Reasoning = openai.ReasoningParam{Effort: t.reasoning}
	}
	return params
}

// streamResponse consumes the SSE stream and returns the assembled response. We
// drive the request over SSE for resilience on long generations, but act on the
// fully-assembled response the terminal event carries.
func (t *openaiTransport) streamResponse(ctx context.Context, params responses.ResponseNewParams) (responses.Response, error) {
	stream := t.client.Responses.NewStreaming(ctx, params)
	defer stream.Close()

	var (
		final     responses.Response
		assembled bool
	)
	for stream.Next() {
		event := stream.Current()
		switch event.Type {
		case "response.completed", "response.incomplete":
			final = event.Response
			assembled = true
		case "response.failed":
			if msg := event.Response.Error.Message; msg != "" {
				return responses.Response{}, classifyProviderMessage(msg, 0)
			}
			return responses.Response{}, retryableExecutorError(errProviderUnavailable, "response_failed")
		case "error":
			return responses.Response{}, classifyProviderMessage(event.Message, 0)
		}
	}
	if err := stream.Err(); err != nil {
		return responses.Response{}, classifyProviderError(err)
	}
	if !assembled {
		return responses.Response{}, retryableExecutorError(errProviderUnavailable, "stream_ended_without_response")
	}
	return final, nil
}

func (t *openaiTransport) recordToolResults(results []nativeToolResult) {
	t.state.recordToolResults(results)
}

func (t *openaiTransport) recordCorrection(prompt string) {
	t.state.recordCorrection(prompt)
}

func (t *openaiTransport) modelRequestLogData(sequence int, previousID string) modelRequestLogData {
	return modelRequestLogData{
		Sequence:        sequence,
		PreviousID:      previousID,
		StateMode:       t.state.mode,
		Model:           t.model,
		MaxOutputTokens: t.maxOutputTokens,
		ToolCount:       len(t.tools),
		ReasoningEffort: string(t.reasoning),
	}
}

func retryDelay(attempt int) time.Duration {
	base := time.Duration(250*(1<<attempt)) * time.Millisecond
	return base + time.Duration(rand.Intn(125))*time.Millisecond
}

func compactHistory(history responses.ResponseInputParam) responses.ResponseInputParam {
	const maxItems = 80
	if len(history) <= maxItems {
		return history
	}
	// Keep the first item (the task) plus the most recent window. The window can
	// begin mid-turn, so drop any function_call_output whose matching
	// function_call fell outside it — the Responses API rejects an output with no
	// preceding call when there is no previous_response_id to anchor it.
	window := make(responses.ResponseInputParam, 0, maxItems)
	window = append(window, history[0])
	window = append(window, history[len(history)-maxItems+1:]...)
	return dropOrphanFunctionOutputs(window)
}

// dropOrphanFunctionOutputs removes function_call_output items whose
// function_call is not present in the same item slice. Only outputs are dropped,
// so every retained output keeps a matching call.
func dropOrphanFunctionOutputs(items responses.ResponseInputParam) responses.ResponseInputParam {
	callIDs := map[string]bool{}
	for _, item := range items {
		if item.OfFunctionCall != nil {
			callIDs[item.OfFunctionCall.CallID] = true
		}
	}
	out := make(responses.ResponseInputParam, 0, len(items))
	for _, item := range items {
		if fco := item.OfFunctionCallOutput; fco != nil && !callIDs[fco.CallID] {
			continue
		}
		out = append(out, item)
	}
	return out
}

func isChainSpecificError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	for _, needle := range []string{"previous_response_id", "previous response", "response chain", "conversation state", "not found"} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

func classifyProviderError(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		classified := classifyProviderMessage(err.Error(), apiErr.StatusCode)
		if execErr, ok := classified.(*executorError); ok {
			execErr.StatusCode = apiErr.StatusCode
		}
		return classified
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if urlErr.Timeout() {
			return retryableExecutorError(errProviderTimeout, err.Error())
		}
		return retryableExecutorError(errProviderUnavailable, err.Error())
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return retryableExecutorError(errProviderTimeout, err.Error())
	}
	if errors.Is(err, context.Canceled) {
		return newExecutorError(errStopped, err.Error())
	}
	return retryableExecutorError(errProviderUnavailable, err.Error())
}

func classifyProviderMessage(message string, statusCode int) error {
	lower := strings.ToLower(message)
	switch {
	case statusCode == http.StatusTooManyRequests:
		return retryableExecutorError(errProviderRateLimited, message)
	case statusCode == http.StatusRequestTimeout || strings.Contains(lower, "timeout"):
		return retryableExecutorError(errProviderTimeout, message)
	case statusCode >= 500:
		return retryableExecutorError(errProviderUnavailable, message)
	case statusCode == http.StatusBadRequest && (strings.Contains(lower, "context") || strings.Contains(lower, "token")):
		return newExecutorError(errProviderContextLimit, message)
	case statusCode >= 400:
		return newExecutorError(errProviderInvalidRequest, message)
	case strings.Contains(lower, "rate limit") || strings.Contains(lower, "too many request"):
		return retryableExecutorError(errProviderRateLimited, message)
	case strings.Contains(lower, "context length") || strings.Contains(lower, "maximum context"):
		return newExecutorError(errProviderContextLimit, message)
	default:
		return retryableExecutorError(errProviderUnavailable, message)
	}
}

func responseToolCalls(output []responses.ResponseOutputItemUnion) []nativeToolCall {
	var calls []nativeToolCall
	for _, item := range output {
		if item.Type == "function_call" {
			calls = append(calls, nativeToolCall{
				ID:        item.CallID,
				Name:      item.Name,
				Arguments: item.Arguments,
			})
		}
	}
	return calls
}

func toolCallsHaveCompleteArguments(calls []nativeToolCall) bool {
	for _, call := range calls {
		raw := strings.TrimSpace(call.Arguments)
		if raw == "" || !json.Valid([]byte(raw)) {
			return false
		}
	}
	return true
}

func responseStopReason(r responses.Response) string {
	if reason := r.IncompleteDetails.Reason; reason != "" {
		return reason
	}
	return string(r.Status)
}

func statsFromResponsesUsage(model string, usage responses.ResponseUsage) game.TurnStats {
	stats := game.TurnStats{
		Model:           model,
		InputTokens:     int(usage.InputTokens),
		OutputTokens:    int(usage.OutputTokens),
		CacheReadTokens: int(usage.InputTokensDetails.CachedTokens),
		CostUnavailable: true,
	}
	if pricing, ok := configuredModelPricing(model); ok {
		stats.CostUSD = pricing.cost(stats.InputTokens, stats.OutputTokens)
		stats.CostUnavailable = false
	}
	return stats
}

func (t *openaiTransport) statsFromResponse(response responses.Response) game.TurnStats {
	stats := statsFromResponsesUsage(t.model, response.Usage)
	if t.lastCostKnown {
		stats.CostUSD = t.lastCostUSD
		stats.CostUnavailable = false
	} else if cost, ok := costFromResponseBody(response.RawJSON()); ok {
		stats.CostUSD = cost
		stats.CostUnavailable = false
	}
	return stats
}

func costFromResponseHeaders(headers http.Header) (float64, bool) {
	for _, name := range []string{
		"x-litellm-response-cost",
		"x-litellm-cost",
		"x-response-cost",
		"x-litellm-spend",
	} {
		raw := strings.TrimSpace(headers.Get(name))
		if raw == "" {
			continue
		}
		raw = strings.TrimPrefix(raw, "$")
		cost, err := strconv.ParseFloat(raw, 64)
		if err == nil && cost >= 0 {
			return cost, true
		}
	}
	return 0, false
}

func costFromResponseBody(raw string) (float64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	var value any
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		return 0, false
	}
	return costFromJSONValue(value)
}

func costFromJSONValue(value any) (float64, bool) {
	switch v := value.(type) {
	case map[string]any:
		for _, key := range []string{
			"response_cost",
			"litellm_response_cost",
			"litellm_cost",
			"cost",
			"total_cost",
			"total_cost_usd",
		} {
			if raw, ok := v[key]; ok {
				if cost, ok := parseCostValue(raw); ok {
					return cost, true
				}
			}
		}
		for _, key := range []string{"metadata", "litellm_metadata", "response_metadata"} {
			if raw, ok := v[key]; ok {
				if cost, ok := costFromJSONValue(raw); ok {
					return cost, true
				}
			}
		}
	case []any:
		for _, item := range v {
			if cost, ok := costFromJSONValue(item); ok {
				return cost, true
			}
		}
	}
	return 0, false
}

func parseCostValue(value any) (float64, bool) {
	switch v := value.(type) {
	case json.Number:
		cost, err := v.Float64()
		return cost, err == nil && cost >= 0
	case float64:
		return v, v >= 0
	case string:
		raw := strings.TrimSpace(strings.TrimPrefix(v, "$"))
		cost, err := strconv.ParseFloat(raw, 64)
		return cost, err == nil && cost >= 0
	default:
		return 0, false
	}
}

type modelPricing struct {
	InputUSDPer1MTokens  float64 `json:"input_usd_per_1m_tokens"`
	OutputUSDPer1MTokens float64 `json:"output_usd_per_1m_tokens"`
}

func (p modelPricing) cost(inputTokens, outputTokens int) float64 {
	input := float64(inputTokens) * p.InputUSDPer1MTokens / 1_000_000
	output := float64(outputTokens) * p.OutputUSDPer1MTokens / 1_000_000
	return input + output
}

func configuredModelPricing(model string) (modelPricing, bool) {
	raw := strings.TrimSpace(os.Getenv("TELOS_MODEL_PRICING_TABLE"))
	if raw == "" {
		return modelPricing{}, false
	}
	var table map[string]modelPricing
	if err := json.Unmarshal([]byte(raw), &table); err != nil {
		return modelPricing{}, false
	}
	pricing, ok := table[model]
	if !ok || pricing.InputUSDPer1MTokens < 0 || pricing.OutputUSDPer1MTokens < 0 {
		return modelPricing{}, false
	}
	if pricing.InputUSDPer1MTokens == 0 && pricing.OutputUSDPer1MTokens == 0 {
		return modelPricing{}, false
	}
	return pricing, true
}

// pricingConfiguredFor reports whether an explicit per-model price is set in
// TELOS_MODEL_PRICING_TABLE, without exposing the rates. Used for audit logging.
func pricingConfiguredFor(model string) bool {
	_, ok := configuredModelPricing(model)
	return ok
}

func reasoningEffort(thinking string) openai.ReasoningEffort {
	switch openai.ReasoningEffort(thinking) {
	case openai.ReasoningEffortLow:
		return openai.ReasoningEffortLow
	case openai.ReasoningEffortMedium:
		return openai.ReasoningEffortMedium
	case openai.ReasoningEffortHigh:
		return openai.ReasoningEffortHigh
	default:
		return ""
	}
}

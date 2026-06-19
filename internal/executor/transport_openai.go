package executor

import (
	"context"
	"fmt"
	"net/http"

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
	input           responses.ResponseInputParam
	previousID      string
}

func newOpenAITransport(httpClient *http.Client, cfg nativeProviderConfig, thinking string, maxOutputTokens int, task, role string) *openaiTransport {
	opts := []option.RequestOption{
		option.WithAPIKey(cfg.APIKey),
		option.WithBaseURL(cfg.BaseURL),
	}
	if httpClient != nil {
		opts = append(opts, option.WithHTTPClient(httpClient))
	}
	return &openaiTransport{
		client:          openai.NewClient(opts...),
		model:           cfg.Model,
		instructions:    nativeSystemPrompt(role),
		reasoning:       reasoningEffort(thinking),
		maxOutputTokens: maxOutputTokens,
		tools:           nativeToolsForOpenAI(),
		input: responses.ResponseInputParam{
			responses.ResponseInputItemParamOfMessage(task, responses.EasyInputMessageRoleUser),
		},
	}
}

func (t *openaiTransport) send(ctx context.Context) (agentTurn, error) {
	params := responses.ResponseNewParams{
		Model:        openai.ResponsesModel(t.model),
		Instructions: openai.String(t.instructions),
		Input:        responses.ResponseNewParamsInputUnion{OfInputItemList: t.input},
		Tools:        t.tools,
	}
	if t.previousID != "" {
		params.PreviousResponseID = openai.String(t.previousID)
	}
	if t.maxOutputTokens > 0 {
		params.MaxOutputTokens = openai.Int(int64(t.maxOutputTokens))
	}
	if t.reasoning != "" {
		params.Reasoning = openai.ReasoningParam{Effort: t.reasoning}
	}

	final, err := t.streamResponse(ctx, params)
	if err != nil {
		return agentTurn{}, err
	}
	t.previousID = final.ID
	return agentTurn{
		text:       final.OutputText(),
		calls:      responseToolCalls(final.Output),
		stopReason: responseStopReason(final),
		stats:      statsFromResponsesUsage(t.model, final.Usage),
	}, nil
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
				return responses.Response{}, fmt.Errorf("provider_error:%s", msg)
			}
			return responses.Response{}, fmt.Errorf("provider_error:response_failed")
		case "error":
			return responses.Response{}, fmt.Errorf("provider_error:%s", event.Message)
		}
	}
	if err := stream.Err(); err != nil {
		return responses.Response{}, fmt.Errorf("provider_error:%w", err)
	}
	if !assembled {
		return responses.Response{}, fmt.Errorf("provider_error:stream_ended_without_response")
	}
	return final, nil
}

func (t *openaiTransport) recordToolResults(results []nativeToolResult) {
	t.input = make(responses.ResponseInputParam, 0, len(results))
	for _, result := range results {
		t.input = append(t.input, responses.ResponseInputItemParamOfFunctionCallOutput(result.CallID, result.Output))
	}
}

func (t *openaiTransport) recordCorrection(prompt string) {
	t.input = responses.ResponseInputParam{
		responses.ResponseInputItemParamOfMessage(prompt, responses.EasyInputMessageRoleUser),
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

func responseStopReason(r responses.Response) string {
	if reason := r.IncompleteDetails.Reason; reason != "" {
		return reason
	}
	return string(r.Status)
}

func statsFromResponsesUsage(model string, usage responses.ResponseUsage) game.TurnStats {
	return game.TurnStats{
		Model:           model,
		InputTokens:     int(usage.InputTokens),
		OutputTokens:    int(usage.OutputTokens),
		CacheReadTokens: int(usage.InputTokensDetails.CachedTokens),
	}
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

package executor

import (
	"context"
	"fmt"

	"github.com/telos-org/telos/internal/game"
)

// -- OpenAI Chat Completions transport ---------------------------------------

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

type chatTransport struct {
	poster          httpPoster
	model           string
	maxOutputTokens int
	schemas         []map[string]interface{}
	messages        []chatMessage
	last            chatMessage
}

func newChatTransport(poster httpPoster, model string, maxOutputTokens int, task, role string) *chatTransport {
	return &chatTransport{
		poster:          poster,
		model:           model,
		maxOutputTokens: maxOutputTokens,
		schemas:         toolSchemasForChat(),
		messages: []chatMessage{
			{Role: "system", Content: nativeSystemPrompt(role)},
			{Role: "user", Content: task},
		},
	}
}

func (t *chatTransport) send(ctx context.Context) (agentTurn, error) {
	var response struct {
		Choices []struct {
			Message      chatMessage `json:"message"`
			FinishReason string      `json:"finish_reason"`
		} `json:"choices"`
		Usage chatUsage `json:"usage"`
		Error apiError  `json:"error"`
	}
	req := map[string]interface{}{
		"model":       t.model,
		"messages":    t.messages,
		"tools":       t.schemas,
		"tool_choice": "auto",
		"max_tokens":  t.maxOutputTokens,
	}
	if err := t.poster.postJSON(ctx, "/chat/completions", req, &response); err != nil {
		return agentTurn{}, err
	}
	if response.Error.Message != "" {
		return agentTurn{}, fmt.Errorf("provider_error:%s", response.Error.Message)
	}
	if len(response.Choices) == 0 {
		return agentTurn{}, fmt.Errorf("provider_error:no_choices")
	}
	msg := response.Choices[0].Message
	if msg.Role == "" {
		msg.Role = "assistant"
	}
	t.last = msg
	return agentTurn{
		text:       msg.Content,
		calls:      chatToolCallsToNative(msg.ToolCalls),
		stopReason: response.Choices[0].FinishReason,
		stats:      statsFromChatUsage(t.model, response.Usage),
	}, nil
}

func (t *chatTransport) recordToolResults(results []nativeToolResult) {
	t.messages = append(t.messages, t.last)
	for _, result := range results {
		t.messages = append(t.messages, chatMessage{
			Role:       "tool",
			ToolCallID: result.CallID,
			Content:    result.Output,
		})
	}
}

func (t *chatTransport) recordCorrection(prompt string) {
	t.messages = append(t.messages, t.last, chatMessage{Role: "user", Content: prompt})
}

func chatToolCallsToNative(calls []chatToolCall) []nativeToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]nativeToolCall, 0, len(calls))
	for _, call := range calls {
		out = append(out, nativeToolCall{
			ID:        call.ID,
			Name:      call.Function.Name,
			Arguments: call.Function.Arguments,
		})
	}
	return out
}

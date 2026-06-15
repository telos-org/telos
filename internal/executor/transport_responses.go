package executor

import (
	"context"
	"fmt"
	"strings"

	"github.com/telos-org/telos/internal/game"
)

// -- OpenAI Responses transport ----------------------------------------------

type responseItem struct {
	Type      string                   `json:"type"`
	ID        string                   `json:"id,omitempty"`
	CallID    string                   `json:"call_id,omitempty"`
	Name      string                   `json:"name,omitempty"`
	Arguments string                   `json:"arguments,omitempty"`
	Content   []map[string]interface{} `json:"content,omitempty"`
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

type responsesTransport struct {
	poster          httpPoster
	model           string
	thinking        string
	maxOutputTokens int
	schemas         []map[string]interface{}
	input           []interface{}
	previousID      string
}

func newResponsesTransport(poster httpPoster, model, thinking string, maxOutputTokens int, task, role string) *responsesTransport {
	return &responsesTransport{
		poster:          poster,
		model:           model,
		thinking:        thinking,
		maxOutputTokens: maxOutputTokens,
		schemas:         toolSchemasForResponses(),
		input: []interface{}{
			map[string]interface{}{"role": "user", "content": nativeSystemPrompt(role) + "\n\n# Assignment\n\n" + task},
		},
	}
}

func (t *responsesTransport) send(ctx context.Context) (agentTurn, error) {
	var response struct {
		ID                string `json:"id"`
		Status            string `json:"status"`
		IncompleteDetails struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Output []responseItem `json:"output"`
		Usage  responsesUsage `json:"usage"`
		Error  apiError       `json:"error"`
	}
	req := map[string]interface{}{
		"model":             t.model,
		"input":             t.input,
		"tools":             t.schemas,
		"max_output_tokens": t.maxOutputTokens,
	}
	if t.previousID != "" {
		req["previous_response_id"] = t.previousID
	}
	if t.thinking != "" {
		req["reasoning"] = map[string]interface{}{"effort": t.thinking}
	}
	if err := t.poster.postJSON(ctx, "/responses", req, &response); err != nil {
		return agentTurn{}, err
	}
	if response.Error.Message != "" {
		return agentTurn{}, fmt.Errorf("provider_error:%s", response.Error.Message)
	}
	t.previousID = response.ID
	text, calls := parseResponseOutput(response.Output)
	stopReason := "stop"
	if response.Status == "incomplete" && response.IncompleteDetails.Reason != "" {
		stopReason = response.IncompleteDetails.Reason
	}
	return agentTurn{
		text:       text,
		calls:      calls,
		stopReason: stopReason,
		stats:      statsFromResponsesUsage(t.model, response.Usage),
	}, nil
}

func (t *responsesTransport) recordToolResults(results []nativeToolResult) {
	t.input = nil
	for _, result := range results {
		t.input = append(t.input, map[string]interface{}{
			"type":    "function_call_output",
			"call_id": result.CallID,
			"output":  result.Output,
		})
	}
}

func (t *responsesTransport) recordCorrection(prompt string) {
	t.input = []interface{}{
		map[string]interface{}{"role": "user", "content": prompt},
	}
}

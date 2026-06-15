package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/telos-org/telos/internal/game"
)

// -- Anthropic Messages transport --------------------------------------------

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

type anthropicTransport struct {
	poster          httpPoster
	model           string
	system          string
	maxOutputTokens int
	schemas         []map[string]interface{}
	messages        []anthropicMessage
	last            []anthropicBlock
}

func newAnthropicTransport(poster httpPoster, model string, maxOutputTokens int, task, role string) *anthropicTransport {
	return &anthropicTransport{
		poster:          poster,
		model:           model,
		system:          nativeSystemPrompt(role),
		maxOutputTokens: maxOutputTokens,
		schemas:         toolSchemasForAnthropic(),
		messages:        []anthropicMessage{{Role: "user", Content: []anthropicBlock{{Type: "text", Text: task}}}},
	}
}

func (t *anthropicTransport) send(ctx context.Context) (agentTurn, error) {
	var response struct {
		ID         string           `json:"id"`
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
		"model":      t.model,
		"system":     t.system,
		"messages":   t.messages,
		"tools":      t.schemas,
		"max_tokens": t.maxOutputTokens,
	}
	if err := t.poster.postAnthropic(ctx, "/messages", req, &response); err != nil {
		return agentTurn{}, err
	}
	if response.Error.Message != "" {
		return agentTurn{}, fmt.Errorf("provider_error:%s", response.Error.Message)
	}
	t.last = response.Content
	text, calls := parseAnthropicOutput(response.Content)
	return agentTurn{
		text:       text,
		calls:      calls,
		stopReason: response.StopReason,
		stats: game.TurnStats{
			Model:               t.model,
			InputTokens:         response.Usage.InputTokens,
			OutputTokens:        response.Usage.OutputTokens,
			CacheReadTokens:     response.Usage.CacheReadInputTokens,
			CacheCreationTokens: response.Usage.CacheCreationInputTokens,
		},
	}, nil
}

func (t *anthropicTransport) recordToolResults(results []nativeToolResult) {
	t.messages = append(t.messages, anthropicMessage{Role: "assistant", Content: t.last})
	blocks := make([]anthropicBlock, 0, len(results))
	for _, result := range results {
		blocks = append(blocks, anthropicBlock{
			Type:      "tool_result",
			ToolUseID: result.CallID,
			Content:   result.Output,
			IsError:   result.IsError,
		})
	}
	t.messages = append(t.messages, anthropicMessage{Role: "user", Content: blocks})
}

func (t *anthropicTransport) recordCorrection(prompt string) {
	t.messages = append(t.messages,
		anthropicMessage{Role: "assistant", Content: t.last},
		anthropicMessage{Role: "user", Content: []anthropicBlock{{Type: "text", Text: prompt}}},
	)
}

package executor

import (
	"strings"

	"github.com/openai/openai-go/responses"
)

const (
	conversationStateServerChain      = "server_chain"
	conversationStateStatelessHistory = "stateless_history"
)

type conversationState struct {
	mode       string
	input      responses.ResponseInputParam
	history    responses.ResponseInputParam
	previousID string
}

func newConversationState(initial responses.ResponseInputParam, mode string) *conversationState {
	switch mode {
	case conversationStateServerChain, conversationStateStatelessHistory:
	default:
		mode = conversationStateStatelessHistory
	}
	return &conversationState{
		mode:    mode,
		input:   append(responses.ResponseInputParam{}, initial...),
		history: append(responses.ResponseInputParam{}, initial...),
	}
}

func (s *conversationState) requestInput() responses.ResponseInputParam {
	if s.mode == conversationStateStatelessHistory {
		return compactHistory(s.history)
	}
	return s.input
}

func (s *conversationState) previousResponseID() string {
	if s.mode != conversationStateServerChain {
		return ""
	}
	return s.previousID
}

func (s *conversationState) recordResponseID(id string) {
	s.previousID = id
}

func (s *conversationState) fallbackToStatelessHistory() {
	s.mode = conversationStateStatelessHistory
	s.previousID = ""
}

func (s *conversationState) recordToolResults(results []nativeToolResult) {
	s.input = make(responses.ResponseInputParam, 0, len(results))
	for _, result := range results {
		item := responses.ResponseInputItemParamOfFunctionCallOutput(result.CallID, result.Output)
		s.input = append(s.input, item)
		s.history = append(s.history, item)
	}
}

func (s *conversationState) recordCorrection(prompt string) {
	item := responses.ResponseInputItemParamOfMessage(prompt, responses.EasyInputMessageRoleUser)
	s.input = responses.ResponseInputParam{item}
	s.history = append(s.history, item)
}

func (s *conversationState) recordAssistantMessage(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	s.history = append(s.history, responses.ResponseInputItemParamOfMessage(text, responses.EasyInputMessageRoleAssistant))
}

func (s *conversationState) recordAssistantToolCalls(calls []nativeToolCall) {
	for _, call := range calls {
		s.history = append(s.history, responses.ResponseInputItemParamOfFunctionCall(call.Arguments, call.ID, call.Name))
	}
}

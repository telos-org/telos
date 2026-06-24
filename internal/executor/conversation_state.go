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
	mode              string
	input             responses.ResponseInputParam
	history           responses.ResponseInputParam
	previousID        string
	compactionSummary string
	firstKeptIndex    int
}

func newConversationState(initial responses.ResponseInputParam, mode string) *conversationState {
	switch mode {
	case conversationStateServerChain, conversationStateStatelessHistory:
	default:
		mode = conversationStateStatelessHistory
	}
	return &conversationState{
		mode:           mode,
		input:          append(responses.ResponseInputParam{}, initial...),
		history:        append(responses.ResponseInputParam{}, initial...),
		firstKeptIndex: 1,
	}
}

func (s *conversationState) requestInput() responses.ResponseInputParam {
	if s.mode != conversationStateStatelessHistory {
		return s.input
	}
	return s.inputWithSummary(s.compactionSummary, s.firstKeptIndex)
}

func (s *conversationState) compactionRequestInput(firstKeptIndex, summaryBudgetTokens int) responses.ResponseInputParam {
	out := s.inputWithSummary(s.compactionSummary, s.firstKeptIndex)
	if firstKeptIndex > s.firstKeptIndex && firstKeptIndex <= len(s.history) {
		out = append(responses.ResponseInputParam{}, s.history[0])
		if strings.TrimSpace(s.compactionSummary) != "" {
			out = append(out, compactionSummaryMessage(s.compactionSummary))
		}
		out = append(out, s.history[s.firstKeptIndex:firstKeptIndex]...)
	}
	out = append(out, compactionCommandMessage(summaryBudgetTokens))
	return out
}

func (s *conversationState) inputWithSummary(summary string, firstKeptIndex int) responses.ResponseInputParam {
	if len(s.history) == 0 {
		return nil
	}
	if firstKeptIndex < 1 {
		firstKeptIndex = 1
	}
	if firstKeptIndex > len(s.history) {
		firstKeptIndex = len(s.history)
	}
	out := make(responses.ResponseInputParam, 0, 1+len(s.history)-firstKeptIndex+1)
	out = append(out, s.history[0])
	if strings.TrimSpace(summary) != "" {
		out = append(out, compactionSummaryMessage(summary))
	}
	out = append(out, s.history[firstKeptIndex:]...)
	return out
}

func (s *conversationState) applyCompaction(summary string, firstKeptIndex int) {
	s.compactionSummary = strings.TrimSpace(summary)
	s.firstKeptIndex = firstKeptIndex
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

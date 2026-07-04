package executor

import (
	"strings"

	"github.com/openai/openai-go/responses"
	"github.com/telos-org/telos/internal/executor/providercore"
)

const (
	conversationStateServerChain      = "server_chain"
	conversationStateStatelessHistory = "stateless_history"
)

// messageInputItem builds a Responses API message input item with an explicit
// "type":"message". The OpenAI Go SDK omits the optional type on "easy input"
// messages, and while OpenAI itself defaults the missing field, some
// OpenAI-compatible gateways (e.g. Wafer) reject an input item whose type is
// absent ("input[0].type=None is not supported"). Setting it explicitly is
// spec-compliant for every provider and keeps history items portable.
func messageInputItem(text string, role responses.EasyInputMessageRole) responses.ResponseInputItemUnionParam {
	item := responses.ResponseInputItemParamOfMessage(text, role)
	if item.OfMessage != nil {
		item.OfMessage.Type = responses.EasyInputMessageTypeMessage
	}
	return item
}

type conversationState struct {
	mode              string
	input             responses.ResponseInputParam
	history           responses.ResponseInputParam
	coreInput         []providercore.Message
	coreHistory       []providercore.Message
	previousID        string
	compactionSummary string
	firstKeptIndex    int
}

func newConversationState(initial responses.ResponseInputParam, mode string) *conversationState {
	coreInitial := coreMessagesFromInitialInput(initial)
	switch mode {
	case conversationStateServerChain, conversationStateStatelessHistory:
	default:
		mode = conversationStateStatelessHistory
	}
	return &conversationState{
		mode:           mode,
		input:          append(responses.ResponseInputParam{}, initial...),
		history:        append(responses.ResponseInputParam{}, initial...),
		coreInput:      append([]providercore.Message(nil), coreInitial...),
		coreHistory:    append([]providercore.Message(nil), coreInitial...),
		firstKeptIndex: 1,
	}
}

func (s *conversationState) requestInput() responses.ResponseInputParam {
	if s.mode != conversationStateStatelessHistory {
		return s.input
	}
	return s.inputWithSummary(s.compactionSummary, s.firstKeptIndex)
}

func (s *conversationState) coreRequestMessages() []providercore.Message {
	if s.mode != conversationStateStatelessHistory {
		return append([]providercore.Message(nil), s.coreInput...)
	}
	if len(s.coreHistory) == 0 {
		return nil
	}
	firstKeptIndex := s.firstKeptIndex
	if firstKeptIndex < 1 {
		firstKeptIndex = 1
	}
	if firstKeptIndex > len(s.coreHistory) {
		firstKeptIndex = len(s.coreHistory)
	}
	out := make([]providercore.Message, 0, 1+len(s.coreHistory)-firstKeptIndex+1)
	out = append(out, s.coreHistory[0])
	if strings.TrimSpace(s.compactionSummary) != "" {
		out = append(out, providercore.Message{Role: providercore.RoleUser, Content: compactionSummaryMessageText(s.compactionSummary)})
	}
	out = append(out, s.coreHistory[firstKeptIndex:]...)
	return out
}

func (s *conversationState) compactionRequestInput(firstKeptIndex, summaryBudgetTokens int) responses.ResponseInputParam {
	return s.compactionRequestSpanInput(s.firstKeptIndex, firstKeptIndex, summaryBudgetTokens)
}

func (s *conversationState) compactionRequestSpanInput(startIndex, endIndex, summaryBudgetTokens int) responses.ResponseInputParam {
	var out responses.ResponseInputParam
	if startIndex < s.firstKeptIndex {
		startIndex = s.firstKeptIndex
	}
	if startIndex < 1 {
		startIndex = 1
	}
	if endIndex > len(s.history) {
		endIndex = len(s.history)
	}
	if endIndex < startIndex {
		endIndex = startIndex
	}
	if endIndex > startIndex && endIndex <= len(s.history) {
		// Summarize only the window (firstKeptIndex .. now) being compacted: the
		// seed task, any prior summary, then the to-be-summarized slice.
		out = append(out, s.history[0])
		if strings.TrimSpace(s.compactionSummary) != "" {
			out = append(out, compactionSummaryMessage(s.compactionSummary))
		}
		out = append(out, s.history[startIndex:endIndex]...)
	} else {
		out = s.inputWithSummary(s.compactionSummary, s.firstKeptIndex)
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
	id = strings.TrimSpace(id)
	if id == "" {
		s.fallbackToStatelessHistory()
		return
	}
	s.previousID = id
}

func (s *conversationState) fallbackToStatelessHistory() {
	s.mode = conversationStateStatelessHistory
	s.previousID = ""
}

func (s *conversationState) recordToolResults(results []nativeToolResult) {
	s.input = make(responses.ResponseInputParam, 0, len(results))
	s.coreInput = make([]providercore.Message, 0, len(results))
	for _, result := range results {
		item := responses.ResponseInputItemParamOfFunctionCallOutput(result.CallID, result.Output)
		s.input = append(s.input, item)
		s.history = append(s.history, item)
		msg := providercore.Message{
			Role:       providercore.RoleTool,
			Content:    result.Output,
			ToolCallID: result.CallID,
			ToolName:   result.Name,
		}
		s.coreInput = append(s.coreInput, msg)
		s.coreHistory = append(s.coreHistory, msg)
	}
}

func (s *conversationState) recordCorrection(prompt string) {
	item := messageInputItem(prompt, responses.EasyInputMessageRoleUser)
	s.input = responses.ResponseInputParam{item}
	s.history = append(s.history, item)
	msg := providercore.Message{Role: providercore.RoleUser, Content: prompt}
	s.coreInput = []providercore.Message{msg}
	s.coreHistory = append(s.coreHistory, msg)
}

func (s *conversationState) recordAssistantMessage(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	s.history = append(s.history, messageInputItem(text, responses.EasyInputMessageRoleAssistant))
	s.coreHistory = append(s.coreHistory, providercore.Message{Role: providercore.RoleAssistant, Content: text})
}

func (s *conversationState) recordAssistantToolCalls(calls []nativeToolCall) {
	if len(calls) == 0 {
		return
	}
	coreCalls := make([]providercore.ToolCall, 0, len(calls))
	for _, call := range calls {
		s.history = append(s.history, responses.ResponseInputItemParamOfFunctionCall(call.Arguments, call.ID, call.Name))
		coreCalls = append(coreCalls, providercore.ToolCall{ID: call.ID, Name: call.Name, Arguments: call.Arguments})
	}
	s.coreHistory = append(s.coreHistory, providercore.Message{Role: providercore.RoleAssistant, ToolCalls: coreCalls})
}

func coreMessagesFromInitialInput(input responses.ResponseInputParam) []providercore.Message {
	var out []providercore.Message
	for _, item := range input {
		if item.OfMessage == nil {
			continue
		}
		out = append(out, providercore.Message{
			Role:    coreRoleFromOpenAI(item.OfMessage.Role),
			Content: item.OfMessage.Content.OfString.Value,
		})
	}
	return out
}

func coreRoleFromOpenAI(role responses.EasyInputMessageRole) providercore.Role {
	switch role {
	case responses.EasyInputMessageRoleAssistant:
		return providercore.RoleAssistant
	default:
		return providercore.RoleUser
	}
}

func compactionSummaryMessageText(summary string) string {
	return compactionSummaryMessagePrefix + strings.TrimSpace(summary)
}

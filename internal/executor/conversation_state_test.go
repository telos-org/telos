package executor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/openai/openai-go/responses"
)

func TestRequestInputStatelessReturnsHistoryWhenUncompacted(t *testing.T) {
	s := newConversationState(nil, conversationStateStatelessHistory)
	s.history = responses.ResponseInputParam{messageItem("task")}
	for i := 1; i <= 200; i++ {
		s.history = append(s.history, messageItem(fmt.Sprintf("m-%d", i)))
	}

	got := s.requestInput()

	if len(got) != len(s.history) {
		t.Fatalf("uncompacted stateless history must resend history as-is: got %d want %d", len(got), len(s.history))
	}
}

func TestRequestInputStatelessUsesSummaryAndRecentHistory(t *testing.T) {
	s := newConversationState(nil, conversationStateStatelessHistory)
	s.history = responses.ResponseInputParam{
		messageItem("task"),
		messageItem("old"),
		messageItem("recent-1"),
		messageItem("recent-2"),
	}
	s.applyCompaction(validCompactionSummary("state"), 2)

	got := s.requestInput()

	if len(got) != 4 {
		t.Fatalf("task + summary + recent items: got %d", len(got))
	}
	body := requestText(got)
	if !strings.Contains(body, "Compacted prior session state") || !strings.Contains(body, "recent-1") || strings.Contains(body, "\nold\n") {
		t.Fatalf("rebuilt input should use task, summary, and recent history:\n%s", body)
	}
}

func TestRecordResponseIDEmptyFallsBackToStatelessHistory(t *testing.T) {
	s := newConversationState(nil, conversationStateServerChain)
	s.recordResponseID("resp_1")
	if got := s.previousResponseID(); got != "resp_1" {
		t.Fatalf("previous response ID: got %q", got)
	}

	s.recordResponseID("")

	if s.mode != conversationStateStatelessHistory {
		t.Fatalf("mode: got %q", s.mode)
	}
	if got := s.previousResponseID(); got != "" {
		t.Fatalf("previous response ID should be cleared, got %q", got)
	}
}

func messageItem(text string) responses.ResponseInputItemUnionParam {
	return responses.ResponseInputItemParamOfMessage(text, responses.EasyInputMessageRoleUser)
}

func functionCallItem(callID string) responses.ResponseInputItemUnionParam {
	return responses.ResponseInputItemParamOfFunctionCall(`{"path":"answer.txt"}`, callID, "read")
}

func hasOrphanFunctionOutput(items responses.ResponseInputParam) bool {
	calls := map[string]bool{}
	for _, item := range items {
		if item.OfFunctionCall != nil {
			calls[item.OfFunctionCall.CallID] = true
		}
	}
	for _, item := range items {
		if item.OfFunctionCallOutput != nil && !calls[item.OfFunctionCallOutput.CallID] {
			return true
		}
	}
	return false
}

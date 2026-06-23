package executor

import (
	"fmt"
	"testing"

	"github.com/openai/openai-go/responses"
)

func TestCompactHistoryReturnsShortHistoryUnchanged(t *testing.T) {
	history := responses.ResponseInputParam{
		messageItem("task"),
		messageItem("assistant"),
		functionCallItem("call_1"),
		functionOutputItem("call_1"),
	}

	got := compactHistory(history)

	if len(got) != len(history) {
		t.Fatalf("len: got %d want %d", len(got), len(history))
	}
	if &got[0] != &history[0] {
		t.Fatal("short history should return the original slice")
	}
}

func TestCompactHistoryPreservesTaskAndKeepsRecentWindow(t *testing.T) {
	history := responses.ResponseInputParam{messageItem("task")}
	for i := 1; i <= 84; i++ {
		history = append(history, messageItem(fmt.Sprintf("message-%02d", i)))
	}

	got := compactHistory(history)

	if len(got) != 80 {
		t.Fatalf("len: got %d want 80", len(got))
	}
	if got[0].OfMessage.Content.OfString.Value != "task" {
		t.Fatalf("first item: got %#v", got[0].OfMessage)
	}
	if got[1].OfMessage.Content.OfString.Value != "message-06" {
		t.Fatalf("window start: got %q", got[1].OfMessage.Content.OfString.Value)
	}
	if got[len(got)-1].OfMessage.Content.OfString.Value != "message-84" {
		t.Fatalf("window end: got %q", got[len(got)-1].OfMessage.Content.OfString.Value)
	}
}

func TestCompactHistoryDropsOrphanFunctionOutputsAtWindowBoundary(t *testing.T) {
	history := responses.ResponseInputParam{messageItem("task")}
	for i := 1; i <= 4; i++ {
		history = append(history, messageItem(fmt.Sprintf("old-%d", i)))
	}
	history = append(history, functionCallItem("call_dropped"))
	history = append(history, functionOutputItem("call_dropped"))
	for i := 1; i <= 78; i++ {
		history = append(history, messageItem(fmt.Sprintf("recent-%d", i)))
	}

	got := compactHistory(history)

	if hasFunctionOutput(got, "call_dropped") {
		t.Fatal("orphan output was retained")
	}
	if hasOrphanFunctionOutput(got) {
		t.Fatal("compacted history contains orphan function output")
	}
}

func TestCompactHistoryRetainsMatchedFunctionOutputs(t *testing.T) {
	history := responses.ResponseInputParam{messageItem("task")}
	for i := 1; i <= 3; i++ {
		history = append(history, messageItem(fmt.Sprintf("old-%d", i)))
	}
	history = append(history, functionCallItem("call_kept"))
	history = append(history, functionOutputItem("call_kept"))
	for i := 1; i <= 77; i++ {
		history = append(history, messageItem(fmt.Sprintf("recent-%d", i)))
	}

	got := compactHistory(history)

	if !hasFunctionOutput(got, "call_kept") {
		t.Fatal("matched output was dropped")
	}
	if hasOrphanFunctionOutput(got) {
		t.Fatal("compacted history contains orphan function output")
	}
}

func TestCompactHistoryDropsAssistantMessagesInRemovedMiddle(t *testing.T) {
	history := responses.ResponseInputParam{messageItem("task")}
	history = append(history, assistantMessageItem("assistant-dropped"))
	for i := 1; i <= 83; i++ {
		history = append(history, messageItem(fmt.Sprintf("recent-%d", i)))
	}

	got := compactHistory(history)

	if containsMessage(got, "assistant-dropped") {
		t.Fatal("dropped middle assistant message was retained")
	}
	if hasOrphanFunctionOutput(got) {
		t.Fatal("assistant-message compaction created orphan output")
	}
}

func messageItem(text string) responses.ResponseInputItemUnionParam {
	return responses.ResponseInputItemParamOfMessage(text, responses.EasyInputMessageRoleUser)
}

func assistantMessageItem(text string) responses.ResponseInputItemUnionParam {
	return responses.ResponseInputItemParamOfMessage(text, responses.EasyInputMessageRoleAssistant)
}

func functionCallItem(callID string) responses.ResponseInputItemUnionParam {
	return responses.ResponseInputItemParamOfFunctionCall(`{"path":"answer.txt"}`, callID, "read")
}

func functionOutputItem(callID string) responses.ResponseInputItemUnionParam {
	return responses.ResponseInputItemParamOfFunctionCallOutput(callID, "tool output")
}

func hasFunctionOutput(items responses.ResponseInputParam, callID string) bool {
	for _, item := range items {
		if item.OfFunctionCallOutput != nil && item.OfFunctionCallOutput.CallID == callID {
			return true
		}
	}
	return false
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

func containsMessage(items responses.ResponseInputParam, text string) bool {
	for _, item := range items {
		if item.OfMessage != nil && item.OfMessage.Content.OfString.Value == text {
			return true
		}
	}
	return false
}

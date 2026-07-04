package providercore

import "testing"

func TestCollectorAssemblesDuplicateAndEmptyToolIDsInOrder(t *testing.T) {
	c := NewCollector()
	for _, event := range []Event{
		{Type: EventToolCallStart, ToolCallID: "dup", ToolName: "first"},
		{Type: EventToolCallDelta, ToolCallID: "dup", ArgumentsFragment: `{"a":`},
		{Type: EventToolCallStart, ToolCallID: "", ToolName: "second"},
		{Type: EventToolCallDelta, ToolCallID: "", ArgumentsFragment: `{"b":2}`},
		{Type: EventToolCallEnd, ToolCallID: ""},
		{Type: EventToolCallStart, ToolCallID: "dup", ToolName: "ignored"},
		{Type: EventToolCallDelta, ToolCallID: "dup", ArgumentsFragment: `1}`},
		{Type: EventToolCallEnd, ToolCallID: "dup"},
		{Type: EventToolCallStart, ToolCallID: "", ToolName: "third"},
		{Type: EventToolCallDelta, ToolCallID: "", ArgumentsFragment: `{"c":3}`},
		{Type: EventToolCallEnd, ToolCallID: ""},
	} {
		c.Apply(event)
	}
	got := c.Response().ToolCalls
	if len(got) != 3 {
		t.Fatalf("tool calls len=%d, want 3: %#v", len(got), got)
	}
	assertToolCall(t, got[0], "dup", "first", `{"a":1}`)
	assertToolCall(t, got[1], "call_2", "second", `{"b":2}`)
	assertToolCall(t, got[2], "call_3", "third", `{"c":3}`)
}

func TestCollectorAdoptsEmptyDeltaBeforeStart(t *testing.T) {
	c := NewCollector()
	c.Apply(Event{Type: EventToolCallDelta, ArgumentsFragment: `{"path":`})
	c.Apply(Event{Type: EventToolCallStart, ToolName: "read_file"})
	c.Apply(Event{Type: EventToolCallDelta, ArgumentsFragment: `"a.txt"}`})
	c.Apply(Event{Type: EventToolCallEnd})
	got := c.Response().ToolCalls
	if len(got) != 1 {
		t.Fatalf("tool calls len=%d, want 1: %#v", len(got), got)
	}
	assertToolCall(t, got[0], "call_1", "read_file", `{"path":"a.txt"}`)
}

func TestClassifyContextLimit(t *testing.T) {
	err := Classify(400, "prompt is too long for this model")
	if err.Class != ErrorContextLimit {
		t.Fatalf("class=%s, want %s", err.Class, ErrorContextLimit)
	}
}

func assertToolCall(t *testing.T, got ToolCall, id, name, args string) {
	t.Helper()
	if got.ID != id || got.Name != name || got.Arguments != args {
		t.Fatalf("tool call = %#v, want id=%q name=%q args=%q", got, id, name, args)
	}
}

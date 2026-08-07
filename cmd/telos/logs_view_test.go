package main

import (
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/sessionapi"
)

func TestDeriveOverallLogStatus(t *testing.T) {
	verifier := "verifier"
	prover := "prover"
	tests := []struct {
		name   string
		header logHeader
		events []sessionapi.SessionEvent
		want   string
	}{
		{
			name:   "starting follows lifecycle",
			header: logHeader{State: "provisioning"},
			want:   "Starting",
		},
		{
			name:   "failed lifecycle wins",
			header: logHeader{State: "failed", Failure: "runtime unavailable"},
			events: []sessionapi.SessionEvent{{Event: "game_end", Data: map[string]any{"game_result": "success"}}},
			want:   "Failed",
		},
		{
			name:   "verifier acceptance is ready",
			header: logHeader{State: "healthy"},
			events: []sessionapi.SessionEvent{{Event: "agent_complete", Role: &verifier, Data: map[string]any{"status": "CONCEDE"}}},
			want:   "Ready",
		},
		{
			name:   "prover concede is not ready",
			header: logHeader{State: "healthy"},
			events: []sessionapi.SessionEvent{{Event: "agent_complete", Role: &prover, Data: map[string]any{"status": "CONCEDE"}}},
			want:   "Working",
		},
		{
			name:   "routine work does not revoke acceptance",
			header: logHeader{State: "healthy"},
			events: []sessionapi.SessionEvent{
				{Event: "agent_complete", Role: &verifier, Data: map[string]any{"status": "CONCEDE"}},
				{Event: "game_start", Data: map[string]any{}},
				{Event: "agent_progress", Role: &prover, Data: map[string]any{"text": "Checking service health"}},
			},
			want: "Ready",
		},
		{
			name:   "new spec resets acceptance",
			header: logHeader{State: "healthy"},
			events: []sessionapi.SessionEvent{
				{Event: "agent_complete", Role: &verifier, Data: map[string]any{"status": "CONCEDE"}},
				{Event: "external_update", Data: map[string]any{"message": "Spec updated"}},
				{Event: "agent_progress", Role: &prover, Data: map[string]any{"text": "Implementing the new spec"}},
			},
			want: "Working",
		},
		{
			name:   "stopped evaluation needs attention",
			header: logHeader{State: "healthy"},
			events: []sessionapi.SessionEvent{
				{Event: "agent_progress", Role: &prover, Data: map[string]any{"text": "Running tests"}},
				{Event: "game_end", Data: map[string]any{"game_result": "failure", "error": "tests failed"}},
			},
			want: "Needs attention",
		},
		{
			name:   "recoverable provider incident stays working",
			header: logHeader{State: "healthy"},
			events: []sessionapi.SessionEvent{
				{Event: "agent_progress", Role: &prover, Data: map[string]any{"text": "Implementing the API"}},
				{Event: "agent_failure_recoverable", Data: map[string]any{"error": "502 no healthy upstream"}},
			},
			want: "Working",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := deriveOverallLogStatus(test.header, test.events)
			if got.Label != test.want {
				t.Fatalf("status: got %q, want %q (%s)", got.Label, test.want, got.Reason)
			}
		})
	}
}

func TestCollapseRepeatedLogRowsGroupsIncidentWindows(t *testing.T) {
	rows := []renderedLogRow{
		{Timestamp: "2026-07-01T00:00:00Z", Summary: "Model provider unavailable", GroupKey: "retry:502", CountNoun: "retries", Count: 1, Order: 0},
		{Timestamp: "2026-07-01T00:20:00Z", Summary: "Model provider unavailable", GroupKey: "retry:502", CountNoun: "retries", Count: 1, Order: 1},
		{Timestamp: "2026-07-01T01:00:00Z", Summary: "Model provider unavailable", GroupKey: "retry:502", CountNoun: "retries", Count: 1, Order: 2},
		{Timestamp: "2026-07-01T03:00:00Z", Summary: "Model provider unavailable", GroupKey: "retry:502", CountNoun: "retries", Count: 1, Order: 3},
		{Timestamp: "2026-07-01T03:15:00Z", Summary: "Model provider unavailable", GroupKey: "retry:502", CountNoun: "retries", Count: 1, Order: 4},
	}

	got := collapseRepeatedLogRows(rows)
	if len(got) != 2 {
		t.Fatalf("incidents: got %d, want 2: %#v", len(got), got)
	}
	if got[0].Count != 3 || got[1].Count != 2 {
		t.Fatalf("incident counts: got %d and %d", got[0].Count, got[1].Count)
	}
}

func TestPrintStructuredLogsCollapsesRetries(t *testing.T) {
	ts1 := "2026-07-01T00:00:00Z"
	ts2 := "2026-07-01T00:10:00Z"
	ts3 := "2026-07-01T00:20:00Z"
	events := []sessionapi.SessionEvent{
		{Event: "agent_failure_recoverable", Timestamp: &ts1, Data: map[string]any{"error": "502 no healthy upstream"}},
		{Event: "agent_failure_recoverable", Timestamp: &ts2, Data: map[string]any{"error": "502 no healthy upstream"}},
		{Event: "agent_failure_recoverable", Timestamp: &ts3, Data: map[string]any{"error": "502 no healthy upstream"}},
	}

	var output strings.Builder
	printStructuredLogs(&output, logHeader{Name: "breachpoint", State: "healthy", SessionID: "sess_456"}, events, logViewOptions{Tail: defaultLogTail})
	text := output.String()
	if !strings.Contains(text, "Model provider unavailable · 3 retries") {
		t.Fatalf("collapsed retry missing:\n%s", text)
	}
	if count := strings.Count(text, "RETRY"); count != 1 {
		t.Fatalf("retry activity rendered %d times:\n%s", count, text)
	}
}

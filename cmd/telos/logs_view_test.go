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
			name:   "starting failure needs attention",
			header: logHeader{State: "deploying", Failure: "managed session update failed"},
			want:   "Needs attention",
		},
		{
			name:   "deleting is stopping instead of working",
			header: logHeader{State: "deleting"},
			want:   "Stopped",
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
			name:   "failure after acceptance needs attention",
			header: logHeader{State: "healthy"},
			events: []sessionapi.SessionEvent{
				{Event: "agent_complete", Role: &verifier, Data: map[string]any{"status": "CONCEDE"}},
				{Event: "game_end", Data: map[string]any{"game_result": "failure", "error": "tests failed"}},
			},
			want: "Needs attention",
		},
		{
			name:   "work after a failure resumes working",
			header: logHeader{State: "healthy"},
			events: []sessionapi.SessionEvent{
				{Event: "agent_complete", Role: &verifier, Data: map[string]any{"status": "CONCEDE"}},
				{Event: "game_error", Data: map[string]any{"error": "tests failed"}},
				{Event: "agent_progress", Role: &prover, Data: map[string]any{"text": "Fixing the failing tests"}},
			},
			want: "Working",
		},
		{
			name:   "acceptance after a failure is ready",
			header: logHeader{State: "healthy"},
			events: []sessionapi.SessionEvent{
				{Event: "game_error", Data: map[string]any{"error": "tests failed"}},
				{Event: "agent_complete", Role: &verifier, Data: map[string]any{"status": "CONCEDE"}},
			},
			want: "Ready",
		},
		{
			name:   "control plane failure needs attention",
			header: logHeader{State: "healthy"},
			events: []sessionapi.SessionEvent{
				{Event: "deployment.update_failed", Data: map[string]any{"reason": "runtime rejected the update"}},
			},
			want: "Needs attention",
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

func TestRenderedLogRowOnlyTreatsVerifierConcedeAsAcceptance(t *testing.T) {
	verifier := "verifier"
	prover := "prover"

	verifierRow, ok := renderedLogRowFromEvent(
		sessionapi.SessionEvent{Event: "agent_complete", Role: &verifier, Data: map[string]any{"status": "CONCEDE"}},
		false,
	)
	if !ok || verifierRow.Summary != "Current spec accepted" {
		t.Fatalf("verifier completion: got %#v, visible=%v", verifierRow, ok)
	}

	if proverRow, visible := renderedLogRowFromEvent(
		sessionapi.SessionEvent{Event: "agent_complete", Role: &prover, Data: map[string]any{"status": "CONCEDE"}},
		false,
	); visible {
		t.Fatalf("prover completion should stay hidden by default: %#v", proverRow)
	}
}

func TestDeriveOverallLogStatusUsesLatestCompletionReason(t *testing.T) {
	verifier := "verifier"
	status := deriveOverallLogStatus(logHeader{State: "healthy"}, []sessionapi.SessionEvent{
		{Event: "agent_failure_recoverable", Data: map[string]any{"error": "502 no healthy upstream"}},
		{Event: "agent_complete", Role: &verifier, Data: map[string]any{"status": "CONTINUE"}},
	})

	if status.Label != "Working" || status.Reason != "Verification requested another iteration." {
		t.Fatalf("status: got %#v", status)
	}
}

func TestPrintStructuredLogsPreservesRepeatedEvents(t *testing.T) {
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
	if count := strings.Count(text, "[agent] [RETRY]"); count != 3 {
		t.Fatalf("retry activity rendered %d times:\n%s", count, text)
	}
	for _, timestamp := range []string{
		"[2026-07-01T00:00:00Z]",
		"[2026-07-01T00:10:00Z]",
		"[2026-07-01T00:20:00Z]",
	} {
		if !strings.Contains(text, timestamp) {
			t.Fatalf("missing retry timestamp %s:\n%s", timestamp, text)
		}
	}
}

func TestRenderedLogRowTreatsToolActivityAsVerboseTelemetry(t *testing.T) {
	event := sessionapi.SessionEvent{
		Event: "agent_progress",
		Data:  map[string]any{"kind": "tool", "text": "Reading package.json"},
	}
	if row, visible := renderedLogRowFromEvent(event, false); visible {
		t.Fatalf("tool activity should be hidden by default: %#v", row)
	}
	row, visible := renderedLogRowFromEvent(event, true)
	if !visible || row.Phase != "TOOL" || row.Summary != "Reading package.json" {
		t.Fatalf("verbose tool activity: visible=%v row=%#v", visible, row)
	}
}

func TestPrintRenderedLogRowUsesUTCTimestampAndPreservesMultilineDetail(t *testing.T) {
	var output strings.Builder
	printRenderedLogRow(&output, renderedLogRow{
		Timestamp:       "2026-07-01T20:42:18-04:00",
		Source:          "agent",
		Phase:           "verify",
		Summary:         "Review",
		Detail:          "criterion,score\nCorrectness,8/10",
		MultilineDetail: true,
	})

	text := output.String()
	for _, want := range []string{
		"[2026-07-02T00:42:18Z] [agent] [VERIFY] Review",
		"│ criterion,score",
		"│ Correctness,8/10",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("rendered row missing %q:\n%s", want, text)
		}
	}
}

func TestRenderedLogRowPreservesReviewEvent(t *testing.T) {
	verifier := "verifier"
	event := sessionapi.SessionEvent{
		Event: "agent_progress",
		Role:  &verifier,
		Data: map[string]any{
			"kind": "review",
			"text": "criterion,score\nCorrectness,8/10",
		},
	}
	row, visible := renderedLogRowFromEvent(event, false)
	if !visible || row.Summary != "Review" || !row.MultilineDetail || !strings.Contains(row.Detail, "\n") {
		t.Fatalf("review row: visible=%v row=%#v", visible, row)
	}
}

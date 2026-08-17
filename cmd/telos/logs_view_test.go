package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/sessionapi"
)

func TestRenderedLogRowKeepsMaterialProgress(t *testing.T) {
	timestamp := "2026-08-10T12:00:00Z"
	row, visible := renderedLogRowFromEvent(sessionapi.SessionEvent{
		Event:     "agent_progress",
		Timestamp: &timestamp,
		Data: map[string]any{
			"kind": "progress_update",
			"text": "Running integration tests",
		},
	})
	if !visible {
		t.Fatal("material progress should be visible")
	}
	if row.Level != "INFO" || row.Summary != "Running integration tests" {
		t.Fatalf("row = %#v", row)
	}
}

func TestRenderedLogRowHidesToolAndEngineChatter(t *testing.T) {
	role := "verifier"
	for _, event := range []sessionapi.SessionEvent{
		{Event: "agent_progress", Data: map[string]any{"kind": "tool", "text": "Reading main.go"}},
		{Event: "agent_progress", Data: map[string]any{"kind": "progress_update", "text": "Editing main.go"}},
		{Event: "agent_progress", Data: map[string]any{"kind": "review", "text": "The other model requested changes"}},
		{Event: "agent_progress", Data: map[string]any{"kind": "summary", "text": "Internal turn summary"}},
		{Event: "game_start"},
		{Event: "round_start", Role: &role},
		{Event: "workspace_checkpoint"},
		{Event: "runtime.heartbeat", Data: map[string]any{"message": "alive"}},
	} {
		if row, visible := renderedLogRowFromEvent(event); visible {
			t.Fatalf("event %q should be hidden: %#v", event.Event, row)
		}
	}
}

func TestRenderedLogRowOnlyTreatsAcceptedRevisionAsCompletion(t *testing.T) {
	verifier := "verifier"
	prover := "prover"
	accepted, visible := renderedLogRowFromEvent(sessionapi.SessionEvent{
		Event: "agent_complete",
		Role:  &verifier,
		Data:  map[string]any{"status": "CONCEDE"},
	})
	if !visible || accepted.Summary != "Current revision accepted" {
		t.Fatalf("accepted row = %#v visible=%v", accepted, visible)
	}
	if row, visible := renderedLogRowFromEvent(sessionapi.SessionEvent{
		Event: "agent_complete",
		Role:  &prover,
		Data:  map[string]any{"status": "CONCEDE"},
	}); visible {
		t.Fatalf("implementation completion should be hidden: %#v", row)
	}
}

func TestRenderedLogRowUsesStandardSeverityLevels(t *testing.T) {
	retry, visible := renderedLogRowFromEvent(sessionapi.SessionEvent{
		Event: "agent_failure_recoverable",
		Data: map[string]any{
			"error":                "502: no healthy upstream",
			"consecutive_failures": 2,
			"max_failures":         5,
		},
	})
	if !visible || retry.Level != "WARNING" || retry.Summary != "Model provider unavailable; retrying" || retry.Detail != "attempt 2 of 5" {
		t.Fatalf("retry row = %#v visible=%v", retry, visible)
	}

	failure, visible := renderedLogRowFromEvent(sessionapi.SessionEvent{
		Event: "workload.rollout.failed",
		Data:  map[string]any{"message": "Provisioning failed: quota exhausted"},
	})
	if !visible || failure.Level != "ERROR" || failure.Summary != "Provisioning failed" || failure.Detail != "quota exhausted" {
		t.Fatalf("failure row = %#v visible=%v", failure, visible)
	}

	waiting, visible := renderedLogRowFromEvent(sessionapi.SessionEvent{
		Event: "workload.rollout.waiting",
		Data:  map[string]any{"message": "Waiting for capacity"},
	})
	if !visible || waiting.Level != "WARNING" {
		t.Fatalf("waiting row = %#v visible=%v", waiting, visible)
	}
}

func TestPrintStructuredLogsUsesCompactPythonStyleLines(t *testing.T) {
	timestamp := "2026-08-10T12:00:00Z"
	events := []sessionapi.SessionEvent{
		{
			Event:     "agent_progress",
			Timestamp: &timestamp,
			Data:      map[string]any{"kind": "tool", "text": "Reading main.go"},
		},
		{
			Event:     "deployment.accepted",
			Timestamp: &timestamp,
			Data:      map[string]any{"message": "Accepted managed session"},
		},
	}

	var output bytes.Buffer
	printStructuredLogs(&output, events, logViewOptions{Tail: defaultLogTail})
	text := output.String()
	if !strings.Contains(text, "[2026-08-10T12:00:00Z] [INFO] Accepted managed session") {
		t.Fatalf("logs = %q", text)
	}
	for _, forbidden := range []string{"ACTIVITY", "Status", "Summary", "Session", "[agent]", "[BUILD]", "[VERIFY]", "Reading main.go"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("logs should omit %q: %q", forbidden, text)
		}
	}
}

func TestRenderLogRowsCollapsesAdjacentDuplicateMessages(t *testing.T) {
	verifier := "verifier"
	events := []sessionapi.SessionEvent{
		{Event: "agent_complete", Role: &verifier, Data: map[string]any{"status": "CONCEDE"}},
		{Event: "game_end", Data: map[string]any{"game_result": "success"}},
	}
	rows := renderLogRows(events)
	if len(rows) != 1 || rows[0].Summary != "Current revision accepted" {
		t.Fatalf("rows = %#v", rows)
	}
}

func TestPrintRenderedLogRowIndentsMultilineDetails(t *testing.T) {
	var output bytes.Buffer
	printRenderedLogRow(&output, renderedLogRow{
		Timestamp:       "2026-08-10T12:00:00Z",
		Level:           "ERROR",
		Summary:         "Execution suspended",
		Detail:          "authentication failed\nrun telos login",
		MultilineDetail: true,
	})
	text := output.String()
	if !strings.Contains(text, "[2026-08-10T12:00:00Z] [ERROR] Execution suspended\n") ||
		!strings.Contains(text, "│ authentication failed\n") ||
		!strings.Contains(text, "│ run telos login\n") {
		t.Fatalf("multiline log row = %q", text)
	}
}

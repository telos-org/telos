package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/telos-org/telos/internal/sessionapi"
)

func TestHumanProgressIsCompleteAndTechnicalRecordsStayInJSON(t *testing.T) {
	text := "Checking whether the bot can resume safely after a restart. The position limits must still hold, and orders must not be submitted twice after reconnecting."
	events := []sessionapi.SessionEvent{
		{Event: "agent_progress", Data: map[string]any{"audience": "agent", "kind": "progress_update", "text": "All four md5s match; 42/42 units."}},
		{Event: "agent_progress", Data: map[string]any{"audience": "user", "kind": "user_update", "text": text}},
	}
	rows := renderLogRows(events)
	if len(rows) != 1 || rows[0].Summary != text {
		t.Fatalf("rows=%#v", rows)
	}
	var raw bytes.Buffer
	if err := printJSONLogEvents(&raw, events); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw.String(), "42/42") {
		t.Fatal("technical JSON was lost")
	}
}

func TestProgressAudienceIsExplicitAndWorksWithoutOtherHistory(t *testing.T) {
	for _, text := range []string{
		"Reading the strategy to identify contradictory entry conditions.",
		"Updating order handling to prevent duplicate orders after a restart.",
		"Running a paper-trading simulation against the strategy's risk limits.",
		strings.Repeat("Checking recovery and exposure limits. ", 10),
	} {
		for _, audience := range []string{"user", "agent", "future-audience"} {
			event := sessionapi.SessionEvent{Event: "agent_progress", Data: map[string]any{
				"audience": audience, "kind": "user_update", "text": text,
			}}
			rows := renderLogRows([]sessionapi.SessionEvent{event})
			if audience == "user" {
				if len(rows) != 1 || rows[0].Summary != strings.TrimSpace(text) || rows[0].Detail != "" {
					t.Fatalf("human update was filtered or shortened: %#v", rows)
				}
			} else if len(rows) != 0 {
				t.Fatalf("non-user audience leaked: %#v", rows)
			}
		}
	}
	if rows := renderLogRows([]sessionapi.SessionEvent{{Event: "agent_progress", Data: map[string]any{
		"audience": "user", "text": "  ",
	}}}); len(rows) != 0 {
		t.Fatalf("empty human update should stay hidden: %#v", rows)
	}
}

func TestUnmarkedLogsKeepOriginalOutput(t *testing.T) {
	ts := "2026-08-10T12:00:00Z"
	prover, verifier := "prover", "verifier"
	events := []sessionapi.SessionEvent{
		{Event: "round_start", Role: &prover},
		{Event: "agent_progress", Data: map[string]any{"kind": "progress_update", "text": "Reading the strategy carefully."}},
		{Event: "agent_progress", Data: map[string]any{"kind": "progress_update", "text": "Updating order handling to prevent duplicates."}},
		{Event: "agent_progress", Data: map[string]any{"kind": "tool", "text": "Reading main.go"}},
		{Event: "agent_progress", Data: map[string]any{"kind": "review", "text": "Internal review"}},
		{Event: "agent_progress", Data: map[string]any{"kind": "summary", "text": "Internal summary"}},
		{Event: "agent_progress", Data: map[string]any{"kind": "progress_update", "text": "Running integration tests"}},
		{Event: "agent_progress", Data: map[string]any{"text": "Planning complete. Building the app next."}},
		{Event: "agent_complete", Role: &prover, Data: map[string]any{"status": "CONTINUE"}},
		{Event: "round_start", Role: &verifier},
		{Event: "agent_failure_recoverable", Data: map[string]any{"error": "502: no healthy upstream", "consecutive_failures": 1, "max_failures": 3}},
		{Event: "agent_complete", Role: &verifier, Data: map[string]any{"status": "CONCEDE"}},
		{Event: "game_end", Data: map[string]any{"game_result": "success"}},
	}
	for i := range events {
		events[i].Timestamp = &ts
	}
	const expected = "[2026-08-10T12:00:00Z] [INFO] Running integration tests\n" +
		"[2026-08-10T12:00:00Z] [INFO] Planning complete — Building the app next.\n" +
		"[2026-08-10T12:00:00Z] [WARNING] Model provider unavailable; retrying — attempt 1 of 3\n" +
		"[2026-08-10T12:00:00Z] [INFO] Current revision accepted\n"
	var out bytes.Buffer
	printStructuredLogs(&out, events, logViewOptions{All: true, Active: true})
	if out.String() != expected {
		t.Fatalf("legacy output changed:\n%s\nwant:\n%s", out.String(), expected)
	}
	// Active old runs do not acquire the new five-minute notice either.
	out.Reset()
	printStructuredLogs(&out, events[:8], logViewOptions{Tail: 1, Active: true})
	if out.String() != "[2026-08-10T12:00:00Z] [INFO] Planning complete — Building the app next.\n" {
		t.Fatalf("legacy tail changed: %q", out.String())
	}
}

func TestQuietProgressDoesNotInventAWaitReason(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 6, 0, 0, time.UTC)
	ts := "2026-09-16T12:01:00Z"
	events := []sessionapi.SessionEvent{{Event: "agent_progress", Timestamp: &ts, Data: map[string]any{"audience": "user", "kind": "user_update", "text": "Checking restart recovery."}}}
	var out bytes.Buffer
	printLogSilence(&out, events, now.Add(-time.Second))
	if out.Len() != 0 {
		t.Fatalf("quiet notice appeared before five minutes: %q", out.String())
	}
	printLogSilence(&out, events, now)
	if !strings.Contains(out.String(), "No new progress update for 5m0s") || strings.Contains(out.String(), "because") {
		t.Fatalf("silence=%q", out.String())
	}
	out.Reset()
	fresh := now.Format(time.RFC3339)
	technical := sessionapi.SessionEvent{Event: "agent_progress", Timestamp: &fresh, Data: map[string]any{
		"audience": "agent", "kind": "progress_update", "text": "42/42 units.",
	}}
	printLogSilence(&out, append(events, technical), now)
	if !strings.Contains(out.String(), "No new progress update for 5m0s. Last reported activity: Checking restart recovery.") {
		t.Fatalf("technical activity reset the human timer: %q", out.String())
	}
	for _, newer := range []sessionapi.SessionEvent{
		{Event: "agent_progress", Timestamp: &fresh, Data: map[string]any{"text": "Restart recovery passed."}},
		{Event: "game_error", Timestamp: &fresh, Data: map[string]any{"error": "Connection interrupted"}},
	} {
		out.Reset()
		printLogSilence(&out, append(events, newer), now)
		if out.Len() != 0 {
			t.Fatalf("newer visible %s should supersede the old activity: %q", newer.Event, out.String())
		}
	}
	out.Reset()
	printLogSilence(&out, append(events, sessionapi.SessionEvent{Event: "game_end"}), now)
	if out.Len() != 0 {
		t.Fatalf("completed work shown as waiting: %q", out.String())
	}
}

func TestNewRoundSuppliesProgressBeforeFirstHumanUpdate(t *testing.T) {
	role, ts := "prover", "2026-09-16T12:00:00Z"
	event := sessionapi.SessionEvent{Event: "round_start", Role: &role, Timestamp: &ts, Data: map[string]any{"audience": "user"}}
	rows := renderLogRows([]sessionapi.SessionEvent{event})
	if len(rows) != 1 || rows[0].Summary != "Working on the spec requirements" {
		t.Fatalf("phase fallback missing: %#v", rows)
	}
	var out bytes.Buffer
	printLogSilence(&out, []sessionapi.SessionEvent{event}, time.Date(2026, 9, 16, 12, 5, 0, 0, time.UTC))
	if !strings.Contains(out.String(), "No new progress update for 5m0s. Last reported activity: Working on the spec requirements") {
		t.Fatalf("first update wait missing: %q", out.String())
	}
}

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
	for _, event := range []sessionapi.SessionEvent{
		{Event: "agent_progress", Data: map[string]any{"kind": "tool", "text": "Reading main.go"}},
		{Event: "agent_progress", Data: map[string]any{"kind": "progress_update", "text": "Editing main.go"}},
		{Event: "agent_progress", Data: map[string]any{"kind": "review", "text": "The other model requested changes"}},
		{Event: "agent_progress", Data: map[string]any{"kind": "summary", "text": "Internal turn summary"}},
		{Event: "game_start"},
		{Event: "workspace_checkpoint"},
		{Event: "runtime.heartbeat", Data: map[string]any{"message": "alive"}},
	} {
		if row, visible := renderedLogRowFromEvent(event); visible {
			t.Fatalf("event %q should be hidden: %#v", event.Event, row)
		}
	}
}

func TestRenderedLogRowWaitsForRuntimeCycleCompletion(t *testing.T) {
	verifier := "verifier"
	prover := "prover"
	accepted, visible := renderedLogRowFromEvent(sessionapi.SessionEvent{
		Event: "game_end",
		Data:  map[string]any{"audience": "user", "game_result": "success"},
	})
	if !visible || accepted.Summary != "This round of checks passed" {
		t.Fatalf("completed row = %#v visible=%v", accepted, visible)
	}
	if row, visible := renderedLogRowFromEvent(sessionapi.SessionEvent{
		Event: "agent_complete",
		Role:  &verifier,
		Data:  map[string]any{"audience": "agent", "status": "CONCEDE"},
	}); visible {
		t.Fatalf("technical status must wait for runtime completion: %#v", row)
	}
	if row, visible := renderedLogRowFromEvent(sessionapi.SessionEvent{
		Event: "agent_complete",
		Role:  &prover,
		Data:  map[string]any{"audience": "agent", "status": "CONCEDE"},
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

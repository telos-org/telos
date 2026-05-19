package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/telos-org/telos-go/internal/sessionapi"
)

func TestVisibleListSessionsHidesChildSessionsByDefault(t *testing.T) {
	parent := "sess_parent"
	sessions := []sessionapi.Session{
		{SessionID: "sess_controller", Status: sessionapi.StatusRunning},
		{SessionID: "sess_task", ParentSessionID: &parent, Status: sessionapi.StatusCompleted},
		{SessionID: "sess_controller_2", Status: sessionapi.StatusScheduled},
	}

	visible := visibleListSessions(sessions, false)
	if len(visible) != 2 {
		t.Fatalf("visible session count: got %d, want 2", len(visible))
	}
	if visible[0].SessionID != "sess_controller" || visible[1].SessionID != "sess_controller_2" {
		t.Fatalf("visible sessions: got %#v", visible)
	}
}

func TestVisibleListSessionsWideKeepsChildSessions(t *testing.T) {
	parent := "sess_parent"
	sessions := []sessionapi.Session{
		{SessionID: "sess_controller", Status: sessionapi.StatusRunning},
		{SessionID: "sess_task", ParentSessionID: &parent, Status: sessionapi.StatusCompleted},
	}

	visible := visibleListSessions(sessions, true)
	if len(visible) != len(sessions) {
		t.Fatalf("wide session count: got %d, want %d", len(visible), len(sessions))
	}
	if visible[1].SessionID != "sess_task" {
		t.Fatalf("wide sessions should preserve child rows: got %#v", visible)
	}
}

func TestSessionResultPrefersSessionResultThenLatestEpoch(t *testing.T) {
	completed := "completed"
	if got := sessionResult(sessionapi.Session{Result: &completed}); got != "completed" {
		t.Fatalf("session result: got %q", got)
	}

	got := sessionResult(sessionapi.Session{
		Status: sessionapi.StatusRunning,
		Epochs: []map[string]any{{"result": "failed"}},
	})
	if got != "failed" {
		t.Fatalf("epoch result: got %q", got)
	}

	if got := sessionResult(sessionapi.Session{Status: sessionapi.StatusRunning}); got != "active" {
		t.Fatalf("active result: got %q", got)
	}
}

func TestPrintSessionDescriptionIncludesAgentFacingArtifacts(t *testing.T) {
	name := "postgres"
	kind := sessionapi.KindController
	result := "completed"
	artifact := "https://postgres.example"
	version := 2
	workspaceExists := true
	evidenceExists := true
	transcriptExists := true
	workspacePath := "/state/workspace.tar.gz"
	evidencePath := "/state/evidence.jsonl"
	transcriptPath := "/state/transcript.md"

	session := sessionapi.Session{
		SessionID:          "sess_123",
		SessionKind:        &kind,
		SpecName:           &name,
		Status:             sessionapi.StatusRunning,
		Runtime:            sessionapi.RuntimeCloud,
		Result:             &result,
		ArtifactURI:        &artifact,
		CurrentSpecVersion: &version,
		Epochs: []map[string]any{{
			"id":          1,
			"result":      "completed",
			"started_at":  "2026-05-19T00:00:00Z",
			"finished_at": "2026-05-19T00:03:00Z",
		}},
		Specs: []sessionapi.SessionSpec{{
			Name:             &name,
			WorkspaceExists:  &workspaceExists,
			WorkspacePath:    &workspacePath,
			EvidenceExists:   &evidenceExists,
			EvidencePath:     &evidencePath,
			TranscriptExists: &transcriptExists,
			TranscriptPath:   &transcriptPath,
		}},
	}

	var out bytes.Buffer
	printSessionDescription(&out, session)
	text := out.String()
	for _, want := range []string{
		"Name:     postgres",
		"Result:   completed",
		"Artifact: https://postgres.example",
		"Spec Ver: 2",
		"Latest Epoch:",
		"Artifacts:",
		"yes:/state/workspace.tar.gz",
		"yes:/state/evidence.jsonl",
		"yes:/state/transcript.md",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("description missing %q:\n%s", want, text)
		}
	}
}

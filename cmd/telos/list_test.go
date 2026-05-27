package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/cli"
	"github.com/telos-org/telos/internal/sessionapi"
)

func TestVisibleListSessionsHidesChildSessionsByDefault(t *testing.T) {
	parent := "sess_parent"
	sessions := []sessionapi.Session{
		{SessionID: "sess_controller", Status: sessionapi.StatusRunning},
		{SessionID: "sess_task", ParentSessionID: &parent, Status: sessionapi.StatusCompleted},
		{SessionID: "sess_controller_2", Status: sessionapi.StatusScheduled},
		{SessionID: "sess_old", Status: sessionapi.StatusStopped},
		{SessionID: "sess_failed", Status: sessionapi.StatusFailed},
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

func TestSessionTurnShowsActiveRoleAndRound(t *testing.T) {
	round := 3
	role := "verifier"
	got := sessionTurn(sessionapi.Session{CurrentRound: &round, CurrentRole: &role})
	if got != "verifier#3" {
		t.Fatalf("session turn: got %q", got)
	}
	if got := sessionTurn(sessionapi.Session{}); got != "-" {
		t.Fatalf("empty session turn: got %q", got)
	}
}

func TestControllerListSessionsUsesScopedContext(t *testing.T) {
	var gotAuth string
	var gotPath string
	cluster := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.RequestURI()
		if r.URL.Path != "/api/sessions" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(sessionapi.SessionListResponse{
			Sessions: []sessionapi.Session{{
				SessionID: "sess_controller",
				Status:    sessionapi.StatusRunning,
				Runtime:   sessionapi.RuntimeCloud,
			}},
		})
	}))
	defer cluster.Close()

	t.Setenv("TELOS_SESSION_DIR", filepath.Join(t.TempDir(), "sessions"))
	t.Setenv("TELOS_API_TOKEN", "scoped-token")
	t.Setenv("TELOS_SESSION_ID", "sess_parent")
	t.Setenv("TELOS_CLUSTER_API_ENDPOINT", cluster.URL)

	sessions, handled, err := controllerListSessions(7)
	if err != nil {
		t.Fatalf("controllerListSessions: %v", err)
	}
	if !handled {
		t.Fatal("expected controller context to be handled")
	}
	if gotAuth != "Bearer scoped-token" {
		t.Fatalf("authorization header: got %q", gotAuth)
	}
	if gotPath != "/api/sessions?limit=7" {
		t.Fatalf("request path: got %q", gotPath)
	}
	if len(sessions) != 1 || sessions[0].SessionID != "sess_controller" {
		t.Fatalf("sessions: got %#v", sessions)
	}
}

func TestControllerListSessionsScopesLocalControllerTree(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeLocal)
	controllerKind := sessionapi.KindController
	controllerSpec := "---\nversion: v0\nname: controller\nplatform: local\n---\n# Controller\n"
	controller, err := store.Create(sessionapi.SessionCreateRequest{
		SpecMarkdown: &controllerSpec,
		SessionKind:  &controllerKind,
	})
	if err != nil {
		t.Fatalf("Create controller: %v", err)
	}
	childSpec := "---\nversion: v0\nname: child\nplatform: local\n---\n# Child\n"
	child, err := store.Create(sessionapi.SessionCreateRequest{
		SpecMarkdown:    &childSpec,
		ParentSessionID: &controller.SessionID,
	})
	if err != nil {
		t.Fatalf("Create child: %v", err)
	}
	grandchildSpec := "---\nversion: v0\nname: grandchild\nplatform: local\n---\n# Grandchild\n"
	grandchild, err := store.Create(sessionapi.SessionCreateRequest{
		SpecMarkdown:    &grandchildSpec,
		ParentSessionID: &child.SessionID,
	})
	if err != nil {
		t.Fatalf("Create grandchild: %v", err)
	}
	siblingSpec := "---\nversion: v0\nname: sibling\nplatform: local\n---\n# Sibling\n"
	if _, err := store.Create(sessionapi.SessionCreateRequest{
		SpecMarkdown: &siblingSpec,
		SessionKind:  &controllerKind,
	}); err != nil {
		t.Fatalf("Create sibling: %v", err)
	}

	t.Setenv("TELOS_SESSION_DIR", root)
	t.Setenv("TELOS_RUNTIME", string(sessionapi.RuntimeLocal))
	t.Setenv("TELOS_SESSION_ID", controller.SessionID)

	sessions, handled, err := controllerListSessions(0)
	if err != nil {
		t.Fatalf("controllerListSessions: %v", err)
	}
	if !handled {
		t.Fatal("expected local controller context to be handled")
	}
	got := make([]string, 0, len(sessions))
	for _, session := range sessions {
		got = append(got, session.SessionID)
	}
	want := []string{controller.SessionID, child.SessionID, grandchild.SessionID}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("scoped sessions: got %v want %v", got, want)
	}
}

func TestPrintSessionDescriptionIncludesAgentFacingArtifacts(t *testing.T) {
	name := "postgres"
	kind := sessionapi.KindController
	result := "completed"
	completionReason := "verifier_conceded"
	verifierConceded := true
	parent := "sess_parent"
	artifact := "https://postgres.example"
	version := 2
	cost := 1.23
	rounds := 4
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
		ParentSessionID:    &parent,
		Status:             sessionapi.StatusCompleted,
		Runtime:            sessionapi.RuntimeCloud,
		Result:             &result,
		CompletionReason:   &completionReason,
		VerifierConceded:   &verifierConceded,
		ArtifactURI:        &artifact,
		CurrentSpecVersion: &version,
		TotalCostUSD:       &cost,
		RoundCount:         &rounds,
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
		"Parent:   sess_parent",
		"Result:   completed",
		"Complete: verifier_conceded",
		"Evaluate: accepted",
		"Artifact: https://postgres.example",
		"Spec Ver: 2",
		"Cost:     $1.2300",
		"Rounds:   4",
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

func TestEvaluationDispositionIsPendingForActiveReview(t *testing.T) {
	verifierConceded := false
	round := 1
	role := "prover"
	session := sessionapi.Session{
		SessionID:        "sess_active",
		Status:           sessionapi.StatusRunning,
		Runtime:          sessionapi.RuntimeLocal,
		VerifierConceded: &verifierConceded,
		CurrentRound:     &round,
		CurrentRole:      &role,
	}

	var out bytes.Buffer
	printSessionDescription(&out, session)
	text := out.String()
	if !strings.Contains(text, "Evaluate: pending") {
		t.Fatalf("description should show pending evaluation while active:\n%s", text)
	}
	if !strings.Contains(text, "Turn:     prover#1") {
		t.Fatalf("description should show active turn:\n%s", text)
	}
}

func TestPrintSessionDescriptionDistinguishesReviewCycles(t *testing.T) {
	completionReason := "review_cycles_complete"
	verifierConceded := false
	session := sessionapi.Session{
		SessionID:        "sess_review",
		Status:           sessionapi.StatusCompleted,
		Runtime:          sessionapi.RuntimeLocal,
		CompletionReason: &completionReason,
		VerifierConceded: &verifierConceded,
	}

	var out bytes.Buffer
	printSessionDescription(&out, session)
	text := out.String()
	for _, want := range []string{
		"Complete: review_cycles_complete",
		"Evaluate: review cycles complete (acceptance not used)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("description missing %q:\n%s", want, text)
		}
	}
}

func TestPrintLocalLaunchIncludesWorkspaceScopedCommands(t *testing.T) {
	session := &cli.LocalSession{
		SessionID:      "local_123",
		SpecName:       "blackbox",
		WorkspaceScope: "/tmp/telos-blackbox",
	}

	var out bytes.Buffer
	printLocalLaunch(&out, "submitted", session)
	text := out.String()
	for _, want := range []string{
		"submitted local_123 (blackbox)",
		"workspace /tmp/telos-blackbox",
		"describe  cd '/tmp/telos-blackbox' && telos describe local_123",
		"logs      cd '/tmp/telos-blackbox' && telos logs local_123",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("launch output missing %q:\n%s", want, text)
		}
	}
}

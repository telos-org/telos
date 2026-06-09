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
		{SessionID: "sess_controller_2", Status: sessionapi.StatusRunning},
		{SessionID: "sess_old", Status: sessionapi.StatusStopped},
		{SessionID: "sess_failed", Status: sessionapi.StatusFailed},
	}

	visible := visibleListSessions(sessions, false)
	if len(visible) != 4 {
		t.Fatalf("visible session count: got %d, want 4", len(visible))
	}
	if visible[0].SessionID != "sess_controller" ||
		visible[1].SessionID != "sess_controller_2" ||
		visible[2].SessionID != "sess_old" ||
		visible[3].SessionID != "sess_failed" {
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

func TestLimitListSessionsAppliesAfterDefaultVisibility(t *testing.T) {
	parent := "sess_parent"
	sessions := []sessionapi.Session{
		{SessionID: "sess_child", ParentSessionID: &parent, Status: sessionapi.StatusRunning},
		{SessionID: "sess_a", Status: sessionapi.StatusRunning},
		{SessionID: "sess_b", Status: sessionapi.StatusRunning},
	}

	visible := limitListSessions(visibleListSessions(sessions, false), 2)
	if len(visible) != 2 {
		t.Fatalf("visible limited sessions: got %d, want 2", len(visible))
	}
	if visible[0].SessionID != "sess_a" || visible[1].SessionID != "sess_b" {
		t.Fatalf("limit should apply after child filtering, got %#v", visible)
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

func TestSessionDisplayStatusDerivesHumanState(t *testing.T) {
	controller := sessionapi.KindController
	task := sessionapi.KindTask
	completed := "completed"
	round := 1
	role := "prover"

	tests := []struct {
		name string
		sess sessionapi.Session
		want string
	}{
		{
			name: "active running task",
			sess: sessionapi.Session{Status: sessionapi.StatusRunning, SessionKind: &task},
			want: "active",
		},
		{
			name: "retained cloud controller",
			sess: sessionapi.Session{
				Status:      sessionapi.StatusRunning,
				Runtime:     sessionapi.RuntimeCloud,
				SessionKind: &controller,
				Result:      &completed,
			},
			want: "idle",
		},
		{
			name: "active turn wins",
			sess: sessionapi.Session{
				Status:       sessionapi.StatusRunning,
				Runtime:      sessionapi.RuntimeCloud,
				SessionKind:  &controller,
				CurrentRound: &round,
				CurrentRole:  &role,
			},
			want: "active",
		},
		{
			name: "completed",
			sess: sessionapi.Session{Status: sessionapi.StatusCompleted},
			want: "completed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sessionDisplayStatus(tt.sess); got != tt.want {
				t.Fatalf("display status: got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestControllerListSessionsUsesScopedContext(t *testing.T) {
	t.Setenv("TELOS_RUNTIME", "")
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
	interval := 14400
	workspaceExists := true
	evidenceExists := true
	transcriptExists := true
	activeWorkspaceExists := true
	activeWorkspacePath := "/state/workspace"
	workspacePath := "/state/workspace.tar.gz"
	evidencePath := "/state/evidence.jsonl"
	transcriptPath := "/state/transcript.md"

	session := sessionapi.Session{
		SessionID:             "sess_123",
		SessionKind:           &kind,
		SpecName:              &name,
		ParentSessionID:       &parent,
		Status:                sessionapi.StatusCompleted,
		Runtime:               sessionapi.RuntimeCloud,
		Result:                &result,
		CompletionReason:      &completionReason,
		VerifierConceded:      &verifierConceded,
		ArtifactURI:           &artifact,
		CurrentSpecVersion:    &version,
		ActiveWorkspacePath:   &activeWorkspacePath,
		ActiveWorkspaceExists: &activeWorkspaceExists,
		TotalCostUSD:          &cost,
		RoundCount:            &rounds,
		Epochs: []map[string]any{{
			"id":          1,
			"result":      "completed",
			"started_at":  "2026-05-19T00:00:00Z",
			"finished_at": "2026-05-19T00:03:00Z",
		}},
		Specs: []sessionapi.SessionSpec{{
			IntervalSeconds:  &interval,
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
		"Name      postgres",
		"Platform  cloud",
		"Status    completed",
		"Cost      $1.2300",
		"Session   sess_123",
		"Lifecycle",
		"result         completed",
		"kind           controller",
		"parent         sess_parent",
		"interval       4h",
		"completion     verifier_conceded",
		"evaluation     accepted",
		"spec version   2",
		"rounds         4",
		"Artifact",
		"https://postgres.example",
		"Latest Epoch",
		"RESULT     STARTED",
		"Paths",
		"active workspace file:///state/workspace",
		"postgres workspace file:///state/workspace.tar.gz",
		"postgres evidence file:///state/evidence.jsonl",
		"postgres transcript file:///state/transcript.md",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("description missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "next run") {
		t.Fatalf("description should not show next run without persisted scheduler state:\n%s", text)
	}
}

func TestPrintSessionDescriptionFormatsIntervals(t *testing.T) {
	for _, tc := range []struct {
		name string
		secs int
		want string
	}{
		{name: "seconds", secs: 45, want: "interval       45s"},
		{name: "minutes", secs: 300, want: "interval       5m"},
		{name: "hours", secs: 7200, want: "interval       2h"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := sessionapi.Session{
				SessionID: "sess_interval",
				Status:    sessionapi.StatusRunning,
				Runtime:   sessionapi.RuntimeCloud,
				Specs: []sessionapi.SessionSpec{{
					IntervalSeconds: &tc.secs,
				}},
			}

			var out bytes.Buffer
			printSessionDescription(&out, session)
			text := out.String()
			if !strings.Contains(text, tc.want) {
				t.Fatalf("description missing %q:\n%s", tc.want, text)
			}
		})
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
	if !strings.Contains(text, "evaluation     pending") {
		t.Fatalf("description should show pending evaluation while active:\n%s", text)
	}
	if !strings.Contains(text, "current turn   prover#1") {
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
		"completion     review_cycles_complete",
		"evaluation     review cycles complete (acceptance not used)",
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
		"submitted blackbox",
		"Name      blackbox",
		"Platform  local",
		"Status    active",
		"Cost      -",
		"Session   local_123",
		"Workspace /tmp/telos-blackbox",
		"Describe  cd '/tmp/telos-blackbox' && telos describe local_123",
		"Logs      cd '/tmp/telos-blackbox' && telos logs local_123",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("launch output missing %q:\n%s", want, text)
		}
	}
}

func TestPrintSessionReceiptUsesNormalizedSummary(t *testing.T) {
	name := "gitea"
	kind := sessionapi.KindController
	completed := "completed"
	cost := 1.1907
	session := &sessionapi.Session{
		SessionID:    "sess_123",
		SpecName:     &name,
		SessionKind:  &kind,
		Runtime:      sessionapi.RuntimeCloud,
		Status:       sessionapi.StatusRunning,
		Result:       &completed,
		TotalCostUSD: &cost,
	}
	env := &environmentJSON{ID: "env_123", Handle: "env-123.usetelos.ai"}

	var out bytes.Buffer
	printSessionReceipt(&out, "updated", session, env)
	text := out.String()
	for _, want := range []string{
		"updated gitea",
		"Name      gitea",
		"Platform  cloud",
		"Status    idle",
		"Cost      $1.1907",
		"Session   sess_123",
		"Environment env_123",
		"Handle    env-123.usetelos.ai",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("receipt missing %q:\n%s", want, text)
		}
	}
}

func TestPrintStopReceiptUsesSessionSummary(t *testing.T) {
	name := "gitea"
	cost := 1.1907
	session := sessionapi.Session{
		SessionID:    "sess_123",
		SpecName:     &name,
		Runtime:      sessionapi.RuntimeCloud,
		Status:       sessionapi.StatusStopped,
		TotalCostUSD: &cost,
	}

	var out bytes.Buffer
	printStopReceipt(&out, session)
	text := out.String()
	for _, want := range []string{
		"stopped gitea",
		"Name      gitea",
		"Platform  cloud",
		"Status    stopped",
		"Cost      $1.1907",
		"Session   sess_123",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("stop receipt missing %q:\n%s", want, text)
		}
	}
}

func TestPrintStopReceiptUsesSessionIDForUnnamedSession(t *testing.T) {
	session := sessionapi.Session{
		SessionID: "sess_123",
		Runtime:   sessionapi.RuntimeLocal,
		Status:    sessionapi.StatusStopped,
	}

	var out bytes.Buffer
	printStopReceipt(&out, session)
	text := out.String()
	for _, want := range []string{
		"stopped sess_123",
		"Name      -",
		"Platform  local",
		"Status    stopped",
		"Session   sess_123",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("stop receipt missing %q:\n%s", want, text)
		}
	}
}

func TestPrintApplyResultsGroupsMultipleOperations(t *testing.T) {
	name := "gitea"
	cost := 1.23
	session := &sessionapi.Session{
		SessionID:    "sess_123",
		SpecName:     &name,
		Runtime:      sessionapi.RuntimeCloud,
		Status:       sessionapi.StatusRunning,
		TotalCostUSD: &cost,
	}
	results := []applyCloudResult{
		{
			Operation:   "updated",
			Session:     session,
			Environment: &environmentJSON{ID: "env_123"},
		},
		{
			Operation: "created",
			Session:   &sessionapi.Session{SessionID: "sess_456", SpecName: &name, Runtime: sessionapi.RuntimeCloud, Status: sessionapi.StatusPending},
		},
	}

	var out bytes.Buffer
	printApplyResults(&out, results)
	text := out.String()
	for _, want := range []string{
		"updated\n\n",
		"ENV      NAME   PLATFORM  STATUS",
		"env_123  gitea  cloud     active  $1.23  sess_123",
		"created\n\n",
		"-    gitea  cloud     active  -     sess_456",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("apply results missing %q:\n%s", want, text)
		}
	}
}

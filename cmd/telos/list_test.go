package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/cli"
	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/sessionapi"
)

func TestVisibleListSessionsHidesChildSessionsByDefault(t *testing.T) {
	parent := "sess_parent"
	sessions := []sessionapi.Session{
		{SessionID: "sess_root", Status: sessionapi.StatusRunning},
		{SessionID: "sess_task", ParentSessionID: &parent, Status: sessionapi.StatusCompleted},
		{SessionID: "sess_root_2", Status: sessionapi.StatusRunning},
		{SessionID: "sess_old", Status: sessionapi.StatusStopped},
		{SessionID: "sess_failed", Status: sessionapi.StatusFailed},
	}

	visible := visibleListSessions(sessions, false)
	if len(visible) != 4 {
		t.Fatalf("visible session count: got %d, want 4", len(visible))
	}
	if visible[0].SessionID != "sess_root" ||
		visible[1].SessionID != "sess_root_2" ||
		visible[2].SessionID != "sess_old" ||
		visible[3].SessionID != "sess_failed" {
		t.Fatalf("visible sessions: got %#v", visible)
	}
}

func TestVisibleListSessionsWideKeepsChildSessions(t *testing.T) {
	parent := "sess_parent"
	sessions := []sessionapi.Session{
		{SessionID: "sess_root", Status: sessionapi.StatusRunning},
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

func TestCmdListShowsDeploymentsForConfiguredCloud(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/deployments" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"deployments": []map[string]any{{
				"id":             "dep_123",
				"name":           "auth",
				"state":          "healthy",
				"package_ref":    "@telos/auth:1.0.0",
				"package_digest": "sha256:abc",
				"service_url":    "https://auth.example.com",
				"dashboard_url":  "https://dashboard.example.com",
				"created_at":     "then",
				"updated_at":     "now",
			}},
		})
	}))
	defer srv.Close()
	configureCloudTest(t, srv.URL)

	out := captureStdout(t, func() {
		cmdList([]string{"--wide"})
	})
	for _, want := range []string{
		"NAME",
		"PACKAGE",
		"auth",
		"healthy",
		"@telos/auth:1.0.0",
		"dep_123",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("list output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "SESSION") {
		t.Fatalf("list output should be deployment-shaped:\n%s", out)
	}
}

func TestPrintDeploymentDescriptionShowsProductSurfaces(t *testing.T) {
	runtime := "0.1.0"
	serviceURL := "https://auth.example.com"
	dashboardURL := "https://dashboard.example.com"
	deployment := cloud.DeploymentRecord{
		ID:             "dep_123",
		Name:           "auth",
		State:          "healthy",
		PackageRef:     "@telos/auth:1.0.0",
		PackageDigest:  "sha256:abc",
		RuntimeVersion: &runtime,
		ServiceURL:     &serviceURL,
		DashboardURL:   &dashboardURL,
		CreatedAt:      "then",
		UpdatedAt:      "now",
	}

	var out bytes.Buffer
	printDeploymentDescription(&out, deployment)
	text := out.String()
	for _, want := range []string{
		"Name      auth",
		"Platform  cloud",
		"Status    healthy",
		"Package   @telos/auth:1.0.0",
		"Deployment dep_123",
		"Service   https://auth.example.com",
		"Dashboard https://dashboard.example.com",
		"Runtime   0.1.0",
		"Lifecycle",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("deployment description missing %q:\n%s", want, text)
		}
	}
}

func TestPrintDeploymentDeleteReceiptUsesDeploymentSummary(t *testing.T) {
	deployment := cloud.DeploymentRecord{
		ID:            "dep_123",
		Name:          "auth",
		State:         "deleted",
		PackageRef:    "@telos/auth:1.0.0",
		PackageDigest: "sha256:abc",
		CreatedAt:     "then",
		UpdatedAt:     "now",
	}

	var out bytes.Buffer
	printDeploymentDeleteReceipt(&out, deployment)
	text := out.String()
	for _, want := range []string{
		"deleted auth",
		"Name      auth",
		"Platform  cloud",
		"Status    deleted",
		"Package   @telos/auth:1.0.0",
		"Deployment dep_123",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("deployment stop receipt missing %q:\n%s", want, text)
		}
	}
}

func TestCmdDeleteDeletesDeployment(t *testing.T) {
	var deleted bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/deployments/dep_123" {
			http.NotFound(w, r)
			return
		}
		deleted = true
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":             "dep_123",
			"name":           "auth",
			"state":          "deleted",
			"package_ref":    "@telos/auth:1.0.0",
			"package_digest": "sha256:abc",
			"created_at":     "then",
			"updated_at":     "now",
		})
	}))
	defer srv.Close()
	configureCloudTest(t, srv.URL)

	out := captureStdout(t, func() {
		cmdDelete([]string{"dep_123"})
	})
	if !deleted {
		t.Fatal("expected deployment delete request")
	}
	for _, want := range []string{
		"deleted auth",
		"Status    deleted",
		"Deployment dep_123",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("delete output missing %q:\n%s", want, out)
		}
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

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = write
	t.Cleanup(func() {
		os.Stdout = old
	})
	fn()
	if err := write.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(read)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
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
			name: "active running child session",
			sess: sessionapi.Session{Status: sessionapi.StatusRunning, SessionKind: &task},
			want: "active",
		},
		{
			name: "retained cloud root",
			sess: sessionapi.Session{
				Status:  sessionapi.StatusRunning,
				Runtime: sessionapi.RuntimeCloud,
				Result:  &completed,
			},
			want: "idle",
		},
		{
			name: "active turn wins",
			sess: sessionapi.Session{
				Status:       sessionapi.StatusRunning,
				Runtime:      sessionapi.RuntimeCloud,
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

func TestRootListSessionsUsesScopedContext(t *testing.T) {
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
			Sessions: []sessionapi.SessionListItem{{
				SessionID: "sess_root",
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

	sessions, handled, err := rootListSessions(7)
	if err != nil {
		t.Fatalf("rootListSessions: %v", err)
	}
	if !handled {
		t.Fatal("expected root context to be handled")
	}
	if gotAuth != "Bearer scoped-token" {
		t.Fatalf("authorization header: got %q", gotAuth)
	}
	if gotPath != "/api/sessions?limit=7&include_children=true" {
		t.Fatalf("request path: got %q", gotPath)
	}
	if len(sessions) != 1 || sessions[0].SessionID != "sess_root" {
		t.Fatalf("sessions: got %#v", sessions)
	}
}

func TestRootListSessionsScopesLocalRootTree(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeLocal)
	rootKind := sessionapi.KindController
	rootSpec := "---\nversion: v0\nname: root\nplatform: local\n---\n# Root\n"
	rootSession, err := store.Create(sessionapi.SessionCreateRequest{
		SpecMarkdown: &rootSpec,
		SessionKind:  &rootKind,
	})
	if err != nil {
		t.Fatalf("Create root: %v", err)
	}
	childSpec := "---\nversion: v0\nname: child\nplatform: local\n---\n# Child\n"
	child, err := store.Create(sessionapi.SessionCreateRequest{
		SpecMarkdown:    &childSpec,
		ParentSessionID: &rootSession.SessionID,
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
		SessionKind:  &rootKind,
	}); err != nil {
		t.Fatalf("Create sibling: %v", err)
	}

	t.Setenv("TELOS_SESSION_DIR", root)
	t.Setenv("TELOS_RUNTIME", string(sessionapi.RuntimeLocal))
	t.Setenv("TELOS_SESSION_ID", rootSession.SessionID)

	sessions, handled, err := rootListSessions(0)
	if err != nil {
		t.Fatalf("rootListSessions: %v", err)
	}
	if !handled {
		t.Fatal("expected local root context to be handled")
	}
	got := make([]string, 0, len(sessions))
	for _, session := range sessions {
		got = append(got, session.SessionID)
	}
	want := []string{rootSession.SessionID, child.SessionID, grandchild.SessionID}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("scoped sessions: got %v want %v", got, want)
	}
}

func TestPrintSessionDescriptionIncludesAgentFacingDetails(t *testing.T) {
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
		ServiceURL:            &artifact,
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
		"lineage        child",
		"parent         sess_parent",
		"interval       4h",
		"completion     verifier_conceded",
		"evaluation     accepted",
		"spec version   2",
		"rounds         4",
		"Service",
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
	var out bytes.Buffer
	printSessionReceipt(&out, "updated", session)
	text := out.String()
	for _, want := range []string{
		"updated gitea",
		"Name      gitea",
		"Platform  cloud",
		"Status    idle",
		"Cost      $1.1907",
		"Session   sess_123",
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

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

func TestCmdListShowsCloudSessionsForConfiguredCloud(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/deployments" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"deployments": []map[string]any{{
				"id":             "sess_123",
				"name":           "auth",
				"state":          "healthy",
				"status":         "ready",
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
		cmdList(nil)
	})
	for _, want := range []string{
		"NAME",
		"STATUS",
		"SESSION",
		"auth",
		"ready",
		"sess_123",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("list output missing %q:\n%s", want, out)
		}
	}
	for _, notWant := range []string{"TARGET", "REVISION", "SERVICE", "PACKAGE", "DASHBOARD"} {
		if strings.Contains(out, notWant) {
			t.Fatalf("default list output should omit %q:\n%s", notWant, out)
		}
	}

	wideOut := captureStdout(t, func() {
		cmdList([]string{"--wide"})
	})
	for _, want := range []string{
		"NAME",
		"STATUS",
		"REVISION",
		"SERVICE",
		"SESSION",
		"auth",
		"ready",
		"sha256:abc",
		"https://auth.example.com",
		"sess_123",
	} {
		if !strings.Contains(wideOut, want) {
			t.Fatalf("wide list output missing %q:\n%s", want, wideOut)
		}
	}
	for _, notWant := range []string{"TARGET", "PACKAGE", "DASHBOARD", "@telos/auth:1.0.0"} {
		if strings.Contains(wideOut, notWant) {
			t.Fatalf("wide list output should omit %q:\n%s", notWant, wideOut)
		}
	}
}

func TestCmdListJSONShowsCloudSessions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/deployments" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"deployments": []map[string]any{{
				"id":             "sess_123",
				"name":           "auth",
				"state":          "healthy",
				"status":         "ready",
				"status_reason":  "The agent finished and the verifier accepted the result.",
				"package_ref":    "@telos/auth:1.0.0",
				"package_digest": "sha256:abc",
				"created_at":     "then",
				"updated_at":     "now",
			}},
		})
	}))
	defer srv.Close()
	configureCloudTest(t, srv.URL)

	out := captureStdout(t, func() {
		cmdList([]string{"--json"})
	})
	var body map[string]any
	if err := json.Unmarshal([]byte(out), &body); err != nil {
		t.Fatalf("list json: %v\n%s", err, out)
	}
	if _, ok := body["deployments"]; ok {
		t.Fatalf("cloud list json should not expose sessions: %#v", body)
	}
	if body["context"] != "personal" {
		t.Fatalf("cloud list json context: %#v", body["context"])
	}
	sessions, ok := body["sessions"].([]any)
	if !ok || len(sessions) != 1 {
		t.Fatalf("cloud list json sessions: %#v", body)
	}
	session, ok := sessions[0].(map[string]any)
	if !ok || session["id"] != "sess_123" || session["status"] != "ready" ||
		session["status_reason"] != "The agent finished and the verifier accepted the result." {
		t.Fatalf("cloud list json first session: %#v", sessions[0])
	}
}

func TestCmdListContextFlagOverridesEnvironment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/account/bootstrap":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"personal_org_id": "org_personal",
				"organizations": []map[string]any{
					{"id": "org_environment", "handle": "environment"},
					{"id": "org_flag", "handle": "flag"},
				},
			})
		case "/api/deployments":
			if got := r.Header.Get("X-Telos-Org-Id"); got != "org_flag" {
				t.Fatalf("organization header = %q, want org_flag", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"deployments": []any{}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	configureCloudTest(t, srv.URL)
	t.Setenv("TELOS_CONTEXT", "@environment")

	out := captureStdout(t, func() {
		cmdList([]string{"--context", "@flag", "--json"})
	})
	var body map[string]any
	if err := json.Unmarshal([]byte(out), &body); err != nil {
		t.Fatal(err)
	}
	if body["context"] != "@flag" {
		t.Fatalf("context = %#v, want @flag", body["context"])
	}
}

func TestPrintCloudSessionDescriptionShowsProductSurfaces(t *testing.T) {
	serviceURL := "https://auth.example.com"
	dashboardURL := "https://dashboard.example.com"
	session := cloud.SessionRecord{
		ID:            "sess_123",
		Name:          "auth",
		State:         "healthy",
		Status:        "ready",
		StatusReason:  "The agent finished and the verifier accepted the result.",
		PackageRef:    "@telos/auth:1.0.0",
		PackageDigest: "sha256:abc",
		ServiceURL:    &serviceURL,
		DashboardURL:  &dashboardURL,
		CreatedAt:     "then",
		UpdatedAt:     "now",
	}

	var out bytes.Buffer
	printCloudSessionDescription(&out, session)
	text := out.String()
	for _, want := range []string{
		"Name      auth",
		"Status    ready",
		"Session   sess_123",
		"Revision  sha256:abc",
		"Service   https://auth.example.com",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("cloud session description missing %q:\n%s", want, text)
		}
	}
	for _, notWant := range []string{
		"Target", "Package", "Dashboard", "Runtime", "Lifecycle", "status reason",
	} {
		if strings.Contains(text, notWant) {
			t.Fatalf("cloud session description should omit %q:\n%s", notWant, text)
		}
	}
}

func TestPrintCloudSessionReceiptShowsNextUsefulAction(t *testing.T) {
	serviceURL := "https://auth.example.com"
	dashboardURL := "https://dashboard.example.com"
	session := &cloud.SessionRecord{
		ID:            "sess_123",
		Name:          "auth",
		State:         "deploying",
		Status:        "working",
		PackageRef:    "@telos/auth:1.0.0",
		PackageDigest: "sha256:abc",
		AgentModel:    "provider/model",
		AgentThinking: "high",
		ServiceURL:    &serviceURL,
		DashboardURL:  &dashboardURL,
	}

	var out bytes.Buffer
	printCloudSessionReceiptForContext(&out, "created", session, "@personal")
	text := out.String()
	for _, want := range []string{
		"created auth",
		"Status    working",
		"Session   sess_123",
		"Revision  sha256:abc",
		"Context   @personal",
		"Service   https://auth.example.com",
		"Logs      telos logs --context @personal sess_123",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("cloud session receipt missing %q:\n%s", want, text)
		}
	}
	for _, notWant := range []string{"Name", "Target", "Package", "Digest", "Model", "Thinking", "Dashboard"} {
		if strings.Contains(text, notWant) {
			t.Fatalf("cloud session receipt should omit %q:\n%s", notWant, text)
		}
	}
}

func TestPrintCloudSessionDescriptionOmitsUnavailableSurfaces(t *testing.T) {
	dashboardURL := "https://dashboard.example.com"
	session := cloud.SessionRecord{
		ID:            "sess_123",
		Name:          "auth",
		State:         "deploying",
		PackageRef:    "@telos/auth:1.0.0",
		PackageDigest: "sha256:abc",
		DashboardURL:  &dashboardURL,
		CreatedAt:     "then",
		UpdatedAt:     "now",
	}

	var out bytes.Buffer
	printCloudSessionDescription(&out, session)
	text := out.String()
	for _, want := range []string{
		"Status    deploying",
		"Session   sess_123",
		"Revision  sha256:abc",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("cloud session description missing %q:\n%s", want, text)
		}
	}
	for _, notWant := range []string{"Service", "Dashboard", "pending", "Inspect"} {
		if strings.Contains(text, notWant) {
			t.Fatalf("cloud session description should omit %q:\n%s", notWant, text)
		}
	}
}

func TestPrintCloudSessionDescriptionShowsCanonicalFailureReason(t *testing.T) {
	failureReason := "Provider authentication failed"
	session := cloud.SessionRecord{
		ID:            "sess_123",
		Name:          "auth",
		State:         "failed",
		Status:        "needs_attention",
		StatusReason:  "The session needs operator attention.",
		PackageDigest: "sha256:abc",
		FailureReason: &failureReason,
	}

	var out bytes.Buffer
	printCloudSessionDescription(&out, session)
	if !strings.Contains(out.String(), "Reason    Provider authentication failed") {
		t.Fatalf("cloud session description missing canonical reason:\n%s", out.String())
	}
}

func TestPrintCloudSessionJSONContainsOnlyAuthoritativeRecord(t *testing.T) {
	session := &cloud.SessionRecord{
		ID:            "sess_123",
		Name:          "auth",
		State:         "deploying",
		Status:        "working",
		PackageDigest: "sha256:abc",
	}

	out := captureStdout(t, func() {
		printCloudSessionJSON(session, "org_telos")
	})
	var body map[string]any
	if err := json.Unmarshal([]byte(out), &body); err != nil {
		t.Fatal(err)
	}
	if body["id"] != "sess_123" || body["status"] != "working" || body["context"] != "org_telos" {
		t.Fatalf("cloud session JSON: %#v", body)
	}
	for _, key := range []string{"progress", "progress_error", "stage", "latest_activity", "waiting_action"} {
		if _, ok := body[key]; ok {
			t.Fatalf("cloud session JSON contains derived field %q: %#v", key, body)
		}
	}
}

func TestPrintCloudSessionDeleteReceiptUsesSessionSummary(t *testing.T) {
	session := cloud.SessionRecord{
		ID:            "sess_123",
		Name:          "auth",
		State:         "deleted",
		PackageRef:    "@telos/auth:1.0.0",
		PackageDigest: "sha256:abc",
		CreatedAt:     "then",
		UpdatedAt:     "now",
	}

	var out bytes.Buffer
	printCloudSessionDeleteReceipt(&out, session)
	text := out.String()
	for _, want := range []string{
		"deleted auth",
		"Status    deleted",
		"Session   sess_123",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("cloud session stop receipt missing %q:\n%s", want, text)
		}
	}
	for _, notWant := range []string{"Name", "Target", "Package"} {
		if strings.Contains(text, notWant) {
			t.Fatalf("cloud session delete receipt should omit %q:\n%s", notWant, text)
		}
	}
}

func TestPrintCloudSessionDeleteReceiptShowsAsyncDeletion(t *testing.T) {
	session := cloud.SessionRecord{
		ID:            "sess_123",
		Name:          "auth",
		State:         "deleting",
		PackageRef:    "@telos/auth:1.0.0",
		PackageDigest: "sha256:abc",
		CreatedAt:     "then",
		UpdatedAt:     "now",
	}

	var out bytes.Buffer
	printCloudSessionDeleteReceipt(&out, session)
	text := out.String()
	for _, want := range []string{
		"delete requested for auth",
		"Status    deleting",
		"Session   sess_123",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("cloud session delete receipt missing %q:\n%s", want, text)
		}
	}
}

func TestPrintCloudSessionDeleteJSONOmitsDescribeProgress(t *testing.T) {
	session := &cloud.SessionRecord{
		ID:    "sess_123",
		Name:  "auth",
		State: "deleted",
	}

	out := captureStdout(t, func() {
		printCloudSessionDeleteJSON(session, "org_telos")
	})
	var body map[string]any
	if err := json.Unmarshal([]byte(out), &body); err != nil {
		t.Fatal(err)
	}
	if body["context"] != "org_telos" {
		t.Fatalf("context = %#v", body["context"])
	}
	if _, ok := body["progress"]; ok {
		t.Fatalf("delete JSON contains describe progress: %#v", body)
	}
}

func TestCmdDeleteDeletesCloudSession(t *testing.T) {
	var deleted bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/deployments/sess_123" {
			http.NotFound(w, r)
			return
		}
		deleted = true
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":             "sess_123",
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
		cmdDelete([]string{"sess_123"})
	})
	if !deleted {
		t.Fatal("expected cloud session delete request")
	}
	for _, want := range []string{
		"deleted auth",
		"Status    deleted",
		"Session   sess_123",
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
			want: "reconciling",
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
	t.Setenv("TELOS_API_ENDPOINT", cluster.URL)

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
	rootSpec := "---\nversion: 0.1.0\nname: root\nplatform: local\n---\n# Root\n"
	rootSession, err := store.Create(sessionapi.SessionCreateRequest{
		SpecMarkdown: &rootSpec,
		SessionKind:  &rootKind,
	})
	if err != nil {
		t.Fatalf("Create root: %v", err)
	}
	childSpec := "---\nversion: 0.1.0\nname: child\nplatform: local\n---\n# Child\n"
	child, err := store.Create(sessionapi.SessionCreateRequest{
		SpecMarkdown:    &childSpec,
		ParentSessionID: &rootSession.SessionID,
	})
	if err != nil {
		t.Fatalf("Create child: %v", err)
	}
	siblingSpec := "---\nversion: 0.1.0\nname: sibling\nplatform: local\n---\n# Sibling\n"
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
	want := []string{rootSession.SessionID, child.SessionID}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("scoped sessions: got %v want %v", got, want)
	}
}

func TestPrintSessionDescriptionIncludesOnlyLifecycleEssentials(t *testing.T) {
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
		"Target    cloud",
		"Status    completed",
		"Session   sess_123",
		"Cost      $1.2300",
		"Revision  2",
		"Parent    sess_parent",
		"Service   https://postgres.example",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("description missing %q:\n%s", want, text)
		}
	}
	for _, notWant := range []string{
		"Lifecycle", "result", "lineage", "interval", "completion", "evaluation",
		"rounds", "Latest Epoch", "Paths", "workspace", "evidence", "transcript",
	} {
		if strings.Contains(text, notWant) {
			t.Fatalf("description should omit %q:\n%s", notWant, text)
		}
	}
}

func TestPrintSessionDescriptionOmitsScheduleInternals(t *testing.T) {
	for _, tc := range []struct {
		name string
		secs int
		want string
	}{
		{name: "seconds", secs: 45, want: "45s"},
		{name: "minutes", secs: 300, want: "5m"},
		{name: "hours", secs: 7200, want: "2h"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := sessionapi.Session{
				SessionID: "sess_123",
				Status:    sessionapi.StatusRunning,
				Runtime:   sessionapi.RuntimeCloud,
				Specs: []sessionapi.SessionSpec{{
					IntervalSeconds: &tc.secs,
				}},
			}

			var out bytes.Buffer
			printSessionDescription(&out, session)
			text := out.String()
			if strings.Contains(text, tc.want) || strings.Contains(text, "interval") {
				t.Fatalf("description should omit schedule details:\n%s", text)
			}
		})
	}
}

func TestPrintSessionDescriptionOmitsActiveEngineState(t *testing.T) {
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
	for _, notWant := range []string{"evaluation", "current turn", "implementation", "prover"} {
		if strings.Contains(text, notWant) {
			t.Fatalf("description should omit %q:\n%s", notWant, text)
		}
	}
}

func TestPrintSessionDescriptionOmitsIdleEngineState(t *testing.T) {
	verifierConceded := true
	session := sessionapi.Session{
		SessionID:        "sess_idle",
		Status:           sessionapi.StatusRunning,
		Runtime:          sessionapi.RuntimeLocal,
		VerifierConceded: &verifierConceded,
	}

	var out bytes.Buffer
	printSessionDescription(&out, session)
	text := out.String()
	if strings.Contains(text, "evaluation") || strings.Contains(text, "accepted") {
		t.Fatalf("description should omit evaluation state:\n%s", text)
	}
}

func TestPrintSessionDescriptionOmitsCompletionImplementationDetails(t *testing.T) {
	completionReason := "review_budget_exhausted"
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
	for _, notWant := range []string{"completion", "review_budget_exhausted", "evaluation"} {
		if strings.Contains(text, notWant) {
			t.Fatalf("description should omit %q:\n%s", notWant, text)
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
		"Target    local",
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
		"Status    idle",
		"Session   sess_123",
		"Cost      $1.1907",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("receipt missing %q:\n%s", want, text)
		}
	}
	for _, notWant := range []string{"Name", "Target"} {
		if strings.Contains(text, notWant) {
			t.Fatalf("receipt should omit %q:\n%s", notWant, text)
		}
	}
}

func TestPrintLocalSessionDeleteReceiptUsesSessionSummary(t *testing.T) {
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
	printLocalSessionDeleteReceipt(&out, session)
	text := out.String()
	for _, want := range []string{
		"deleted gitea (history preserved)",
		"Status    stopped",
		"Session   sess_123",
		"Cost      $1.1907",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("delete receipt missing %q:\n%s", want, text)
		}
	}
	for _, notWant := range []string{"Name", "Target"} {
		if strings.Contains(text, notWant) {
			t.Fatalf("delete receipt should omit %q:\n%s", notWant, text)
		}
	}
}

func TestPrintLocalSessionDeleteReceiptUsesSessionIDForUnnamedSession(t *testing.T) {
	session := sessionapi.Session{
		SessionID: "sess_123",
		Runtime:   sessionapi.RuntimeLocal,
		Status:    sessionapi.StatusStopped,
	}

	var out bytes.Buffer
	printLocalSessionDeleteReceipt(&out, session)
	text := out.String()
	for _, want := range []string{
		"deleted sess_123 (history preserved)",
		"Status    stopped",
		"Session   sess_123",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("delete receipt missing %q:\n%s", want, text)
		}
	}
	for _, notWant := range []string{"Name", "Target", "Cost"} {
		if strings.Contains(text, notWant) {
			t.Fatalf("delete receipt should omit %q:\n%s", notWant, text)
		}
	}
}

package sessionapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func execGoal(t *testing.T) (*FileStore, string, string) {
	t.Helper()
	store := NewFileStore(t.TempDir(), RuntimeCloud)
	id := "goal_exec"
	dir := filepath.Join(store.Root, id)
	if err := os.MkdirAll(filepath.Join(dir, "workspace"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := &Manifest{
		SessionID: id, SessionKind: KindController, DesiredStatus: DesiredStatusRunning,
		Paused: true, Config: SessionConfig{Model: "test/model", Thinking: "high"},
		Access: &ScopedToken{APIToken: "goal-token", SubjectSessionID: id, Scopes: []string{string(ScopeSessionsRun)}},
	}
	if err := WriteManifest(filepath.Join(dir, "session.json"), manifest); err != nil {
		t.Fatal(err)
	}
	return store, id, dir
}

func TestExecUsesWorkspaceAndPreservesArgumentsOutputAndExitStatus(t *testing.T) {
	store, id, dir := execGoal(t)
	before, _ := os.ReadFile(filepath.Join(dir, "session.json"))
	result, err := store.Exec(context.Background(), id, SessionExecRequest{Argv: []string{
		"sh", "-c", `printf '%s\n' "$@"; printf '\000\377' >&2; echo changed > state.txt; exit 37`,
		"command", "a b", "--json", "$(exit 90)",
	}})
	if err != nil || result.ExitCode != 37 || string(result.Stdout) != "a b\n--json\n$(exit 90)\n" || !bytes.Equal(result.Stderr, []byte{0, 255}) {
		t.Fatalf("result = %+v, error = %v", result, err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "workspace", "state.txt")); err != nil || string(data) != "changed\n" {
		t.Fatalf("workspace change = %q, %v", data, err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "session.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("exec changed the manifest")
	}
}

func TestExecRequiresAPausedRootWithAnExistingWorkspace(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Manifest)
	}{
		{"running", func(m *Manifest) { m.Paused = false }},
		{"blocked", func(m *Manifest) { m.Paused = false; m.Issue = &Issue{Status: IssueBlocked} }},
		{"stopped", func(m *Manifest) { m.DesiredStatus = DesiredStatusStopped }},
		{"task", func(m *Manifest) { m.SessionKind = KindTask }},
		{"child controller", func(m *Manifest) { parent := "goal_parent"; m.ParentSessionID = &parent }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, id, dir := execGoal(t)
			if _, err := MutateManifest(filepath.Join(dir, "session.json"), func(m *Manifest) error { tc.edit(m); return nil }); err != nil {
				t.Fatal(err)
			}
			_, err := store.Exec(context.Background(), id, SessionExecRequest{Argv: []string{"touch", "should-not-exist"}})
			if !errors.Is(err, ErrConflict) && !errors.Is(err, ErrInvalidSession) {
				t.Fatalf("exec should reject %s: %v", tc.name, err)
			}
			if _, err := os.Stat(filepath.Join(dir, "workspace", "should-not-exist")); !os.IsNotExist(err) {
				t.Fatalf("command ran: %v", err)
			}
		})
	}
	store, id, dir := execGoal(t)
	if err := os.RemoveAll(filepath.Join(dir, "workspace")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Exec(context.Background(), id, SessionExecRequest{Argv: []string{"true"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("missing workspace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "workspace")); !os.IsNotExist(err) {
		t.Fatal("exec created a workspace")
	}
	if _, err := store.Exec(context.Background(), "goal_missing", SessionExecRequest{Argv: []string{"true"}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing Goal: %v", err)
	}
}

func TestExecExcludesWorkerAndConcurrentCommands(t *testing.T) {
	store, id, dir := execGoal(t)
	lock, err := LockWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err := store.Exec(context.Background(), id, SessionExecRequest{Argv: []string{"true"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("busy workspace: %v", err)
	}
	lock.Close()
	if result, err := store.Exec(context.Background(), id, SessionExecRequest{Argv: []string{"true"}}); err != nil || result.ExitCode != 0 {
		t.Fatalf("released workspace: %+v, %v", result, err)
	}
}

func TestExecLimitsOutputAndCleansUpProcessGroups(t *testing.T) {
	store, id, dir := execGoal(t)
	result, err := store.Exec(context.Background(), id, SessionExecRequest{
		Argv: []string{"sh", "-c", "head -c 1100000 /dev/zero; head -c 1100000 /dev/zero >&2"},
	})
	if err != nil || result.ExitCode != 0 || len(result.Stdout) != MaxExecOutputBytes || len(result.Stderr) != MaxExecOutputBytes || !result.StdoutTruncated || !result.StderrTruncated {
		t.Fatalf("bounded output: %+v, %v", result, err)
	}
	for _, tc := range []struct {
		name, script string
		timeout      int
		code         int
	}{
		{"timeout", "(sleep 0.4; touch leaked) & wait", 50, 124},
		{"background child", "(sleep 0.4; touch leaked) & exit 0", 2000, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := store.Exec(context.Background(), id, SessionExecRequest{Argv: []string{"sh", "-c", tc.script}, TimeoutMS: tc.timeout})
			if err != nil || result.ExitCode != tc.code || result.TimedOut != (tc.code == 124) {
				t.Fatalf("result = %+v, %v", result, err)
			}
			time.Sleep(450 * time.Millisecond)
			if _, err := os.Stat(filepath.Join(dir, "workspace", "leaked")); !os.IsNotExist(err) {
				t.Fatalf("descendant outlived exec: %v", err)
			}
			if result, err := store.Exec(context.Background(), id, SessionExecRequest{Argv: []string{"true"}}); err != nil || result.ExitCode != 0 {
				t.Fatalf("workspace remained locked: %+v, %v", result, err)
			}
		})
	}
}

func TestExecUsesGoalCredentialsAndReportsMissingCommands(t *testing.T) {
	store, id, _ := execGoal(t)
	t.Setenv("TELOS_API_TOKEN", "operator-token")
	t.Setenv("TELOS_CLOUD_API_TOKEN", "cloud-secret")
	t.Setenv("TELOS_API_ENDPOINT", "http://runtime.test")
	result, err := store.Exec(context.Background(), id, SessionExecRequest{Argv: []string{
		"sh", "-c", `printf '%s|%s|%s|%s' "$TELOS_API_TOKEN" "$TELOS_CLOUD_API_TOKEN" "$TELOS_SESSION_ID" "$TELOS_API_ENDPOINT"`,
	}})
	if err != nil || string(result.Stdout) != "goal-token||goal_exec|http://runtime.test" {
		t.Fatalf("command environment: %+v, %v", result, err)
	}
	result, err = store.Exec(context.Background(), id, SessionExecRequest{Argv: []string{"telos-no-such-command"}})
	if err != nil || result.ExitCode != 127 || !strings.Contains(string(result.Stderr), "command not found") {
		t.Fatalf("missing command: %+v, %v", result, err)
	}
}

func TestExecCancellationSignalsAndInvalidCommands(t *testing.T) {
	store, id, dir := execGoal(t)
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(40*time.Millisecond, cancel)
	defer timer.Stop()
	defer cancel()
	result, err := store.Exec(ctx, id, SessionExecRequest{Argv: []string{"sh", "-c", "sleep 10"}})
	if err != nil || result.ExitCode != 130 || result.TimedOut {
		t.Fatalf("canceled command: %+v, %v", result, err)
	}
	result, err = store.Exec(context.Background(), id, SessionExecRequest{Argv: []string{"sh", "-c", "kill -TERM $$"}})
	if err != nil || result.ExitCode != 143 {
		t.Fatalf("signaled command: %+v, %v", result, err)
	}
	path := filepath.Join(dir, "workspace", "not-executable")
	if err := os.WriteFile(path, []byte("exit 0"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err = store.Exec(context.Background(), id, SessionExecRequest{Argv: []string{"./not-executable"}})
	if err != nil || result.ExitCode != 126 {
		t.Fatalf("non-executable command: %+v, %v", result, err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	result, err = store.Exec(context.Background(), id, SessionExecRequest{Argv: []string{"./not-executable"}})
	if err != nil || result.ExitCode != 126 {
		t.Fatalf("invalid executable format: %+v, %v", result, err)
	}
	for _, request := range []SessionExecRequest{
		{Argv: []string{""}}, {Argv: []string{"a\x00b"}}, {Argv: []string{"\xff"}},
		{Argv: make([]string, 257)}, {Argv: []string{strings.Repeat("a", 128<<10+1)}},
		{Argv: []string{"true"}, TimeoutMS: -1},
	} {
		if _, err := store.Exec(context.Background(), id, request); !errors.Is(err, ErrInvalidSession) {
			t.Fatalf("invalid request accepted: %v", err)
		}
	}
	if _, err := store.Exec(context.Background(), "../goal_exec", SessionExecRequest{Argv: []string{"true"}}); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("invalid Goal ID: %v", err)
	}
}

func TestExecAPIRequiresOperatorAndValidatesRequests(t *testing.T) {
	store, id, _ := execGoal(t)
	mux := http.NewServeMux()
	RegisterRoutes(mux, store, NewBearerAuthorizer(store, "operator-token"), RuntimeIdentity{})
	for _, tc := range []struct {
		token, body string
		status      int
	}{
		{"", `{"argv":["true"]}`, 401},
		{"goal-token", `{"argv":["true"]}`, 403},
		{"operator-token", `{"argv":["true"],"shell":true}`, 400},
		{"operator-token", `{"argv":["true"]} {}`, 400},
		{"operator-token", `{"argv":[]}`, 400},
		{"operator-token", `{"argv":["true"],"timeout_ms":300001}`, 400},
		{"operator-token", `{"argv":["printf","hello"]}`, 200},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+id+"/exec", strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer "+tc.token)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, req)
		if response.Code != tc.status {
			t.Fatalf("token %q body %s: %d %s", tc.token, tc.body, response.Code, response.Body)
		}
		if tc.status == 200 {
			var result SessionExecResponse
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || string(result.Stdout) != "hello" {
				t.Fatalf("response = %s, %v", response.Body, err)
			}
		}
	}
}

package sessionapi_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/telos-org/telos/internal/sessionapi"
)

func TestConcurrentManifestWritesAcrossProcesses(t *testing.T) {
	if os.Getenv("TELOS_MANIFEST_WRITE_HELPER") == "1" {
		helperManifestWriter(t)
		return
	}

	root := t.TempDir()
	path := filepath.Join(root, "sess_concurrent", "session.json")

	cmds := []*exec.Cmd{
		helperManifestCommand(t, path, "writer-a"),
		helperManifestCommand(t, path, "writer-b"),
	}
	for _, cmd := range cmds {
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
	}
	for _, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("helper failed: %v\n%s", err, cmd.Stderr)
		}
	}

	m, err := sessionapi.ReadManifest(path)
	if err != nil {
		t.Fatalf("final manifest should be readable: %v", err)
	}
	if m.SessionID != "sess_concurrent" {
		t.Fatalf("session id changed: %q", m.SessionID)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("fixed temp path should not be used, stat err=%v", err)
	}
}

func helperManifestCommand(t *testing.T, path string, writer string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestConcurrentManifestWritesAcrossProcesses")
	cmd.Env = append(os.Environ(),
		"TELOS_MANIFEST_WRITE_HELPER=1",
		"TELOS_MANIFEST_PATH="+path,
		"TELOS_MANIFEST_WRITER="+writer,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	return cmd
}

func helperManifestWriter(t *testing.T) {
	t.Helper()
	path := os.Getenv("TELOS_MANIFEST_PATH")
	writer := os.Getenv("TELOS_MANIFEST_WRITER")
	if path == "" || writer == "" {
		t.Fatal("helper environment missing")
	}
	for i := 0; i < 40; i++ {
		m := manifestFixture("sess_concurrent", sessionapi.KindTask)
		m.SpecName = writer
		m.Provenance = map[string]any{"writer": writer, "iteration": i}
		if err := sessionapi.WriteManifest(path, &m); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCrashTempFilesAreIgnored(t *testing.T) {
	root := t.TempDir()
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeLocal)
	sessionDir := filepath.Join(root, "sess_temp")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "session.json.tmp"), []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, ".session.json.crash.tmp"), []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := manifestFixture("sess_temp", sessionapi.KindTask)
	if err := sessionapi.WriteManifest(filepath.Join(sessionDir, "session.json"), &m); err != nil {
		t.Fatal(err)
	}

	got, err := store.Get("sess_temp")
	if err != nil {
		t.Fatalf("Get should ignore temp files: %v", err)
	}
	if got.SessionID != "sess_temp" {
		t.Fatalf("got session %q", got.SessionID)
	}
	sessions, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("List should ignore temp files, got %d sessions", len(sessions))
	}
}

func TestScopedTokenHashIndexLegacyAndRevocation(t *testing.T) {
	root := t.TempDir()
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeCloud)

	access, err := sessionapi.NewScopedToken("sess_indexed", sessionapi.KindController)
	if err != nil {
		t.Fatal(err)
	}
	writeManifestWithAccess(t, root, "sess_indexed", sessionapi.KindController, access, false)
	if err := store.IndexScopedToken("sess_indexed", sessionapi.KindController, access); err != nil {
		t.Fatal(err)
	}
	persisted, err := sessionapi.ReadManifest(filepath.Join(root, "sess_indexed", "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Access == nil || persisted.Access.APIToken != "" || persisted.Access.TokenSHA256 == "" {
		t.Fatalf("persisted manifest should contain only token hash: %#v", persisted.Access)
	}
	if caller, ok := store.CallerForToken(access.APIToken); !ok || caller.SubjectSessionID != "sess_indexed" || caller.Role != sessionapi.RoleController {
		t.Fatalf("indexed token did not authenticate: caller=%#v ok=%v", caller, ok)
	}

	revoked, err := store.RevokeScopedToken(access.APIToken)
	if err != nil {
		t.Fatal(err)
	}
	if !revoked {
		t.Fatal("expected token to be revoked")
	}
	if _, ok := store.CallerForToken(access.APIToken); ok {
		t.Fatal("revoked token authenticated")
	}
	if err := sessionapi.CheckStoreEventLogIntegrity(filepath.Join(root, "sess_indexed", "events.jsonl")); err != nil {
		t.Fatalf("revocation event log integrity: %v", err)
	}

	legacyRoot := t.TempDir()
	legacyStore := sessionapi.NewFileStore(legacyRoot, sessionapi.RuntimeCloud)
	legacyAccess, err := sessionapi.NewScopedToken("sess_legacy", sessionapi.KindTask)
	if err != nil {
		t.Fatal(err)
	}
	writeManifestWithAccess(t, legacyRoot, "sess_legacy", sessionapi.KindTask, legacyAccess, true)
	if caller, ok := legacyStore.CallerForToken(legacyAccess.APIToken); !ok || caller.SubjectSessionID != "sess_legacy" || caller.Role != sessionapi.RoleTask {
		t.Fatalf("legacy token did not authenticate: caller=%#v ok=%v", caller, ok)
	}
	if _, err := os.Stat(filepath.Join(legacyRoot, ".scoped-token-index.json")); !os.IsNotExist(err) {
		t.Fatalf("legacy lookup should not write an index, stat err=%v", err)
	}
}

func TestReplaceWorkerScopedTokenRevokesPreviousWorkerOnly(t *testing.T) {
	root := t.TempDir()
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeCloud)

	previousWorker, err := sessionapi.NewScopedToken("sess_worker", sessionapi.KindController)
	if err != nil {
		t.Fatal(err)
	}
	userScoped, err := sessionapi.NewScopedToken("sess_worker", sessionapi.KindController)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := sessionapi.NewScopedToken("sess_worker", sessionapi.KindController)
	if err != nil {
		t.Fatal(err)
	}
	writeManifestWithAccess(t, root, "sess_worker", sessionapi.KindController, userScoped, false)
	if err := store.IndexScopedToken("sess_worker", sessionapi.KindController, previousWorker); err != nil {
		t.Fatal(err)
	}
	if err := store.IndexScopedToken("sess_worker", sessionapi.KindController, userScoped); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceWorkerScopedToken("sess_worker", sessionapi.KindController, replacement, previousWorker.APIToken); err != nil {
		t.Fatal(err)
	}

	if _, ok := store.CallerForToken(previousWorker.APIToken); ok {
		t.Fatal("previous worker token authenticated")
	}
	if caller, ok := store.CallerForToken(replacement.APIToken); !ok || caller.SubjectSessionID != "sess_worker" || caller.Role != sessionapi.RoleController {
		t.Fatalf("replacement worker token did not authenticate: caller=%#v ok=%v", caller, ok)
	}
	if caller, ok := store.CallerForToken(userScoped.APIToken); !ok || caller.SubjectSessionID != "sess_worker" || caller.Role != sessionapi.RoleController {
		t.Fatalf("user scoped token should survive: caller=%#v ok=%v", caller, ok)
	}
}

func TestStoreEventSequenceIntegrity(t *testing.T) {
	root := t.TempDir()
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeLocal)
	sessionDir := filepath.Join(root, "sess_events")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, typ := range []sessionapi.StoreEventType{sessionapi.EventStopRequested, sessionapi.EventStopSignalSent} {
		if _, err := store.AppendStoreEvent("sess_events", typ, map[string]any{"ok": true}); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(sessionDir, "events.jsonl")
	if err := sessionapi.CheckStoreEventLogIntegrity(path); err != nil {
		t.Fatalf("valid log failed integrity: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := strings.Replace(string(data), `"sequence":2`, `"sequence":4`, 1)
	corruptPath := filepath.Join(sessionDir, "events-corrupt.jsonl")
	if err := os.WriteFile(corruptPath, []byte(corrupt), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := sessionapi.CheckStoreEventLogIntegrity(corruptPath); err == nil {
		t.Fatal("expected sequence gap to be detected")
	}

	truncatedPath := filepath.Join(sessionDir, "events-truncated.jsonl")
	if err := os.WriteFile(truncatedPath, []byte(`{"sequence":1`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := sessionapi.CheckStoreEventLogIntegrity(truncatedPath); err == nil {
		t.Fatal("expected truncated log to be detected")
	}
}

func TestStopLifecycleEscalatesSIGKILL(t *testing.T) {
	t.Setenv("TELOS_STOP_GRACE_MS", "50")
	root := t.TempDir()
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeLocal)
	cmd := stopHelperCommand(t, "ignore-term")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	writeOpenRunnerManifest(t, root, "sess_kill", cmd.Process.Pid)

	session, err := store.Stop("sess_kill")
	if err != nil {
		t.Fatal(err)
	}
	if session.Status != sessionapi.StatusStopped {
		t.Fatalf("status: got %q", session.Status)
	}
	events := readStoreEventTypes(t, filepath.Join(root, "sess_kill", "events.jsonl"))
	want := []sessionapi.StoreEventType{
		sessionapi.EventStopRequested,
		sessionapi.EventStopSignalSent,
		sessionapi.EventForcedKill,
		sessionapi.EventProcessExitObserved,
	}
	if strings.Join(storeEventTypeStrings(events), ",") != strings.Join(storeEventTypeStrings(want), ",") {
		t.Fatalf("events: got %#v want %#v", events, want)
	}
	m, err := sessionapi.ReadManifest(filepath.Join(root, "sess_kill", "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Epochs[0].Runner.Status; !strings.Contains(got, "killed") {
		t.Fatalf("runner status: %q", got)
	}
	_ = cmd.Process.Release()
}

func TestStopLifecycleCompliantWorkerExitsWithoutSIGKILL(t *testing.T) {
	t.Setenv("TELOS_STOP_GRACE_MS", "500")
	root := t.TempDir()
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeLocal)
	cmd := stopHelperCommand(t, "exit-term")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	writeOpenRunnerManifest(t, root, "sess_term", cmd.Process.Pid)

	if _, err := store.Stop("sess_term"); err != nil {
		t.Fatal(err)
	}
	events := readStoreEventTypes(t, filepath.Join(root, "sess_term", "events.jsonl"))
	want := []sessionapi.StoreEventType{
		sessionapi.EventStopRequested,
		sessionapi.EventStopSignalSent,
		sessionapi.EventProcessExitObserved,
	}
	if strings.Join(storeEventTypeStrings(events), ",") != strings.Join(storeEventTypeStrings(want), ",") {
		t.Fatalf("events: got %#v want %#v", events, want)
	}
	_ = cmd.Process.Release()
}

func stopHelperCommand(t *testing.T, mode string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestStopLifecycleHelper")
	cmd.Env = append(os.Environ(), "TELOS_STOP_HELPER="+mode)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	return cmd
}

func TestStopLifecycleHelper(t *testing.T) {
	switch os.Getenv("TELOS_STOP_HELPER") {
	case "ignore-term":
		stopHelperIgnoreTERM()
	case "exit-term":
		stopHelperExitOnTERM()
	default:
		t.Skip("helper only")
	}
}

func stopHelperIgnoreTERM() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM)
	for {
		<-signals
	}
}

func stopHelperExitOnTERM() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM)
	<-signals
	os.Exit(0)
}

func writeOpenRunnerManifest(t *testing.T, root string, id string, pid int) {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	m := manifestFixture(id, sessionapi.KindTask)
	runner := sessionapi.Runner{
		Kind:      "local-subprocess",
		PID:       pid,
		PGID:      pid,
		StartedAt: "2026-07-04T00:00:00.000Z",
	}
	m.Epochs = append(m.Epochs, sessionapi.Epoch{
		ID:        1,
		StartedAt: "2026-07-04T00:00:00.000Z",
		Runner:    &runner,
	})
	if err := sessionapi.WriteManifest(filepath.Join(dir, "session.json"), &m); err != nil {
		t.Fatal(err)
	}
}

func readStoreEventTypes(t *testing.T, path string) []sessionapi.StoreEventType {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var data []byte
	var err error
	for {
		data, err = os.ReadFile(path)
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []sessionapi.StoreEventType
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event sessionapi.StoreEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		out = append(out, event.Type)
	}
	return out
}

func storeEventTypeStrings(events []sessionapi.StoreEventType) []string {
	out := make([]string, 0, len(events))
	for _, event := range events {
		out = append(out, string(event))
	}
	return out
}

func manifestFixture(id string, kind sessionapi.SessionKind) sessionapi.Manifest {
	return sessionapi.ManifestFromInitial(sessionapi.InitialManifest{
		SessionID:   id,
		SessionKind: kind,
		Runtime:     sessionapi.RuntimeLocal,
		CreatedAt:   "2026-07-04T00:00:00.000Z",
		Launcher:    "test",
		SpecName:    "demo",
		Provenance:  map[string]any{"mode": "test"},
		Config:      sessionapi.SessionConfig{},
		Specs:       []sessionapi.InitialManifestSpec{{Index: 0, Name: "demo", DirName: "demo"}},
	})
}

func writeManifestWithAccess(t *testing.T, root string, id string, kind sessionapi.SessionKind, access *sessionapi.ScopedToken, raw bool) {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	m := manifestFixture(id, kind)
	m.Runtime = sessionapi.RuntimeCloud
	m.Access = access
	if !raw {
		if err := sessionapi.WriteManifest(filepath.Join(dir, "session.json"), &m); err != nil {
			t.Fatal(err)
		}
		return
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session.json"), append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

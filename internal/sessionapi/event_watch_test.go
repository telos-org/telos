package sessionapi_test

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/telos-org/telos/internal/sessionapi"
)

type ownerStatusStore struct {
	*sessionapi.FileStore
	checked chan sessionapi.SessionStatus
}

func (s *ownerStatusStore) Status(id string) (sessionapi.SessionStatus, error) {
	status, err := s.FileStore.Status(id)
	select {
	case s.checked <- status:
	default:
	}
	return status, err
}

func TestEventWatchOwnerProcess(t *testing.T) {
	path := os.Getenv("TELOS_TEST_OWNER_LOCK")
	if path == "" {
		return
	}
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	_, _ = os.Stdout.Write([]byte("ready\n"))
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func startWatchOwner(t *testing.T, sessionDir string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestEventWatchOwnerProcess$")
	cmd.Env = append(os.Environ(), "TELOS_TEST_OWNER_LOCK="+filepath.Join(sessionDir, "runner.lock"))
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = input.Close()
		_ = cmd.Wait()
	})
	reader := bufio.NewReader(output)
	if line, err := reader.ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("owner readiness: %q, %v", line, err)
	}
	return cmd
}

func TestEventsStreamEndsWhenIndependentOwnerIsKilled(t *testing.T) {
	store := &ownerStatusStore{
		FileStore: sessionapi.NewFileStore(t.TempDir(), sessionapi.RuntimeLocal),
		checked:   make(chan sessionapi.SessionStatus, 1),
	}
	mux := http.NewServeMux()
	sessionapi.RegisterRoutes(mux, store, sessionapi.AllowAllAuthorizer{}, sessionapi.RuntimeIdentity{})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	created := createSession(t, srv.URL, createSessionBody(t, "owner-exit"))
	dir := filepath.Join(store.Root, created.SessionID)
	owner := startWatchOwner(t, dir)
	_, err := sessionapi.MutateManifest(filepath.Join(dir, "session.json"), func(m *sessionapi.Manifest) error {
		m.Runner = &sessionapi.Runner{PID: owner.Process.Pid}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest("GET", srv.URL+"/api/sessions/"+created.SessionID+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status: %d", resp.StatusCode)
	}
	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, resp.Body)
		done <- err
	}()
	select {
	case status := <-store.checked:
		if status != sessionapi.StatusRunning {
			t.Fatalf("owner was not alive at subscription: %s", status)
		}
	case <-time.After(time.Second):
		t.Fatal("stream did not check its initial status")
	}
	// The owner was already running when the subscription attached. Nothing
	// reaps it or touches session files after the kill, as with an exited CLI
	// launcher or an owner adopted by a restarted daemon.
	if err := owner.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("independent owner exit did not end SSE within one second")
	}
	if status, err := store.Status(created.SessionID); err != nil || status != sessionapi.StatusStale {
		t.Fatalf("owner exit status: %s, %v", status, err)
	}
}

func TestEventsStreamObservesAppendsAndManifestReplacement(t *testing.T) {
	srv, store := newTestServer(t)
	defer srv.Close()
	created := createSession(t, srv.URL, createSessionBody(t, "watched"))
	req, err := http.NewRequest("GET", srv.URL+"/api/sessions/"+created.SessionID+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("stream status: %d", resp.StatusCode)
	}
	events := make(chan string, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "data: ") {
				events <- scanner.Text()
			}
		}
	}()
	expect := func(name string) {
		t.Helper()
		select {
		case line := <-events:
			if !strings.Contains(line, `"event":"`+name+`"`) {
				t.Fatalf("unexpected event: %s", line)
			}
		case <-time.After(750 * time.Millisecond):
			t.Fatal("file change did not reach stream within 750ms")
		}
	}
	writeEventsFixture(t, store.Root, created.SessionID, "watched", []string{`{"event_seq":1,"event":"first"}`})
	expect("first")

	// The manifest is atomically replaced before the new evidence directory
	// exists. Both creation and subsequent appends must remain observable.
	manifestPath := filepath.Join(store.Root, created.SessionID, "session.json")
	manifest, err := sessionapi.ReadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	next := filepath.Join(store.Root, created.SessionID, "replacement", "nested", "events.jsonl")
	manifest.Specs[0].EvidencePath = &next
	if err := sessionapi.WriteManifest(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(next), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(next, []byte(`{"event_seq":2,"event":"second"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	expect("second")
	file, err := os.OpenFile(next, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteString(`{"event_seq":3,"event":"third"}` + "\n")
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	expect("third")
	stopSession(t, srv.URL, created.SessionID)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("terminal manifest change did not end stream")
	}
}

func TestEventWatchCloseReleasesSubscriber(t *testing.T) {
	srv, store := newTestServer(t)
	defer srv.Close()
	created := createSession(t, srv.URL, createSessionBody(t, "watchclose"))
	dir := filepath.Join(store.Root, created.SessionID)
	owner := startWatchOwner(t, dir)
	if _, err := sessionapi.MutateManifest(filepath.Join(dir, "session.json"), func(m *sessionapi.Manifest) error {
		m.Runner = &sessionapi.Runner{PID: owner.Process.Pid}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	changed, closeWatch, err := store.WatchEvents(created.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	closeWatch()
	closeWatch()
	select {
	case _, ok := <-changed:
		if ok {
			t.Fatal("watch remained open after close")
		}
	case <-time.After(time.Second):
		t.Fatal("watch cleanup did not finish")
	}
	if err := owner.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("watch cleanup required owner exit: %v", err)
	}
}

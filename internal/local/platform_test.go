package local

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/telos-org/telos-go/internal/sessionapi"
)

func writeLocalSpec(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "SPEC.md")
	body := "---\nversion: v0\nname: local-test\nplatform: local\n---\n# Local Test\n\nBody."
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLocalPlatformCreateSpawnsWorker(t *testing.T) {
	dir := t.TempDir()
	specPath := writeLocalSpec(t, dir)
	p := NewLocalPlatform(filepath.Join(dir, ".telos", "sessions"))

	var spawned string
	p.spawn = func(sessionDir string) error {
		spawned = sessionDir
		return nil
	}

	session, err := p.Create(sessionapi.SessionCreateRequest{SpecPath: &specPath})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if session.SessionID == "" {
		t.Fatal("empty session id")
	}
	if spawned == "" {
		t.Fatal("worker was not spawned")
	}
	// No epoch yet (the worker would open it); a fresh session is pending.
	if session.Status != sessionapi.StatusPending {
		t.Fatalf("status: got %s, want pending", session.Status)
	}

	// The embedded FileStore can read the session back.
	got, err := p.Get(session.SessionID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.SessionID != session.SessionID {
		t.Fatalf("Get returned %q, want %q", got.SessionID, session.SessionID)
	}
}

func TestLocalPlatformCreateRequiresSpecPath(t *testing.T) {
	p := NewLocalPlatform(t.TempDir())
	p.spawn = func(string) error { return nil }
	if _, err := p.Create(sessionapi.SessionCreateRequest{}); err == nil {
		t.Fatal("expected missing spec_path error")
	}
}

func TestLocalPlatformStopMarksStopped(t *testing.T) {
	dir := t.TempDir()
	specPath := writeLocalSpec(t, dir)
	p := NewLocalPlatform(filepath.Join(dir, ".telos", "sessions"))
	p.spawn = func(string) error { return nil }

	session, err := p.Create(sessionapi.SessionCreateRequest{SpecPath: &specPath})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	stopped, err := p.Stop(session.SessionID)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if stopped.Status != sessionapi.StatusStopped {
		t.Fatalf("status: got %s, want stopped", stopped.Status)
	}
}

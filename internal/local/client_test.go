package local

import (
	"errors"
	"os"
	"testing"
	"time"
)

func waitForSocket(t *testing.T, socket string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socket); err == nil {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("telosd socket never appeared")
}

func TestClientAgainstRunningDaemon(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TELOS_RUNTIME_ROOT", t.TempDir())
	paths := DaemonPathsFor(root)

	errCh := make(chan error, 1)
	go func() { errCh <- RunDaemon(root, 2) }()
	waitForSocket(t, paths.Socket)

	c := NewClient(root)

	sessions, err := c.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("want 0 sessions, got %d", len(sessions))
	}

	_, err = c.GetSession("does-not-exist")
	if err == nil {
		t.Fatal("expected error for missing session")
	}
	var de *DaemonError
	if !errors.As(err, &de) || de.Status != 404 {
		t.Fatalf("want 404 DaemonError, got %v", err)
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("RunDaemon returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not idle-shut-down")
	}
}

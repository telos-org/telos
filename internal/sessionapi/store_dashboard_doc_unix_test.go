//go:build darwin || linux

package sessionapi

import (
	"errors"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestReadDashboardDocRejectsNamedPipe(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, dashboardDocFilename)
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}

	done := make(chan bool, 1)
	go func() {
		_, ok := readDashboardDoc(workspace)
		done <- ok
	}()

	select {
	case ok := <-done:
		if ok {
			t.Fatal("named pipe should not be surfaced as dashboard_doc")
		}
	case <-time.After(time.Second):
		t.Fatal("reading a dashboard_doc named pipe blocked")
	}
}

func TestOpenWorkspaceArchiveRejectsSymlink(t *testing.T) {
	store, session := createCloudStoreSession(t)
	archivePath := filepath.Join(store.Root, session.SessionID, "specs", "service", "workspace.tar.gz")
	if err := syscall.Symlink("../../session.json", archivePath); err != nil {
		t.Fatal(err)
	}

	_, err := store.OpenWorkspaceArchive(session.SessionID)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("symlink archive error = %v, want ErrConflict", err)
	}
}

func TestOpenWorkspaceArchiveRejectsNamedPipeWithoutBlocking(t *testing.T) {
	store, session := createCloudStoreSession(t)
	archivePath := filepath.Join(store.Root, session.SessionID, "specs", "service", "workspace.tar.gz")
	if err := syscall.Mkfifo(archivePath, 0o600); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		archive, err := store.OpenWorkspaceArchive(session.SessionID)
		if archive != nil {
			archive.Close()
		}
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("named pipe error = %v, want ErrConflict", err)
		}
	case <-time.After(time.Second):
		t.Fatal("opening a named-pipe workspace archive blocked")
	}
}

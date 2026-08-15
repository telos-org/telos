//go:build darwin || linux

package sessionapi

import (
	"os"
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

	done := make(chan DashboardDocumentStatus, 1)
	go func() {
		_, status := readDashboardDoc(workspace)
		done <- status
	}()

	select {
	case status := <-done:
		if status != DashboardDocumentStatusInvalid {
			t.Fatalf("named pipe status = %q, want invalid", status)
		}
	case <-time.After(time.Second):
		t.Fatal("reading a dashboard_doc named pipe blocked")
	}
}

func TestReadDashboardDocAcceptsAtomicReplacement(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, dashboardDocFilename)
	replacement := filepath.Join(workspace, "dashboard.replacement")
	if err := os.WriteFile(replacement, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}

	doc, status := readDashboardDoc(workspace)
	if doc != "replacement" || status != DashboardDocumentStatusValid {
		t.Fatalf("replacement path returned (%q, %q), want valid replacement", doc, status)
	}
}

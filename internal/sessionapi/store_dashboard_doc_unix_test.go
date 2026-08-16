//go:build darwin || linux

package sessionapi

import (
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

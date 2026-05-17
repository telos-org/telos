package local

import (
	"context"
	"net"
	"net/http"
	"os"
	"testing"
	"time"
)

func unixHTTPClient(socket string) *http.Client {
	return &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			},
		},
	}
}

func TestRunDaemonServesAndIdleShutsDown(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TELOS_RUNTIME_ROOT", t.TempDir())
	paths := DaemonPathsFor(root)

	errCh := make(chan error, 1)
	go func() { errCh <- RunDaemon(root, 1) }()

	client := unixHTTPClient(paths.Socket)
	deadline := time.Now().Add(5 * time.Second)
	healthy := false
	for time.Now().Before(deadline) {
		resp, err := client.Get("http://unix/api/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				healthy = true
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !healthy {
		t.Fatal("daemon did not become healthy over the unix socket")
	}

	// The PID file should exist while the daemon is up.
	if _, err := os.Stat(paths.PID); err != nil {
		t.Errorf("pid file missing: %v", err)
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("RunDaemon returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not idle-shut-down")
	}

	if _, err := os.Stat(paths.Socket); !os.IsNotExist(err) {
		t.Errorf("socket not cleaned up: %v", err)
	}
	if _, err := os.Stat(paths.PID); !os.IsNotExist(err) {
		t.Errorf("pid file not cleaned up: %v", err)
	}
}

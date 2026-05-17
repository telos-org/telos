package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/telos-org/telos-go/internal/sessionapi"
)

// DefaultIdleSeconds is how long telosd stays up with no requests and no
// active sessions before shutting itself down.
const DefaultIdleSeconds = 300

// RunDaemon starts telosd for a workspace: it serves the Sessions API over a
// per-workspace Unix socket and exits once idle. It blocks until shutdown.
func RunDaemon(workspaceRoot string, idleSeconds int) error {
	root := absOr(workspaceRoot)
	paths := DaemonPathsFor(root)
	if err := os.MkdirAll(paths.RunDir, 0o700); err != nil {
		return fmt.Errorf("create run dir: %w", err)
	}

	ln, err := listenUnix(paths.Socket)
	if err != nil {
		return err
	}
	defer func() {
		ln.Close()
		os.Remove(paths.Socket)
		removePIDFile(paths.PID)
	}()
	if err := writePIDFile(paths.PID, root, paths.Socket); err != nil {
		return err
	}

	platform := NewLocalPlatform(filepath.Join(root, ".telos", "sessions"))
	tracker := newActivityTracker()
	mux := http.NewServeMux()
	sessionapi.RegisterRoutes(mux, platform)

	srv := &http.Server{
		Handler:           tracker.wrap(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	var shutdownOnce sync.Once
	shutdown := func() {
		shutdownOnce.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = srv.Shutdown(ctx)
		})
	}

	sigCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	go func() {
		<-sigCtx.Done()
		shutdown()
	}()

	monitorDone := make(chan struct{})
	defer close(monitorDone)
	go monitorIdle(monitorDone, shutdown, platform, tracker, idleSeconds)

	fmt.Fprintf(os.Stderr, "telosd listening on %s\n", paths.Socket)
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func listenUnix(socketPath string) (net.Listener, error) {
	_ = os.Remove(socketPath) // clear a stale socket from a prior daemon
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", socketPath, err)
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		ln.Close()
		os.Remove(socketPath)
		return nil, fmt.Errorf("secure socket: %w", err)
	}
	return ln, nil
}

func monitorIdle(done <-chan struct{}, shutdown func(), platform *LocalPlatform, tracker *activityTracker, idleSeconds int) {
	if idleSeconds <= 0 {
		return
	}
	idle := time.Duration(idleSeconds) * time.Second
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			if hasActiveSessions(platform) {
				continue
			}
			if time.Since(tracker.last()) >= idle {
				shutdown()
				return
			}
		}
	}
}

func hasActiveSessions(platform *LocalPlatform) bool {
	sessions, err := platform.List()
	if err != nil {
		return false
	}
	for _, s := range sessions {
		if !s.Status.IsTerminal() {
			return true
		}
	}
	return false
}

// activityTracker records the time of the most recent HTTP request so the
// idle monitor can decide when to shut the daemon down.
type activityTracker struct {
	mu   sync.Mutex
	when time.Time
}

func newActivityTracker() *activityTracker {
	return &activityTracker{when: time.Now()}
}

func (t *activityTracker) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.mu.Lock()
		t.when = time.Now()
		t.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

func (t *activityTracker) last() time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.when
}

type pidFile struct {
	PID           int    `json:"pid"`
	WorkspaceRoot string `json:"workspace_root"`
	Socket        string `json:"socket"`
}

func writePIDFile(path, workspaceRoot, socket string) error {
	data, err := json.MarshalIndent(pidFile{
		PID:           os.Getpid(),
		WorkspaceRoot: workspaceRoot,
		Socket:        socket,
	}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func removePIDFile(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var pf pidFile
	if json.Unmarshal(data, &pf) == nil && pf.PID == os.Getpid() {
		os.Remove(path)
	}
}

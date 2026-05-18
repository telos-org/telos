package telosd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/telos-org/telos-go/internal/sessionapi"
)

func Run(ctx context.Context, cfg Config) error {
	cfg, err := NormalizeConfig(cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(SessionsRoot(cfg.Root), 0o755); err != nil {
		return fmt.Errorf("create sessions root: %w", err)
	}

	store := storeForConfig(cfg)
	mux := http.NewServeMux()
	sessionapi.RegisterRoutes(mux, store)

	var lastRequest atomic.Int64
	lastRequest.Store(time.Now().UnixNano())
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastRequest.Store(time.Now().UnixNano())
		mux.ServeHTTP(w, r)
	})
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ln, cleanup, err := listen(cfg)
	if err != nil {
		return err
	}
	defer cleanup()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	if cfg.Mode == ModeLocal && cfg.Server.IdleSeconds > 0 {
		go idleShutdown(ctx, srv, store, &lastRequest, time.Duration(cfg.Server.IdleSeconds)*time.Second)
	}

	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

func storeForConfig(cfg Config) *sessionapi.FileStore {
	if cfg.Mode == ModeCloud {
		return sessionapi.NewFileStore(SessionsRoot(cfg.Root), sessionapi.RuntimeCloud)
	}
	return sessionapi.NewFileStore(SessionsRoot(cfg.Root), sessionapi.RuntimeLocal)
}

func listen(cfg Config) (net.Listener, func(), error) {
	switch cfg.Server.Transport {
	case "http":
		ln, err := net.Listen("tcp", cfg.Server.Listen)
		if err != nil {
			return nil, func() {}, fmt.Errorf("listen: %w", err)
		}
		return ln, func() { _ = ln.Close() }, nil
	case "unix":
		if err := os.MkdirAll(filepath.Dir(cfg.Server.Socket), 0o755); err != nil {
			return nil, func() {}, fmt.Errorf("create socket dir: %w", err)
		}
		_ = os.Remove(cfg.Server.Socket)
		ln, err := net.Listen("unix", cfg.Server.Socket)
		if err != nil {
			return nil, func() {}, fmt.Errorf("listen: %w", err)
		}
		if err := os.Chmod(cfg.Server.Socket, 0o600); err != nil {
			_ = ln.Close()
			_ = os.Remove(cfg.Server.Socket)
			return nil, func() {}, fmt.Errorf("chmod socket: %w", err)
		}
		return ln, func() {
			_ = ln.Close()
			_ = os.Remove(cfg.Server.Socket)
		}, nil
	default:
		return nil, func() {}, fmt.Errorf("invalid server.transport %q", cfg.Server.Transport)
	}
}

func idleShutdown(ctx context.Context, srv *http.Server, store *sessionapi.FileStore, lastRequest *atomic.Int64, idle time.Duration) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if hasActiveSessions(store) {
			continue
		}
		last := time.Unix(0, lastRequest.Load())
		if time.Since(last) < idle {
			continue
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = srv.Shutdown(shutdownCtx)
		cancel()
		return
	}
}

func hasActiveSessions(store *sessionapi.FileStore) bool {
	sessions, err := store.List()
	if err != nil {
		return false
	}
	for _, session := range sessions {
		if !session.Status.IsTerminal() {
			return true
		}
	}
	return false
}

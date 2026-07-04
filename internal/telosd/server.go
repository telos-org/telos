package telosd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"github.com/telos-org/telos/internal/sessionapi"
)

// Allow the bare apex usetelos.ai as well as any *.usetelos.ai subdomain. The
// dashboard is served from the apex, so an origin with no subdomain label must
// pass — `(.*\.)?` makes the subdomain optional (`usetelos.ai` and
// `app.usetelos.ai` both match).
var cloudAllowedOrigin = regexp.MustCompile(`^https://(.*\.)?usetelos\.ai$|^http://localhost(:[0-9]+)?$|^http://127\.0\.0\.1(:[0-9]+)?$`)

func Run(ctx context.Context, cfg Config) error {
	cfg, err := NormalizeConfig(cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(SessionsRoot(cfg.Root), 0o755); err != nil {
		return fmt.Errorf("create sessions root: %w", err)
	}

	baseStore := storeForConfig(cfg)
	store := sessionapi.Store(baseStore)
	if cfg.Mode == ModeCloud {
		substrate, err := newKubernetesSubstrate(cfg)
		if err != nil {
			return err
		}
		scrubCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if err := substrate.scrubManagedAgentSecrets(scrubCtx); err != nil {
			cancel()
			return fmt.Errorf("scrub managed agent secrets: %w", err)
		}
		cancel()
		startRouteReconciler(ctx, substrate.client)
		store = newCloudSessionStore(baseStore, newRouteHandleResolver(), substrate)
		startDeploymentBootstrapper(ctx, store)
		startControlSessionReconciler(ctx, store, cfg)
	}
	mux := http.NewServeMux()
	authorizer := authorizerForConfig(cfg, baseStore)
	sessionapi.RegisterRoutes(mux, store, authorizer)
	if cfg.Mode == ModeCloud {
		registerTopologyRoutes(mux, authorizer)
	}

	var lastRequest atomic.Int64
	lastRequest.Store(time.Now().UnixNano())
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastRequest.Store(time.Now().UnixNano())
		mux.ServeHTTP(w, r)
	})
	if cfg.Mode == ModeCloud {
		handler = withCloudCORS(handler)
	}
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	conns := newServerConnTracker()
	srv.ConnState = conns.ConnState

	ln, cleanup, err := listen(cfg)
	if err != nil {
		return err
	}
	defer cleanup()

	go func() {
		<-ctx.Done()
		_ = shutdownHTTPServer(srv, conns, 10*time.Second)
	}()
	if cfg.Mode == ModeLocal && cfg.Server.IdleSeconds > 0 {
		go idleShutdown(ctx, srv, baseStore, &lastRequest, time.Duration(cfg.Server.IdleSeconds)*time.Second)
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

func authorizerForConfig(cfg Config, store *sessionapi.FileStore) sessionapi.Authorizer {
	if cfg.Auth.Type == AuthBearer {
		return sessionapi.NewBearerAuthorizer(store, cfg.Auth.Token)
	}
	return sessionapi.AllowAllAuthorizer{}
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

func withCloudCORS(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); cloudAllowedOrigin.MatchString(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept, X-Telos-User-Authorization, X-Telos-Org-Id")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, OPTIONS")
			w.Header().Add("Vary", "Origin")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
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
		_ = shutdownHTTPServer(srv, nil, 10*time.Second)
		return
	}
}

type serverConnTracker struct {
	mu    sync.Mutex
	conns map[net.Conn]http.ConnState
}

func newServerConnTracker() *serverConnTracker {
	return &serverConnTracker{conns: map[net.Conn]http.ConnState{}}
}

func (t *serverConnTracker) ConnState(conn net.Conn, state http.ConnState) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	switch state {
	case http.StateClosed, http.StateHijacked:
		delete(t.conns, conn)
	default:
		t.conns[conn] = state
	}
}

func (t *serverConnTracker) closeAll() {
	if t == nil {
		return
	}
	t.mu.Lock()
	conns := make([]net.Conn, 0, len(t.conns))
	for conn := range t.conns {
		conns = append(conns, conn)
	}
	t.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
}

func shutdownHTTPServer(srv *http.Server, conns *serverConnTracker, grace time.Duration) error {
	if srv == nil {
		return nil
	}
	if grace <= 0 {
		conns.closeAll()
		return srv.Close()
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	err := srv.Shutdown(shutdownCtx)
	if err == nil {
		return nil
	}
	conns.closeAll()
	closeErr := srv.Close()
	if closeErr != nil && !errors.Is(closeErr, http.ErrServerClosed) {
		return errors.Join(err, closeErr)
	}
	return err
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

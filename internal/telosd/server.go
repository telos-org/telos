package telosd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
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
		substrate, err := newSessionSubstrate(cfg)
		if err != nil {
			return err
		}
		materializer := newApplyPackageMaterializer(baseStore.PackageRoot, cfg.Auth.Token)
		cloudStore := newCloudSessionStore(baseStore, substrate, materializer)
		store = cloudStore
		if err := cloudStore.ensureRootWorkers("server_started"); err != nil {
			log.Printf("ensure root workers: %v", err)
		}
		startSessionBootstrapReconciler(ctx, store, materializer)
	}
	mux := http.NewServeMux()
	authorizer := authorizerForConfig(cfg, baseStore)
	sessionapi.RegisterRoutes(mux, store, authorizer)

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
		go idleShutdown(ctx, srv, baseStore, &lastRequest, time.Duration(cfg.Server.IdleSeconds)*time.Second)
	}

	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

func storeForConfig(cfg Config) *sessionapi.FileStore {
	if cfg.Mode == ModeCloud {
		store := sessionapi.NewFileStore(SessionsRoot(cfg.Root), sessionapi.RuntimeCloud)
		store.PackageRoot = os.Getenv("TELOS_PACKAGE_ROOT")
		return store
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
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept")
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

package telosd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"time"

	"github.com/telos-org/telos/internal/sessionapi"
)

// Allow the bare apex usetelos.ai as well as any *.usetelos.ai subdomain. The
// dashboard is served from the apex, so an origin with no subdomain label must
// pass — `(.*\.)?` makes the subdomain optional (`usetelos.ai` and
// `app.usetelos.ai` both match).
var cloudAllowedOrigin = regexp.MustCompile(`^https://(.*\.)?usetelos\.ai$|^http://localhost(:[0-9]+)?$|^http://127\.0\.0\.1(:[0-9]+)?$`)

const rootWorkerReconcileInterval = 5 * time.Second

func Run(ctx context.Context, cfg Config, runtime sessionapi.RuntimeIdentity) error {
	cfg, err := NormalizeConfig(cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(SessionsRoot(cfg.Root), 0o755); err != nil {
		return fmt.Errorf("create sessions root: %w", err)
	}

	baseStore := sessionapi.NewFileStore(SessionsRoot(cfg.Root), sessionapi.RuntimeCloud)
	baseStore.PackageRoot = os.Getenv("TELOS_PACKAGE_ROOT")
	substrate, err := newSessionSubstrate(cfg)
	if err != nil {
		return err
	}
	materializer := newApplyPackageMaterializer(baseStore.PackageRoot, cfg.Auth.Token)
	if err := installPlatformSkills(ctx, materializer); err != nil {
		return err
	}
	reconciler := newControllerReconciler(baseStore, substrate, materializer, cloudControllerDefaults())
	if err := reconciler.ensureRootWorkers("server_started"); err != nil {
		log.Printf("ensure root workers: %v", err)
	}
	startRootWorkerReconciler(ctx, reconciler)
	startSessionBootstrapReconciler(ctx, reconciler, materializer)

	mux := http.NewServeMux()
	authorizer := sessionapi.NewBearerAuthorizer(baseStore, cfg.Auth.Token)
	sessionapi.RegisterRoutes(mux, reconciler, authorizer, runtime)
	srv := &http.Server{
		Handler:           withCloudCORS(mux.ServeHTTP),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ln, err := net.Listen("tcp", cfg.Server.Listen)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer ln.Close()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

func startRootWorkerReconciler(ctx context.Context, reconciler *controllerReconciler) {
	go func() {
		ticker := time.NewTicker(rootWorkerReconcileInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := reconciler.ensureRootWorkers("worker_supervision"); err != nil {
					log.Printf("ensure root workers: %v", err)
				}
			}
		}
	}()
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

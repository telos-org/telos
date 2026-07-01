package telosd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/telos-org/telos/internal/sessionapi"
)

type desiredSession struct {
	Name          string `json:"name"`
	PackageDigest string `json:"package_digest"`
	DesiredState  string `json:"desired_state"`
}

type desiredSessionsResponse struct {
	Sessions []desiredSession `json:"sessions"`
}

type controlSessionReconciler struct {
	apiURL      string
	envID       string
	token       string
	packageRoot string
	client      *http.Client
	store       sessionapi.Store
}

func startControlSessionReconciler(ctx context.Context, store sessionapi.Store, cfg Config) {
	if os.Getenv("TELOS_CONTROL_RECONCILER_ENABLED") == "0" {
		return
	}
	r, ok := newControlSessionReconciler(store, cfg)
	if !ok {
		return
	}

	interval := time.Duration(envInt("TELOS_CONTROL_RECONCILER_INTERVAL", 10)) * time.Second
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			if err := r.reconcile(ctx); err != nil {
				log.Printf("control session reconcile failed: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func newControlSessionReconciler(store sessionapi.Store, cfg Config) (controlSessionReconciler, bool) {
	r := controlSessionReconciler{
		apiURL:      controlReconcilerAPIURL(cfg),
		envID:       strings.TrimSpace(cfg.ControlPlane.EnvID),
		token:       strings.TrimSpace(cfg.ControlPlane.Token),
		packageRoot: strings.TrimSpace(os.Getenv("TELOS_PACKAGE_ROOT")),
		client:      &http.Client{Timeout: 10 * time.Second},
		store:       store,
	}
	if r.apiURL == "" || r.envID == "" || r.token == "" || r.packageRoot == "" {
		return controlSessionReconciler{}, false
	}
	return r, true
}

func controlReconcilerAPIURL(cfg Config) string {
	return strings.TrimRight(strings.TrimSpace(cfg.ControlPlane.Endpoint), "/")
}

func (r controlSessionReconciler) reconcile(ctx context.Context) error {
	desired, err := r.fetchDesired(ctx)
	if err != nil {
		return err
	}
	current, err := r.store.List()
	if err != nil {
		return fmt.Errorf("list local sessions: %w", err)
	}
	active := activeRootSessionsByName(current)
	for _, session := range desired {
		if strings.TrimSpace(session.DesiredState) != "running" {
			continue
		}
		name := strings.TrimSpace(session.Name)
		digest := strings.TrimSpace(session.PackageDigest)
		if name == "" || digest == "" {
			continue
		}
		if existing, ok := active[name]; ok {
			if sessionPackageDigest(existing) == digest {
				continue
			}
			if _, err := r.store.Stop(existing.SessionID); err != nil {
				return fmt.Errorf("stop stale session %s: %w", existing.SessionID, err)
			}
		}
		packagePath, err := packagePathForDigest(r.packageRoot, digest)
		if err != nil {
			return err
		}
		if _, err := os.Stat(packagePath); err != nil {
			return fmt.Errorf("package %s is not materialized at %s: %w", digest, packagePath, err)
		}
		kind := sessionapi.KindController
		if _, err := r.store.Create(sessionapi.SessionCreateRequest{
			ApplyPackagePath:   packagePath,
			ApplyPackageDigest: digest,
			SessionKind:        &kind,
		}); err != nil {
			return fmt.Errorf("create session %s from package %s: %w", name, digest, err)
		}
		current, err = r.store.List()
		if err != nil {
			return fmt.Errorf("list local sessions after create: %w", err)
		}
		active = activeRootSessionsByName(current)
	}
	return nil
}

func (r controlSessionReconciler) fetchDesired(ctx context.Context) ([]desiredSession, error) {
	u := r.apiURL + "/api/environments/" + url.PathEscape(r.envID) + "/sessions"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch desired sessions: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("fetch desired sessions: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out desiredSessionsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode desired sessions: %w", err)
	}
	return out.Sessions, nil
}

func activeRootSessionsByName(sessions []sessionapi.Session) map[string]sessionapi.Session {
	out := map[string]sessionapi.Session{}
	for _, session := range sessions {
		if session.ParentSessionID != nil && *session.ParentSessionID != "" {
			continue
		}
		if session.SpecName == nil || *session.SpecName == "" {
			continue
		}
		if session.Status.IsTerminal() {
			continue
		}
		out[*session.SpecName] = session
	}
	return out
}

func sessionPackageDigest(session sessionapi.Session) string {
	for i := len(session.SpecVersions) - 1; i >= 0; i-- {
		if digest, ok := session.SpecVersions[i]["apply_package_digest"].(string); ok {
			return strings.TrimSpace(digest)
		}
	}
	return ""
}

func packagePathForDigest(root string, digest string) (string, error) {
	digest = strings.TrimSpace(digest)
	hex, ok := strings.CutPrefix(digest, "sha256:")
	if !ok || len(hex) != 64 {
		return "", fmt.Errorf("invalid package digest %q", digest)
	}
	return filepath.Join(root, "blobs", "sha256", hex, "package.tar.gz"), nil
}

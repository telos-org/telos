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
	DeploymentID  string `json:"deployment_id,omitempty"`
	Name          string `json:"name"`
	PackageDigest string `json:"package_digest"`
	DesiredState  string `json:"desired_state"`
}

type desiredSessionsResponse struct {
	Sessions []desiredSession `json:"sessions"`
}

const defaultCloudSessionModel = "sail-research/zai-org/GLM-5.2-FP8"

type controlSessionReconciler struct {
	apiURL      string
	envID       string
	token       string
	packageRoot string
	model       string
	client      *http.Client
	store       sessionapi.Store
}

func startControlSessionReconciler(ctx context.Context, store sessionapi.Store, cfg Config) {
	if os.Getenv("TELOS_CONTROL_RECONCILER_ENABLED") == "0" {
		return
	}
	startDeploymentBootstrapReconciler(ctx, store)
	r := controlSessionReconciler{
		apiURL:      strings.TrimRight(strings.TrimSpace(os.Getenv("TELOS_CONTROL_API_URL")), "/"),
		envID:       strings.TrimSpace(os.Getenv("TELOS_ENV_ID")),
		token:       strings.TrimSpace(cfg.Auth.Token),
		packageRoot: strings.TrimSpace(os.Getenv("TELOS_PACKAGE_ROOT")),
		model:       cloudSessionModel(),
		client:      &http.Client{Timeout: 10 * time.Second},
		store:       store,
	}
	if r.apiURL == "" || r.envID == "" || r.token == "" || r.packageRoot == "" {
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

func startDeploymentBootstrapReconciler(ctx context.Context, store sessionapi.Store) {
	desired, ok := deploymentBootstrapDesiredSession()
	if !ok {
		return
	}
	r := controlSessionReconciler{
		packageRoot: strings.TrimSpace(os.Getenv("TELOS_PACKAGE_ROOT")),
		model:       cloudSessionModel(),
		store:       store,
	}
	if r.packageRoot == "" {
		return
	}

	interval := time.Duration(envInt("TELOS_CONTROL_RECONCILER_INTERVAL", 10)) * time.Second
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			if err := r.reconcileDesired([]desiredSession{desired}); err != nil {
				log.Printf("deployment bootstrap reconcile failed: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func deploymentBootstrapDesiredSession() (desiredSession, bool) {
	deploymentID := strings.TrimSpace(os.Getenv("TELOS_DEPLOYMENT_ID"))
	name := strings.TrimSpace(os.Getenv("TELOS_DEPLOYMENT_NAME"))
	digest := strings.TrimSpace(os.Getenv("TELOS_PACKAGE_DIGEST"))
	if name == "" || digest == "" {
		return desiredSession{}, false
	}
	return desiredSession{
		DeploymentID:  deploymentID,
		Name:          name,
		PackageDigest: digest,
		DesiredState:  "running",
	}, true
}

func (r controlSessionReconciler) reconcile(ctx context.Context) error {
	desired, err := r.fetchDesired(ctx)
	if err != nil {
		return err
	}
	return r.reconcileDesired(desired)
}

func (r controlSessionReconciler) reconcileDesired(desired []desiredSession) error {
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
		model := strings.TrimSpace(r.model)
		if model == "" {
			model = cloudSessionModel()
		}
		if _, err := r.store.Create(sessionapi.SessionCreateRequest{
			ApplyPackagePath:   packagePath,
			ApplyPackageDigest: digest,
			DeploymentID:       strings.TrimSpace(session.DeploymentID),
			DeploymentName:     name,
			SessionKind:        &kind,
			Model:              model,
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

func cloudSessionModel() string {
	if model := strings.TrimSpace(os.Getenv("TELOS_CLOUD_DEFAULT_MODEL")); model != "" {
		return model
	}
	return defaultCloudSessionModel
}

func activeRootSessionsByName(sessions []sessionapi.Session) map[string]sessionapi.Session {
	out := map[string]sessionapi.Session{}
	for _, session := range sessions {
		if session.ParentSessionID != nil && *session.ParentSessionID != "" {
			continue
		}
		if session.Status.IsTerminal() {
			continue
		}
		if name := sessionDeploymentName(session); name != "" {
			out[name] = session
			continue
		}
		if session.SpecName == nil || *session.SpecName == "" {
			continue
		}
		out[*session.SpecName] = session
	}
	return out
}

func sessionDeploymentName(session sessionapi.Session) string {
	if session.Provenance == nil {
		return ""
	}
	name, _ := session.Provenance["deployment_name"].(string)
	return strings.TrimSpace(name)
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

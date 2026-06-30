package telosd

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/telos-org/telos/internal/sessionapi"
)

type deploymentSession struct {
	DeploymentID  string `json:"deployment_id,omitempty"`
	Name          string `json:"name"`
	PackageDigest string `json:"package_digest"`
}

const defaultCloudSessionModel = "sail-research/zai-org/GLM-5.2-FP8"

type deploymentBootstrapReconciler struct {
	packageRoot string
	model       string
	store       sessionapi.Store
}

func startDeploymentBootstrapReconciler(ctx context.Context, store sessionapi.Store) {
	if os.Getenv("TELOS_DEPLOYMENT_BOOTSTRAP_ENABLED") == "0" {
		return
	}
	session, ok := deploymentBootstrapSession()
	if !ok {
		return
	}
	r := deploymentBootstrapReconciler{
		packageRoot: strings.TrimSpace(os.Getenv("TELOS_PACKAGE_ROOT")),
		model:       cloudSessionModel(),
		store:       store,
	}
	if r.packageRoot == "" {
		return
	}

	interval := time.Duration(envInt("TELOS_DEPLOYMENT_BOOTSTRAP_INTERVAL", 10)) * time.Second
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			if err := r.reconcile([]deploymentSession{session}); err != nil {
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

func deploymentBootstrapSession() (deploymentSession, bool) {
	deploymentID := strings.TrimSpace(os.Getenv("TELOS_DEPLOYMENT_ID"))
	name := strings.TrimSpace(os.Getenv("TELOS_DEPLOYMENT_NAME"))
	digest := strings.TrimSpace(os.Getenv("TELOS_PACKAGE_DIGEST"))
	if name == "" || digest == "" {
		return deploymentSession{}, false
	}
	return deploymentSession{
		DeploymentID:  deploymentID,
		Name:          name,
		PackageDigest: digest,
	}, true
}

func (r deploymentBootstrapReconciler) reconcile(sessions []deploymentSession) error {
	current, err := r.store.List()
	if err != nil {
		return fmt.Errorf("list local sessions: %w", err)
	}
	active := activeRootSessionsByName(current)
	for _, session := range sessions {
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
		packagePath, err := sessionapi.PackagePathForDigest(r.packageRoot, digest)
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

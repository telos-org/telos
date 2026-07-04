package telosd

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/telos-org/telos/internal/sessionapi"
)

type cloudBootstrapSession struct {
	CloudSessionID string `json:"cloud_session_id,omitempty"`
	Name           string `json:"name"`
	PackageDigest  string `json:"package_digest"`
}

const (
	defaultCloudSessionModel    = "sail-research/zai-org/GLM-5.2-FP8"
	defaultCloudSessionThinking = "medium"
	defaultCloudAgentTimeoutSec = 900
	cloudAgentTimeoutEnvVar     = "TELOS_AGENT_TIMEOUT_SEC"
)

type sessionBootstrapReconciler struct {
	packageRoot     string
	model           string
	thinking        string
	agentTimeoutSec *int
	store           sessionapi.Store
}

func startSessionBootstrapReconciler(ctx context.Context, store sessionapi.Store) {
	if os.Getenv("TELOS_SESSION_BOOTSTRAP_ENABLED") == "0" {
		return
	}
	session, ok := bootstrapSessionFromEnv()
	if !ok {
		return
	}
	r := sessionBootstrapReconciler{
		packageRoot:     strings.TrimSpace(os.Getenv("TELOS_PACKAGE_ROOT")),
		model:           cloudSessionModel(),
		thinking:        cloudSessionThinking(),
		agentTimeoutSec: intPtr(cloudAgentTimeoutSec()),
		store:           store,
	}
	if r.packageRoot == "" {
		return
	}

	interval := time.Duration(envInt("TELOS_SESSION_BOOTSTRAP_INTERVAL", 10)) * time.Second
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			if err := r.reconcile([]cloudBootstrapSession{session}); err != nil {
				log.Printf("session bootstrap reconcile failed: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func bootstrapSessionFromEnv() (cloudBootstrapSession, bool) {
	cloudSessionID := strings.TrimSpace(os.Getenv("TELOS_SESSION_ID"))
	name := strings.TrimSpace(os.Getenv("TELOS_SESSION_NAME"))
	digest := strings.TrimSpace(os.Getenv("TELOS_PACKAGE_DIGEST"))
	if name == "" || digest == "" {
		return cloudBootstrapSession{}, false
	}
	return cloudBootstrapSession{
		CloudSessionID: cloudSessionID,
		Name:           name,
		PackageDigest:  digest,
	}, true
}

func (r sessionBootstrapReconciler) reconcile(sessions []cloudBootstrapSession) error {
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
		thinking := strings.TrimSpace(r.thinking)
		if thinking == "" {
			thinking = cloudSessionThinking()
		}
		agentTimeoutSec := cloudAgentTimeoutSec()
		if r.agentTimeoutSec != nil {
			agentTimeoutSec = *r.agentTimeoutSec
		}
		if _, err := r.store.Create(sessionapi.SessionCreateRequest{
			ApplyPackagePath:   packagePath,
			ApplyPackageDigest: digest,
			CloudSessionID:     strings.TrimSpace(session.CloudSessionID),
			CloudSessionName:   name,
			SessionKind:        &kind,
			Model:              model,
			Thinking:           thinking,
			AgentTimeoutSec:    intPtr(agentTimeoutSec),
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

func cloudSessionThinking() string {
	if thinking := strings.TrimSpace(os.Getenv("TELOS_CLOUD_DEFAULT_THINKING")); thinking != "" {
		return thinking
	}
	return defaultCloudSessionThinking
}

func cloudAgentTimeoutSec() int {
	return envNonNegativeInt(cloudAgentTimeoutEnvVar, defaultCloudAgentTimeoutSec)
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
		if name := sessionCloudName(session); name != "" {
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

func sessionCloudName(session sessionapi.Session) string {
	if session.Provenance == nil {
		return ""
	}
	name, _ := session.Provenance["cloud_session_name"].(string)
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

func envInt(name string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func envNonNegativeInt(name string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return fallback
	}
	return value
}

func intPtr(value int) *int {
	return &value
}

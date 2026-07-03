package telosd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/telos-org/telos/internal/sessionapi"
)

func TestWorkerIntervalReadsSessionManifest(t *testing.T) {
	sessionDir := writeWorkerManifest(t, map[string]any{
		"session_kind": "controller",
		"specs": []map[string]any{{
			"interval_seconds": 12,
		}},
	})

	manifest, err := LoadWorkerManifest(sessionDir)
	if err != nil {
		t.Fatalf("LoadWorkerManifest: %v", err)
	}
	if got := manifest.Kind; got != "controller" {
		t.Fatalf("kind: got %q", got)
	}
	if got := manifest.Interval; got != 12*time.Second {
		t.Fatalf("interval: got %s", got)
	}
}

func TestWorkerManifestRejectsMalformedManifest(t *testing.T) {
	sessionDir := filepath.Join(t.TempDir(), "sess_bad")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "session.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadWorkerManifest(sessionDir); err == nil {
		t.Fatal("expected malformed manifest to fail")
	}
}

func TestWorkerManifestRejectsMissingSessionKind(t *testing.T) {
	sessionDir := writeWorkerManifest(t, map[string]any{"specs": []any{}})

	if _, err := LoadWorkerManifest(sessionDir); err == nil {
		t.Fatal("expected missing session_kind to fail")
	}
}

func TestRootWorkerAllowsNoInterval(t *testing.T) {
	sessionDir := writeWorkerManifest(t, map[string]any{
		"session_kind": "controller",
		"specs": []map[string]any{{
			"name": "demo",
		}},
	})

	manifest, err := LoadWorkerManifest(sessionDir)
	if err != nil {
		t.Fatalf("LoadWorkerManifest: %v", err)
	}
	if got := manifest.Kind; got != "controller" {
		t.Fatalf("kind: got %q", got)
	}
	if manifest.Interval != 0 {
		t.Fatalf("interval: got %s", manifest.Interval)
	}
}

func TestWorkerBillingConfigFromEnvDoesNotRequireOperatorAuth(t *testing.T) {
	t.Setenv("TELOS_API_TOKEN", "")
	t.Setenv("TELOS_ENV_ID", "env_test")
	t.Setenv("TELOS_BILLING_ENDPOINT", "https://billing.example.com")
	t.Setenv("TELOS_BILLING_ENV_TOKEN", "billing-token")

	cfg := workerBillingConfigFromEnv()
	if cfg.Endpoint != "https://billing.example.com" || cfg.EnvID != "env_test" || cfg.Token != "billing-token" {
		t.Fatalf("billing config: %+v", cfg)
	}
}

func TestReconcileWorkerBillingSkipsWithoutManagedGateway(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	t.Setenv("TELOS_ENV_ID", "env_test")
	t.Setenv("TELOS_BILLING_ENDPOINT", server.URL)
	t.Setenv("TELOS_BILLING_ENV_TOKEN", "billing-token")
	t.Setenv("TELOS_GATEWAY_MODE", "")

	reconcileWorkerBilling(t.TempDir(), WorkerManifest{
		SessionID: "sess_cloud",
		Runtime:   sessionapi.RuntimeCloud,
	}, true)

	if called {
		t.Fatal("billing reconcile should not run outside managed gateway mode")
	}
}

func TestRunSessionWorkerReconcilesTerminalBillingAfterLocalRunError(t *testing.T) {
	sessionDir := writeWorkerManifest(t, map[string]any{
		"session_id":    "sess_cloud",
		"session_kind":  "task",
		"runtime":       "cloud",
		"specs":         []any{},
		"config":        map[string]any{},
		"workspace":     map[string]any{},
		"provenance":    map[string]any{},
		"epochs":        []any{},
		"events":        []any{},
		"session_state": map[string]any{},
	})
	gotReconcile := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/billing/reconcile/sess_cloud" || r.URL.RawQuery != "terminal=true" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer billing-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		gotReconcile = true
		_ = json.NewEncoder(w).Encode(map[string]any{"session_id": "sess_cloud"})
	}))
	defer server.Close()

	t.Setenv("TELOS_API_TOKEN", "")
	t.Setenv("TELOS_ENV_ID", "env_test")
	t.Setenv("TELOS_BILLING_ENDPOINT", server.URL)
	t.Setenv("TELOS_BILLING_ENV_TOKEN", "billing-token")
	t.Setenv("TELOS_GATEWAY_MODE", "managed")

	code, err := RunSessionWorker(sessionDir, false)
	if err == nil || code != 1 {
		t.Fatalf("RunSessionWorker should fail local run: code=%d err=%v", code, err)
	}
	if !gotReconcile {
		t.Fatal("missing terminal billing reconcile after local run error")
	}
}

func writeWorkerManifest(t *testing.T, data map[string]any) string {
	t.Helper()
	sessionDir := filepath.Join(t.TempDir(), "sess_controller")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "session.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return sessionDir
}

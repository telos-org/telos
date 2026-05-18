package telosd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestControllerWorkerRequiresPositiveIntervalUnlessOnce(t *testing.T) {
	sessionDir := writeWorkerManifest(t, map[string]any{
		"session_kind": "controller",
		"specs": []map[string]any{{
			"name": "demo",
		}},
	})

	code, err := RunSessionWorker(sessionDir, false)
	if err == nil {
		t.Fatal("expected controller without interval to fail before running")
	}
	if code != 1 {
		t.Fatalf("code: got %d", code)
	}
	if !strings.Contains(err.Error(), "no positive interval_seconds") {
		t.Fatalf("unexpected error: %v", err)
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

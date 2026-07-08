package telosd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
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

func TestWorkerManifestReadsDesiredState(t *testing.T) {
	version := 7
	sessionDir := writeWorkerManifest(t, map[string]any{
		"session_kind":         "controller",
		"current_spec_version": version,
		"package_digest":       "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"specs": []map[string]any{{
			"name": "demo",
		}},
	})

	manifest, err := LoadWorkerManifest(sessionDir)
	if err != nil {
		t.Fatalf("LoadWorkerManifest: %v", err)
	}
	if manifest.Desired.SpecVersion != version {
		t.Fatalf("spec version: got %d", manifest.Desired.SpecVersion)
	}
	if manifest.Desired.PackageDigest != "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("package digest: got %q", manifest.Desired.PackageDigest)
	}
}

func TestDesiredStateIncludesSpecVersion(t *testing.T) {
	before := DesiredState{SpecVersion: 1, PackageDigest: "sha256:same"}
	after := DesiredState{SpecVersion: 2, PackageDigest: "sha256:same"}

	if before.Equal(after) {
		t.Fatal("desired state should change when only spec version changes")
	}
}

func TestDrainWakeClearsBufferedWakeSignals(t *testing.T) {
	wake := make(chan os.Signal, 2)
	wake <- syscall.SIGUSR1
	wake <- syscall.SIGUSR1

	drainWake(wake)

	select {
	case signal := <-wake:
		t.Fatalf("unexpected buffered wake after drain: %v", signal)
	default:
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

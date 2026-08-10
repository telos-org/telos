package telosd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

func TestLoadCompletedEpochDesiredUsesBoundIdentity(t *testing.T) {
	sessionDir := writeWorkerManifest(t, map[string]any{
		"session_kind":         "controller",
		"current_spec_version": 8,
		"package_digest":       "sha256:current",
		"specs":                []map[string]any{{"name": "demo"}},
		"epochs": []map[string]any{{
			"id":             4,
			"started_at":     "2026-08-14T12:00:00.000Z",
			"finished_at":    "2026-08-14T12:01:00.000Z",
			"result":         "completed",
			"spec_version":   7,
			"package_digest": "sha256:completed",
		}},
	})

	desired, ok, err := LoadCompletedEpochDesired(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected bound completion identity")
	}
	if desired.SpecVersion != 7 || desired.PackageDigest != "sha256:completed" {
		t.Fatalf("unexpected desired identity: %#v", desired)
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

func TestStopRequestWinsBeforeImmediateDesiredStateCycle(t *testing.T) {
	stop := make(chan os.Signal, 1)
	stop <- syscall.SIGTERM
	if !stopRequested(stop) {
		t.Fatal("expected buffered retirement signal to stop the worker")
	}
}

func TestFailureBackoffReachesFifteenMinuteCap(t *testing.T) {
	for failures, want := range map[int]time.Duration{
		1:   time.Second,
		7:   64 * time.Second,
		11:  controllerFailureBackoffCap,
		100: controllerFailureBackoffCap,
	} {
		if got := failureBackoff(failures); got != want {
			t.Fatalf("failureBackoff(%d) = %s, want %s", failures, got, want)
		}
	}
}

func TestJitteredFailureBackoffStaysWithinBound(t *testing.T) {
	for failures := 1; failures <= 20; failures++ {
		base := failureBackoff(failures)
		for range 20 {
			got := jitteredFailureBackoff(failures)
			if got < base-base/5 || got > base {
				t.Fatalf("jitteredFailureBackoff(%d) = %s, base %s", failures, got, base)
			}
		}
	}
}

func TestLogControllerSuspendedWritesStructuredEvidence(t *testing.T) {
	evidencePath := filepath.Join(t.TempDir(), "evidence.jsonl")
	sessionDir := writeWorkerManifest(t, map[string]any{
		"session_id":   "sess_123",
		"session_kind": "controller",
		"created_at":   "2026-08-10T12:00:00Z",
		"specs": []map[string]any{{
			"name":          "demo",
			"evidence_path": evidencePath,
		}},
		"epochs": []map[string]any{{
			"id":         4,
			"started_at": "2026-08-10T12:00:00Z",
		}},
	})

	logControllerSuspended(sessionDir, "agent_authentication_invalid", "403: inactive virtual key")
	data, err := os.ReadFile(evidencePath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		`"event":"agent_suspended"`,
		`"epoch_id":4`,
		`"blocker_code":"agent_authentication_invalid"`,
		`"state":"waiting"`,
		"update the model credentials",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("suspension evidence missing %q:\n%s", want, text)
		}
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

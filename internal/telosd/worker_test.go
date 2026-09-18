package telosd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/telos-org/telos/internal/game"
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

	logControllerSuspended(sessionDir, "agent_authentication_invalid", "403: inactive virtual key", controllerCredentialRetryInterval)
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
		`"retry_after_seconds":300`,
		"update the model credentials",
		"retry automatically",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("suspension evidence missing %q:\n%s", want, text)
		}
	}
}

func TestControllerConfigurationFailureSuspendsUntilExplicitWake(t *testing.T) {
	evidencePath := filepath.Join(t.TempDir(), "evidence.jsonl")
	sessionDir := writeWorkerManifest(t, map[string]any{
		"session_id":   "sess_123",
		"session_kind": "controller",
		"specs": []map[string]any{{
			"name":          "demo",
			"evidence_path": evidencePath,
		}},
	})
	wake := make(chan os.Signal, 1)
	stop := make(chan os.Signal, 1)
	t.Cleanup(func() { stop <- syscall.SIGTERM })
	calls := make(chan int, 2)
	attempt := 0
	runSession := func(string) (*game.PVGResult, error) {
		attempt++
		calls <- attempt
		if attempt == 1 {
			return &game.PVGResult{
				GameResult: game.GameFailure,
				Error:      "unknown model",
			}, nil
		}
		return &game.PVGResult{GameResult: game.GameStopped}, nil
	}
	type workerResult struct {
		code int
		err  error
	}
	done := make(chan workerResult, 1)
	go func() {
		code, err := runSessionWorker(sessionDir, false, runSession, wake, stop)
		done <- workerResult{code: code, err: err}
	}()

	select {
	case got := <-calls:
		if got != 1 {
			t.Fatalf("first attempt = %d", got)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not run the first cycle")
	}
	select {
	case got := <-calls:
		t.Fatalf("worker retried without a wake signal: attempt %d", got)
	case result := <-done:
		t.Fatalf("worker exited while suspended: %#v", result)
	case <-time.After(failureBackoff(1) + 100*time.Millisecond):
	}

	wake <- syscall.SIGUSR1
	select {
	case got := <-calls:
		if got != 2 {
			t.Fatalf("attempt after wake = %d", got)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not resume after wake")
	}
	select {
	case result := <-done:
		if result.code != 0 || result.err != nil {
			t.Fatalf("worker result = %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not stop after resumed cycle")
	}
}

func TestControllerRetriesCredentialFailures(t *testing.T) {
	for _, providerError := range []string{"401: invalid x-api-key", "403: forbidden", "403: inactive virtual key"} {
		t.Run(providerError, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				sessionDir := writeWorkerManifest(t, map[string]any{
					"session_kind": "controller",
					"specs":        []map[string]any{{"name": "demo"}},
				})
				stop := make(chan os.Signal, 1)
				defer func() { stop <- syscall.SIGTERM }()
				var attempts atomic.Int32
				var keyFixed atomic.Bool
				done := make(chan struct{})
				go func() {
					defer close(done)
					code, err := runSessionWorker(sessionDir, false, func(string) (*game.PVGResult, error) {
						attempts.Add(1)
						if !keyFixed.Load() {
							return &game.PVGResult{GameResult: game.GameFailure, Error: providerError}, nil
						}
						return &game.PVGResult{GameResult: game.GameSuccess}, nil
					}, make(chan os.Signal), stop)
					if code != 0 || err != nil {
						t.Errorf("worker returned %d, %v", code, err)
					}
				}()
				synctest.Wait()
				if attempts.Load() != 1 {
					t.Fatalf("initial attempts = %d", attempts.Load())
				}
				time.Sleep(controllerCredentialRetryInterval - time.Second)
				synctest.Wait()
				if attempts.Load() != 1 {
					t.Fatalf("retried too soon: %d attempts", attempts.Load())
				}
				time.Sleep(time.Second)
				synctest.Wait()
				if attempts.Load() != 2 {
					t.Fatalf("expected one retry with the invalid key, got %d attempts", attempts.Load())
				}
				keyFixed.Store(true)
				time.Sleep(controllerCredentialRetryInterval)
				synctest.Wait()
				if attempts.Load() != 3 {
					t.Fatalf("did not recover without a wake: %d attempts", attempts.Load())
				}
				time.Sleep(2 * controllerCredentialRetryInterval)
				synctest.Wait()
				if attempts.Load() != 3 {
					t.Fatalf("retried after success with no interval: %d attempts", attempts.Load())
				}
				stop <- syscall.SIGTERM
				<-done
			})
		})
	}
}

func TestCredentialRetryRespectsWakeAndStop(t *testing.T) {
	for _, signal := range []os.Signal{syscall.SIGUSR1, syscall.SIGTERM} {
		t.Run(signal.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				sessionDir := writeWorkerManifest(t, map[string]any{
					"session_kind": "controller",
					"specs":        []map[string]any{{"name": "demo"}},
				})
				wake := make(chan os.Signal, 1)
				stop := make(chan os.Signal, 1)
				defer func() { stop <- syscall.SIGTERM }()
				var attempts atomic.Int32
				done := make(chan struct{})
				go func() {
					defer close(done)
					code, err := runSessionWorker(sessionDir, false, func(string) (*game.PVGResult, error) {
						attempts.Add(1)
						return &game.PVGResult{GameResult: game.GameFailure, Error: "401: unauthorized"}, nil
					}, wake, stop)
					if code != 0 || err != nil {
						t.Errorf("worker returned %d, %v", code, err)
					}
				}()
				synctest.Wait()
				if signal == syscall.SIGUSR1 {
					wake <- signal
				} else {
					stop <- signal
				}
				synctest.Wait()
				want := int32(1)
				if signal == syscall.SIGUSR1 {
					want = 2
					stop <- syscall.SIGTERM
				}
				<-done
				time.Sleep(2 * controllerCredentialRetryInterval)
				if attempts.Load() != want {
					t.Fatalf("attempts = %d, want %d", attempts.Load(), want)
				}
			})
		})
	}
}

func TestCredentialFailureDoesNotRetryBoundedWork(t *testing.T) {
	for _, kind := range []string{"task", "controller"} {
		t.Run(kind, func(t *testing.T) {
			sessionDir := writeWorkerManifest(t, map[string]any{
				"session_kind": kind,
				"specs":        []map[string]any{{"name": "demo"}},
			})
			attempts := 0
			_, _ = runSessionWorker(sessionDir, kind == "controller", func(string) (*game.PVGResult, error) {
				attempts++
				return &game.PVGResult{GameResult: game.GameFailure, Error: "401: unauthorized"}, nil
			}, make(chan os.Signal), make(chan os.Signal))
			if attempts != 1 {
				t.Fatalf("bounded work ran %d times", attempts)
			}
		})
	}
}

func TestStoppedWorkerDoesNotRetryWithPendingWake(t *testing.T) {
	sessionDir := writeWorkerManifest(t, map[string]any{"session_kind": "controller"})
	wake := make(chan os.Signal, 1)
	stop := make(chan os.Signal, 1)
	wake <- syscall.SIGUSR1
	stop <- syscall.SIGTERM
	code, err := runSessionWorker(sessionDir, false, func(string) (*game.PVGResult, error) {
		t.Fatal("worker ran with a pending stop")
		return nil, nil
	}, wake, stop)
	if code != 0 || err != nil {
		t.Fatalf("worker returned %d, %v", code, err)
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

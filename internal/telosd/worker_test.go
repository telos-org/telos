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
		if got := failureBackoff(failures, time.Second); got != want {
			t.Fatalf("failureBackoff(%d) = %s, want %s", failures, got, want)
		}
	}
}

func TestJitteredFailureBackoffStaysWithinBound(t *testing.T) {
	for failures := 1; failures <= 20; failures++ {
		base := failureBackoff(failures, time.Second)
		for range 20 {
			got := jitteredFailureBackoff(failures, time.Second)
			if got < base-base/5 || got > base {
				t.Fatalf("jitteredFailureBackoff(%d) = %s, base %s", failures, got, base)
			}
		}
	}
}

func TestRootFailureRecordsRetryingIssueAndBacksOff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sessionDir := writeWorkerManifest(t, map[string]any{
			"session_kind": "controller",
			"specs":        []map[string]any{{"name": "demo"}},
		})
		stop, wake := make(chan os.Signal, 1), make(chan os.Signal, 1)
		starts := make(chan time.Time, 1)
		var attempts atomic.Int32
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = runSessionWorker(sessionDir, false, func(string) (*game.PVGResult, error) {
				starts <- time.Now()
				if attempts.Add(1) == 5 {
					return &game.PVGResult{GameResult: game.GameSuccess}, nil
				}
				return &game.PVGResult{GameResult: game.GameFailure, Error: "agent_failure_budget_exceeded: 503: overloaded", ProviderError: "503: overloaded"}, nil
			}, wake, stop)
		}()
		first := <-starts
		previous := first
		for _, upper := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second} {
			next := <-starts
			if elapsed := next.Sub(previous); elapsed < upper*4/5 || elapsed > upper {
				t.Fatalf("retry after %v, want [%v, %v]", elapsed, upper*4/5, upper)
			}
			previous = next
		}
		synctest.Wait()
		issue := readIssue(t, sessionDir)
		if issue.Status != sessionapi.IssueRetrying || issue.Reason != sessionapi.ReasonInferenceProviderError ||
			issue.Message != "503: overloaded" || issue.Since != sessionapi.FormatTime(first) ||
			issue.LastRetryAt != sessionapi.FormatTime(previous) || issue.NextRetryAt <= issue.LastRetryAt {
			t.Fatalf("issue = %+v", issue)
		}

		<-starts // the fifth attempt succeeds
		wake <- syscall.SIGUSR1
		failed := <-starts
		if elapsed := (<-starts).Sub(failed); elapsed > time.Second {
			t.Fatalf("backoff did not reset after a success: %v", elapsed)
		}
		stop <- syscall.SIGTERM
		<-done
	})
}

func TestAnHourOfProviderErrorsBlocksTheRootUntilRecheck(t *testing.T) {
	evidencePath := filepath.Join(t.TempDir(), "evidence.jsonl")
	synctest.Test(t, func(t *testing.T) {
		sessionDir := writeWorkerManifest(t, map[string]any{
			"session_kind": "controller",
			"specs":        []map[string]any{{"name": "demo", "evidence_path": evidencePath}},
		})
		wake, stop := make(chan os.Signal, 1), make(chan os.Signal, 1)
		var attempts atomic.Int32
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = runSessionWorker(sessionDir, false, func(string) (*game.PVGResult, error) {
				attempts.Add(1)
				return &game.PVGResult{GameResult: game.GameFailure, Error: "agent_failure_budget_exceeded: 401: invalid api key", ProviderError: "401: invalid api key"}, nil
			}, wake, stop)
		}()
		time.Sleep(59 * time.Minute)
		synctest.Wait()
		if issue := readIssue(t, sessionDir); issue.Status != sessionapi.IssueRetrying {
			t.Fatalf("before an hour: %+v", issue)
		}
		time.Sleep(30 * time.Minute) // past the hour, and past the next retry
		synctest.Wait()
		issue := readIssue(t, sessionDir)
		if issue.Status != sessionapi.IssueBlocked || issue.Reason != sessionapi.ReasonInferenceProviderError ||
			!strings.Contains(issue.Message, "401: invalid api key") {
			t.Fatalf("after an hour: %+v", issue)
		}
		activity, _ := os.ReadFile(evidencePath)
		if !strings.Contains(string(activity), `"event":"goal_blocked"`) ||
			!strings.Contains(string(activity), `"reason":"INFERENCE_PROVIDER_ERROR"`) {
			t.Fatalf("the block is missing from the activity:\n%s", activity)
		}
		blockedAt := attempts.Load()
		time.Sleep(time.Hour)
		synctest.Wait()
		if attempts.Load() != blockedAt {
			t.Fatalf("blocked root kept trying: %d attempts after %d", attempts.Load(), blockedAt)
		}
		store := sessionapi.NewFileStore(filepath.Dir(sessionDir), sessionapi.RuntimeCloud)
		if _, err := store.Recheck(filepath.Base(sessionDir)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(holdRecheckInterval)
		synctest.Wait()
		if issue := readIssue(t, sessionDir); attempts.Load() <= blockedAt || issue.Status != sessionapi.IssueRetrying {
			t.Fatalf("recheck did not retry: %d attempts after %d, %+v", attempts.Load(), blockedAt, issue)
		}
		stop <- syscall.SIGTERM
		<-done
	})
}

func TestVerifierBlockHoldsRootUntilRecheck(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sessionDir := writeWorkerManifest(t, map[string]any{
			"session_kind": "controller",
			"specs":        []map[string]any{{"name": "demo"}},
		})
		wake, stop := make(chan os.Signal, 1), make(chan os.Signal, 1)
		var attempts atomic.Int32
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = runSessionWorker(sessionDir, false, func(string) (*game.PVGResult, error) {
				if attempts.Add(1) == 1 { // the game records its block, as cli.recordBlock does
					if _, err := sessionapi.MutateManifest(filepath.Join(sessionDir, "session.json"), func(m *sessionapi.Manifest) error {
						m.Issue = sessionapi.NewIssue(sessionapi.IssueBlocked, sessionapi.ReasonSpecUnsatisfiable, "The spec needs a key it never provides.", time.Now())
						return nil
					}); err != nil {
						t.Error(err)
					}
					return &game.PVGResult{GameResult: game.GameBlocked}, nil
				}
				return &game.PVGResult{GameResult: game.GameFailure, Error: "boom"}, nil
			}, wake, stop)
		}()
		synctest.Wait()
		time.Sleep(time.Hour)
		synctest.Wait()
		if issue := readIssue(t, sessionDir); attempts.Load() != 1 || issue.Status != sessionapi.IssueBlocked ||
			issue.Reason != sessionapi.ReasonSpecUnsatisfiable || issue.Message != "The spec needs a key it never provides." {
			t.Fatalf("attempts = %d, issue = %+v", attempts.Load(), issue)
		}
		wake <- syscall.SIGUSR1 // a wake alone does not lift a block
		synctest.Wait()
		if attempts.Load() != 1 {
			t.Fatalf("blocked root ran again: %d attempts", attempts.Load())
		}
		store := sessionapi.NewFileStore(filepath.Dir(sessionDir), sessionapi.RuntimeCloud)
		if _, err := store.Recheck(filepath.Base(sessionDir)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(holdRecheckInterval)
		synctest.Wait()
		if issue := readIssue(t, sessionDir); attempts.Load() != 2 || issue.Status != sessionapi.IssueRetrying {
			t.Fatalf("after resume: attempts = %d, issue = %+v", attempts.Load(), issue)
		}
		stop <- syscall.SIGTERM
		<-done
	})
}

func TestLiftedHoldKeepsAnAcceptedGoalOnItsSchedule(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		accepted := map[string]any{
			"id": 1, "started_at": "t", "finished_at": "t", "result": "completed", "spec_version": 1,
			"completion_reason": "verifier_conceded", "verifier_conceded": true, "checkpoint_saved": true,
		}
		interrupted := map[string]any{"id": 2, "started_at": "t", "finished_at": "t", "result": sessionapi.EpochHeld, "spec_version": 1}
		sessionDir := writeWorkerManifest(t, map[string]any{
			"session_kind":         "controller",
			"paused":               true,
			"current_spec_version": 1,
			"specs":                []map[string]any{{"name": "demo", "interval_seconds": 3600}},
			"epochs":               []map[string]any{accepted, interrupted},
		})
		wake, stop := make(chan os.Signal, 1), make(chan os.Signal, 1)
		var attempts atomic.Int32
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = runSessionWorker(sessionDir, false, func(string) (*game.PVGResult, error) {
				attempts.Add(1)
				return &game.PVGResult{GameResult: game.GameSuccess}, nil
			}, wake, stop)
		}()
		synctest.Wait()
		store := sessionapi.NewFileStore(filepath.Dir(sessionDir), sessionapi.RuntimeCloud)
		if _, err := store.Pause(filepath.Base(sessionDir), false); err != nil {
			t.Fatal(err)
		}
		time.Sleep(59 * time.Minute)
		synctest.Wait()
		if attempts.Load() != 0 {
			t.Fatalf("resuming an accepted goal started a check at once: %d attempts", attempts.Load())
		}
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		if attempts.Load() != 1 {
			t.Fatalf("scheduled check after resume: %d attempts", attempts.Load())
		}
		stop <- syscall.SIGTERM
		<-done
	})
}

func TestHoldLiftedBeforeTheCheckStopsKeepsAnAcceptedGoalOnItsSchedule(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		accepted := map[string]any{
			"id": 1, "started_at": "t", "finished_at": "t", "result": "completed", "spec_version": 1,
			"completion_reason": "verifier_conceded", "verifier_conceded": true, "checkpoint_saved": true,
		}
		sessionDir := writeWorkerManifest(t, map[string]any{
			"session_kind":         "controller",
			"current_spec_version": 1,
			"specs":                []map[string]any{{"name": "demo", "interval_seconds": 3600}},
			"epochs":               []map[string]any{accepted},
		})
		store := sessionapi.NewFileStore(filepath.Dir(sessionDir), sessionapi.RuntimeCloud)
		wake, stop := make(chan os.Signal, 1), make(chan os.Signal, 1)
		var attempts atomic.Int32
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = runSessionWorker(sessionDir, false, func(string) (*game.PVGResult, error) {
				if attempts.Add(1) > 1 {
					return &game.PVGResult{GameResult: game.GameSuccess}, nil
				}
				// Paused mid-check, then resumed while the check is still saving.
				for _, paused := range []bool{true, false} {
					if _, err := store.Pause(filepath.Base(sessionDir), paused); err != nil {
						t.Error(err)
					}
				}
				return &game.PVGResult{GameResult: game.GameStopped}, nil
			}, wake, stop)
		}()
		time.Sleep(59 * time.Minute)
		synctest.Wait()
		if attempts.Load() != 1 {
			t.Fatalf("resuming before the check stopped started another at once: %d attempts", attempts.Load())
		}
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		if attempts.Load() != 2 {
			t.Fatalf("scheduled check after resume: %d attempts", attempts.Load())
		}
		stop <- syscall.SIGTERM
		<-done
	})
}

func TestChildStoppedByRootHoldWaitsInsteadOfFailing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rootDir := writeWorkerManifest(t, map[string]any{"session_kind": "controller"})
		store := sessionapi.NewFileStore(filepath.Dir(rootDir), sessionapi.RuntimeCloud)
		childDir := filepath.Join(filepath.Dir(rootDir), "sess_child")
		parent := filepath.Base(rootDir)
		if err := sessionapi.WriteManifest(filepath.Join(childDir, "session.json"), &sessionapi.Manifest{
			SessionID: "sess_child", SessionKind: sessionapi.KindTask, ParentSessionID: &parent,
		}); err != nil {
			t.Fatal(err)
		}
		var attempts atomic.Int32
		done := make(chan int, 1)
		go func() {
			code, _ := runSessionWorker(childDir, false, func(string) (*game.PVGResult, error) {
				if attempts.Add(1) == 1 { // Cloud holds the goal mid-run.
					if _, err := store.Pause(filepath.Base(rootDir), true); err != nil {
						t.Error(err)
					}
					return &game.PVGResult{GameResult: game.GameStopped}, nil
				}
				return &game.PVGResult{GameResult: game.GameSuccess}, nil
			}, make(chan os.Signal), make(chan os.Signal))
			done <- code
		}()
		time.Sleep(time.Hour)
		synctest.Wait()
		if attempts.Load() != 1 {
			t.Fatalf("held child ran %d attempts", attempts.Load())
		}
		if _, err := store.Pause(filepath.Base(rootDir), false); err != nil {
			t.Fatal(err)
		}
		if code := <-done; code != 0 || attempts.Load() != 2 {
			t.Fatalf("child after hold lifted: code %d, %d attempts", code, attempts.Load())
		}
	})
}

func TestHeldRootWaitsWithoutRunning(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sessionDir := writeWorkerManifest(t, map[string]any{
			"session_kind": "controller",
			"paused":       true,
			"specs":        []map[string]any{{"name": "demo"}},
		})
		wake, stop := make(chan os.Signal, 1), make(chan os.Signal, 1)
		var attempts atomic.Int32
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = runSessionWorker(sessionDir, false, func(string) (*game.PVGResult, error) {
				attempts.Add(1)
				return &game.PVGResult{GameResult: game.GameSuccess}, nil
			}, wake, stop)
		}()
		time.Sleep(time.Hour)
		synctest.Wait()
		if attempts.Load() != 0 {
			t.Fatalf("held root ran %d attempts", attempts.Load())
		}
		store := sessionapi.NewFileStore(filepath.Dir(sessionDir), sessionapi.RuntimeCloud)
		if _, err := store.Pause(filepath.Base(sessionDir), false); err != nil {
			t.Fatal(err)
		}
		time.Sleep(holdRecheckInterval) // no wake: the root notices on its own
		synctest.Wait()
		if attempts.Load() != 1 {
			t.Fatalf("lifting the hold ran %d attempts", attempts.Load())
		}
		stop <- syscall.SIGTERM
		<-done
	})
}

func readIssue(t *testing.T, sessionDir string) sessionapi.Issue {
	t.Helper()
	m, err := sessionapi.ReadManifest(filepath.Join(sessionDir, "session.json"))
	if err != nil || m.Issue == nil {
		t.Fatalf("issue missing: %+v, %v", m, err)
	}
	return *m.Issue
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
				time.Sleep(2 * time.Minute)
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

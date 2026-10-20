package sessionworker

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/telos-org/telos/internal/sessionapi"
)

func TestAbruptWorkerExitNotifiesActivityWatchers(t *testing.T) {
	if os.Getenv("TELOS_TEST_EXIT_WORKER") == "1" {
		sessionDir := filepath.Join(os.Getenv("TELOS_SESSION_DIR"), os.Getenv("TELOS_SESSION_ID"))
		owner, err := AcquireOwnership(sessionDir, "")
		if err != nil {
			t.Fatal(err)
		}
		defer owner.Release()
		time.Sleep(time.Hour)
		return
	}
	root := t.TempDir()
	sessionDir := filepath.Join(root, "sess_exit")
	manifest := sessionapi.ManifestFromInitial(sessionapi.InitialManifest{
		SessionID: "sess_exit", SpecName: "exit",
	})
	if err := sessionapi.WriteManifest(manifestPath(sessionDir), &manifest); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(root, "telosd")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nexec \"$TELOS_TEST_WORKER_BINARY\" -test.run=^TestAbruptWorkerExitNotifiesActivityWatchers$\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TELOSD_PATH", helper)
	t.Setenv("TELOS_TEST_EXIT_WORKER", "1")
	t.Setenv("TELOS_TEST_WORKER_BINARY", os.Args[0])
	if err := Start(sessionDir, sessionapi.RuntimeLocal); err != nil {
		t.Fatal(err)
	}
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeLocal)
	deadline := time.Now().Add(3 * time.Second)
	for {
		manifest, err := sessionapi.ReadManifest(manifestPath(sessionDir))
		if err != nil {
			t.Fatal(err)
		}
		if manifest.Runner != nil {
			pid := manifest.Runner.PID
			killed := false
			t.Cleanup(func() {
				if !killed {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
			})
			changed, stop, err := store.WatchEvents("sess_exit")
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
				t.Fatal(err)
			}
			killed = true
			select {
			case _, ok := <-changed:
				if !ok {
					t.Fatal("watch failed instead of announcing worker exit")
				}
			case <-time.After(time.Second):
				t.Fatal("abrupt worker exit did not reach activity watchers")
			}
			if status, err := store.Status("sess_exit"); err != nil || status != sessionapi.StatusStale {
				t.Fatalf("worker exit status: %s, %v", status, err)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("worker did not acquire ownership")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAcquireOwnershipIsExclusiveAndRecordsTopLevelRunner(t *testing.T) {
	sessionDir := t.TempDir()
	if err := sessionapi.WriteManifest(manifestPath(sessionDir), &sessionapi.Manifest{
		SessionID:   filepath.Base(sessionDir),
		SessionKind: sessionapi.KindController,
	}); err != nil {
		t.Fatal(err)
	}

	owner, err := AcquireOwnership(sessionDir, filepath.Join(sessionDir, "runner.log"))
	if err != nil {
		t.Fatalf("AcquireOwnership: %v", err)
	}
	defer owner.Release()

	_, err = AcquireOwnership(sessionDir, "")
	if !errors.Is(err, ErrWorkerAlreadyRunning) {
		t.Fatalf("second AcquireOwnership: got %v want ErrWorkerAlreadyRunning", err)
	}

	manifest, err := sessionapi.ReadManifest(manifestPath(sessionDir))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Runner == nil || manifest.Runner.PID != os.Getpid() {
		t.Fatalf("top-level runner not recorded: %#v", manifest.Runner)
	}
}

func TestStartEpochWithRunnerPreservesOpenEpochRunner(t *testing.T) {
	sessionDir := t.TempDir()
	oldLog := filepath.Join(sessionDir, "runner.log")
	manifest := &sessionapi.Manifest{
		SessionID:   filepath.Base(sessionDir),
		SessionKind: sessionapi.KindController,
		Epochs: []sessionapi.Epoch{{
			ID:        1,
			StartedAt: "2026-07-07T00:00:00.000Z",
			Runner: &sessionapi.Runner{
				Kind:    "local-subprocess",
				PID:     os.Getpid(),
				PGID:    os.Getpid(),
				LogPath: oldLog,
			},
		}},
	}
	if err := sessionapi.WriteManifest(manifestPath(sessionDir), manifest); err != nil {
		t.Fatal(err)
	}

	id, err := StartEpochWithRunner(sessionDir, manifest, os.Getpid()+1, filepath.Join(sessionDir, "new.log"))
	if err != nil {
		t.Fatalf("StartEpochWithRunner: %v", err)
	}
	if id != 1 {
		t.Fatalf("epoch id: got %d want 1", id)
	}
	updated, err := sessionapi.ReadManifest(manifestPath(sessionDir))
	if err != nil {
		t.Fatal(err)
	}
	if got := updated.OpenEpoch().Runner.LogPath; got != oldLog {
		t.Fatalf("epoch runner should be history, got log %q want %q", got, oldLog)
	}
}

func TestStartEpochBindsProvidedRevisionIdentity(t *testing.T) {
	sessionDir := t.TempDir()
	oldVersion := 1
	oldRevision := "0.1.0"
	oldDigest := "sha256:old"
	provided := &sessionapi.Manifest{
		SessionID:          "sess-controller",
		SessionKind:        sessionapi.KindController,
		SpecName:           "controller",
		CurrentSpecVersion: &oldVersion,
		CurrentRevision:    &oldRevision,
		PackageDigest:      &oldDigest,
		SpecVersions: []map[string]any{{
			"version":     oldVersion,
			"spec_sha256": "sha256:old-spec",
		}},
	}
	newVersion := 2
	newRevision := "0.2.0"
	newDigest := "sha256:new"
	onDisk := *provided
	onDisk.CurrentSpecVersion = &newVersion
	onDisk.CurrentRevision = &newRevision
	onDisk.PackageDigest = &newDigest
	onDisk.SpecVersions = []map[string]any{{
		"version":     newVersion,
		"spec_sha256": "sha256:new-spec",
	}}
	if err := sessionapi.WriteManifest(manifestPath(sessionDir), &onDisk); err != nil {
		t.Fatal(err)
	}

	if _, err := StartEpochWithRunner(sessionDir, provided, os.Getpid(), ""); err != nil {
		t.Fatal(err)
	}
	updated, err := sessionapi.ReadManifest(manifestPath(sessionDir))
	if err != nil {
		t.Fatal(err)
	}
	epoch := updated.LastEpoch()
	if epoch.SpecVersion == nil || *epoch.SpecVersion != oldVersion {
		t.Fatalf("spec version: %#v", epoch.SpecVersion)
	}
	if epoch.Revision == nil || *epoch.Revision != oldRevision {
		t.Fatalf("revision: %#v", epoch.Revision)
	}
	if epoch.PackageDigest == nil || *epoch.PackageDigest != oldDigest {
		t.Fatalf("package digest: %#v", epoch.PackageDigest)
	}
	if epoch.SpecSHA256 != "sha256:old-spec" {
		t.Fatalf("spec sha: %q", epoch.SpecSHA256)
	}
}

func TestStartEpochDoesNotAppendAfterSessionStops(t *testing.T) {
	sessionDir := t.TempDir()
	manifest := &sessionapi.Manifest{
		SessionID:     "sess-stopped",
		SessionKind:   sessionapi.KindController,
		DesiredStatus: sessionapi.DesiredStatusStopped,
		SpecName:      "controller",
	}
	stopped := "stopped"
	finishedAt := "2026-08-14T12:00:00.000Z"
	epoch := sessionapi.NewEpoch(manifest, 1, finishedAt, nil)
	epoch.FinishedAt = &finishedAt
	epoch.Result = &stopped
	manifest.Epochs = append(manifest.Epochs, epoch)
	if err := sessionapi.WriteManifest(manifestPath(sessionDir), manifest); err != nil {
		t.Fatal(err)
	}

	if _, err := StartEpochWithRunner(sessionDir, manifest, os.Getpid(), ""); !errors.Is(err, ErrSessionStopped) {
		t.Fatalf("StartEpochWithRunner error: got %v want %v", err, ErrSessionStopped)
	}
	updated, err := sessionapi.ReadManifest(manifestPath(sessionDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Epochs) != 1 {
		t.Fatalf("epochs: got %d want 1", len(updated.Epochs))
	}
}

func TestWorkerExportsSelectedModelForNestedCLI(t *testing.T) {
	const model = "anthropic/claude-sonnet-4-6"
	t.Setenv("TELOS_MODEL", "openai-codex/gpt-5.5")
	sessionDir := t.TempDir()
	manifest := &sessionapi.Manifest{
		SessionKind: sessionapi.KindController,
		Runtime:     sessionapi.RuntimeCloud,
	}
	manifest.Config.Model = model
	if err := sessionapi.WriteManifest(manifestPath(sessionDir), manifest); err != nil {
		t.Fatal(err)
	}
	environment := map[string]string{}
	for _, entry := range Env(sessionDir, StartOptions{Runtime: sessionapi.RuntimeCloud}) {
		key, value, _ := strings.Cut(entry, "=")
		environment[key] = value
	}
	if got := environment["TELOS_MODEL"]; got != model {
		t.Fatalf("nested CLI model = %q, want %q", got, model)
	}
}

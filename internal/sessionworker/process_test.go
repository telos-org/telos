package sessionworker

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/sessionapi"
)

func TestEnvPropagatesCheckpointAdmissionScope(t *testing.T) {
	t.Setenv("TELOS_CHECKPOINT_ROOT", "/stale-root")
	t.Setenv("TELOS_CHECKPOINT_SESSION_ID", "sess_stale")
	sessionDir := t.TempDir()
	env := Env(sessionDir, StartOptions{
		Runtime:             sessionapi.RuntimeCloud,
		CheckpointRoot:      "/telos-state",
		CheckpointSessionID: "sess_deployment_123",
	})
	joined := strings.Join(env, "\n")
	for _, want := range []string{
		"TELOS_CHECKPOINT_ROOT=/telos-state",
		"TELOS_CHECKPOINT_SESSION_ID=sess_deployment_123",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("worker environment missing %q", want)
		}
	}
	if strings.Contains(joined, "/stale-root") || strings.Contains(joined, "sess_stale") {
		t.Fatalf("worker environment retained stale checkpoint scope:\n%s", joined)
	}

	disabled := strings.Join(Env(sessionDir, StartOptions{Runtime: sessionapi.RuntimeCloud}), "\n")
	if strings.Contains(disabled, "TELOS_CHECKPOINT_") {
		t.Fatalf("worker environment retained checkpoint admission while disabled:\n%s", disabled)
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

func TestActiveWorkerCapabilityReadiness(t *testing.T) {
	sessionDir := t.TempDir()
	if err := sessionapi.WriteManifest(manifestPath(sessionDir), &sessionapi.Manifest{
		SessionID:   filepath.Base(sessionDir),
		SessionKind: sessionapi.KindController,
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TELOS_CHECKPOINT_ROOT", "")
	t.Setenv("TELOS_CHECKPOINT_SESSION_ID", "")
	legacyOwner, err := AcquireOwnership(sessionDir, "")
	if err != nil {
		t.Fatal(err)
	}
	ready, err := ActiveWorkerSupportsCapabilities(
		sessionDir,
		sessionapi.CapabilityCheckpointSafePoint,
	)
	if err != nil {
		t.Fatal(err)
	}
	if ready {
		t.Fatal("active legacy worker was accepted as checkpoint-aware")
	}
	if err := legacyOwner.Release(); err != nil {
		t.Fatal(err)
	}
	ready, err = ActiveWorkerSupportsCapabilities(
		sessionDir,
		sessionapi.CapabilityCheckpointSafePoint,
	)
	if err != nil {
		t.Fatal(err)
	}
	if ready {
		t.Fatal("worker cleanup gap was accepted before its runner record was cleared")
	}

	t.Setenv("TELOS_CHECKPOINT_ROOT", "/telos-state")
	t.Setenv("TELOS_CHECKPOINT_SESSION_ID", "sess_deployment_123")
	checkpointOwner, err := AcquireOwnership(sessionDir, "")
	if err != nil {
		t.Fatal(err)
	}
	defer checkpointOwner.Release()
	ready, err = ActiveWorkerSupportsCapabilities(
		sessionDir,
		sessionapi.CapabilityCheckpointSafePoint,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !ready {
		t.Fatal("active checkpoint-aware worker was rejected")
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
	t.Setenv("TELOS_CHECKPOINT_ROOT", "")
	t.Setenv("TELOS_CHECKPOINT_SESSION_ID", "")
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
	if len(epoch.WorkerCapabilities) != 1 || epoch.WorkerCapabilities[0] != sessionapi.CapabilityEpochFinalizedEventsV1 {
		t.Fatalf("worker capabilities: %#v", epoch.WorkerCapabilities)
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

func TestRunnerIdentityAdvertisesFinalizationCapability(t *testing.T) {
	t.Setenv("TELOS_CHECKPOINT_ROOT", "")
	t.Setenv("TELOS_CHECKPOINT_SESSION_ID", "")
	runner := RunnerIdentity(42)
	if !runnerSupportsCapability(&runner, sessionapi.CapabilityEpochFinalizedEventsV1) {
		t.Fatalf("runner capabilities: %#v", runner.WorkerCapabilities)
	}
	if runnerSupportsCapability(&runner, sessionapi.CapabilityCheckpointSafePoint) {
		t.Fatalf("unscoped worker advertised checkpoint participation: %#v", runner.WorkerCapabilities)
	}
}

func TestRunnerIdentityAdvertisesCheckpointCapabilityOnlyWithCompleteScope(t *testing.T) {
	t.Setenv("TELOS_CHECKPOINT_ROOT", "/telos-state")
	t.Setenv("TELOS_CHECKPOINT_SESSION_ID", "")
	partial := RunnerIdentity(42)
	if runnerSupportsCapability(&partial, sessionapi.CapabilityCheckpointSafePoint) {
		t.Fatalf("partially scoped worker advertised checkpoint participation: %#v", partial.WorkerCapabilities)
	}

	t.Setenv("TELOS_CHECKPOINT_SESSION_ID", "sess_deployment_123")
	claimed := RunnerIdentity(42)
	if !runnerSupportsCapability(&claimed, sessionapi.CapabilityCheckpointSafePoint) {
		t.Fatalf("scoped worker capabilities: %#v", claimed.WorkerCapabilities)
	}
	if !runnerSupportsRequiredCapabilities(&claimed, StartOptions{
		CheckpointRoot:      "/telos-state",
		CheckpointSessionID: "sess_deployment_123",
	}) {
		t.Fatalf("scoped worker did not meet rollout requirements: %#v", claimed.WorkerCapabilities)
	}
	if runnerSupportsRequiredCapabilities(&partial, StartOptions{
		CheckpointRoot:      "/telos-state",
		CheckpointSessionID: "sess_deployment_123",
	}) {
		t.Fatal("legacy worker was accepted for checkpoint-aware rollout")
	}
}

func TestWorkerArgvMustMatchSessionBeforeRetirement(t *testing.T) {
	sessionDir := "/var/lib/telos/sessions/sess_1"
	if !workerArgvMatchesSession(
		[]string{"/usr/local/bin/telosd", "--session-dir", sessionDir},
		sessionDir,
	) {
		t.Fatal("expected exact telosd session command to match")
	}
	if !workerArgvMatchesSession(
		[]string{"/usr/local/bin/telosd", "--session-dir=" + sessionDir},
		sessionDir,
	) {
		t.Fatal("expected exact telosd session assignment to match")
	}
	if workerArgvMatchesSession(
		[]string{"/usr/local/bin/telosd", "--session-dir", "/var/lib/telos/sessions/sess_10"},
		sessionDir,
	) {
		t.Fatal("must not match a different session")
	}
	if workerArgvMatchesSession(
		[]string{"/usr/local/bin/telosd-helper", "--session-dir", sessionDir},
		sessionDir,
	) {
		t.Fatal("must not match an unrelated recycled PID")
	}
}

package sessionworker

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/telos-org/telos/internal/runtimeenv"
	"github.com/telos-org/telos/internal/sessionapi"
)

func TestEnvIncludesRuntimeCredentialEnvironmentPath(t *testing.T) {
	sessionDir := t.TempDir()
	path := filepath.Join(t.TempDir(), "credential-environment.json")
	environment := Env(sessionDir, StartOptions{
		Runtime:                          sessionapi.RuntimeCloud,
		RuntimeCredentialEnvironmentPath: path,
	})

	want := runtimeenv.PathEnvironmentVariable + "=" + path
	for _, entry := range environment {
		if entry == want {
			return
		}
	}
	t.Fatalf("runtime credential environment path missing from %v", environment)
}

func TestAcquireOwnershipIsExclusiveAndRecordsTopLevelRunner(t *testing.T) {
	t.Setenv(runtimeenv.PathEnvironmentVariable, "")
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
	if len(manifest.Runner.Capabilities) != 0 {
		t.Fatalf("unconfigured worker advertised capabilities: %#v", manifest.Runner.Capabilities)
	}
}

func TestRuntimeCredentialEnvironmentCapabilityStatusUsesLiveConfiguredRunner(t *testing.T) {
	sessionDir := t.TempDir()
	if err := sessionapi.WriteManifest(manifestPath(sessionDir), &sessionapi.Manifest{
		SessionID:   filepath.Base(sessionDir),
		SessionKind: sessionapi.KindController,
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(runtimeenv.PathEnvironmentVariable, filepath.Join(t.TempDir(), "credential-environment.json"))
	owner, err := AcquireOwnership(sessionDir, "")
	if err != nil {
		t.Fatal(err)
	}

	live, supported, err := RuntimeCredentialEnvironmentCapabilityStatus(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if !live || !supported {
		t.Fatalf("configured live worker: live=%t supported=%t", live, supported)
	}
	if err := owner.Release(); err != nil {
		t.Fatal(err)
	}
	live, supported, err = RuntimeCredentialEnvironmentCapabilityStatus(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if live || supported {
		t.Fatalf("stale runner record: live=%t supported=%t", live, supported)
	}
}

func TestRuntimeCredentialEnvironmentCapabilityStatusRejectsStaleRunnerPID(t *testing.T) {
	sessionDir := t.TempDir()
	if err := sessionapi.WriteManifest(manifestPath(sessionDir), &sessionapi.Manifest{
		SessionID:   filepath.Base(sessionDir),
		SessionKind: sessionapi.KindController,
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(runtimeenv.PathEnvironmentVariable, filepath.Join(t.TempDir(), "credential-environment.json"))
	owner, err := AcquireOwnership(sessionDir, "")
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Release()
	if _, err := sessionapi.MutateManifest(manifestPath(sessionDir), func(manifest *sessionapi.Manifest) error {
		manifest.Runner.PID = 1 << 30
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	live, supported, err := RuntimeCredentialEnvironmentCapabilityStatus(sessionDir)
	if !errors.Is(err, ErrWorkerReadinessTransient) {
		t.Fatalf("stale PID readiness: got %v want ErrWorkerReadinessTransient", err)
	}
	if !live || supported {
		t.Fatalf("stale PID capability: live=%t supported=%t", live, supported)
	}
}

func TestRuntimeCredentialEnvironmentCapabilityStatusTreatsIncompleteRunnerAsTransient(t *testing.T) {
	sessionDir := t.TempDir()
	if err := sessionapi.WriteManifest(manifestPath(sessionDir), &sessionapi.Manifest{
		SessionID:   filepath.Base(sessionDir),
		SessionKind: sessionapi.KindController,
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(runtimeenv.PathEnvironmentVariable, filepath.Join(t.TempDir(), "credential-environment.json"))
	owner, err := AcquireOwnership(sessionDir, "")
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Release()
	if _, err := sessionapi.MutateManifest(manifestPath(sessionDir), func(manifest *sessionapi.Manifest) error {
		manifest.Runner.Kind = ""
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	live, supported, err := RuntimeCredentialEnvironmentCapabilityStatus(sessionDir)
	if !errors.Is(err, ErrWorkerReadinessTransient) {
		t.Fatalf("incomplete runner readiness: got %v want ErrWorkerReadinessTransient", err)
	}
	if !live || supported {
		t.Fatalf("incomplete runner capability: live=%t supported=%t", live, supported)
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

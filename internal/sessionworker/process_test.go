package sessionworker

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/telos-org/telos/internal/sessionapi"
)

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
	if epoch.FinalizationKey != "sess-controller:epoch:00000001:finalized" {
		t.Fatalf("finalization key: %q", epoch.FinalizationKey)
	}
	if len(epoch.WorkerCapabilities) != 1 || epoch.WorkerCapabilities[0] != sessionapi.CapabilityEpochFinalizedEventsV1 {
		t.Fatalf("worker capabilities: %#v", epoch.WorkerCapabilities)
	}
}

func TestRunnerIdentityAdvertisesFinalizationCapability(t *testing.T) {
	runner := RunnerIdentity(42)
	if !runnerSupportsCapability(&runner, sessionapi.CapabilityEpochFinalizedEventsV1) {
		t.Fatalf("runner capabilities: %#v", runner.WorkerCapabilities)
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

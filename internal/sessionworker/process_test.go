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

package sessionapi

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCreateSessionDirSkipsExistingID(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root, RuntimeLocal)

	sessionSeq.Store(0)
	existingID := generateSessionID(RuntimeLocal)
	existingDir := filepath.Join(root, existingID)
	if err := os.Mkdir(existingDir, 0o755); err != nil {
		t.Fatal(err)
	}

	sessionSeq.Store(0)
	id, dir, err := store.createSessionDir(SessionCreateRequest{}, KindTask)
	if err != nil {
		t.Fatal(err)
	}
	if id == existingID {
		t.Fatalf("reused existing session id %q", id)
	}
	if dir == existingDir {
		t.Fatalf("reused existing session dir %q", dir)
	}
	if _, err := os.Stat(existingDir); err != nil {
		t.Fatalf("existing session dir was modified: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("created session dir missing: %v", err)
	}
}

func TestListRootWorkerSessionsReadsOnlyEligibleManifests(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root, RuntimeCloud)
	parent := "sess_root"
	stopped := "stopped"
	write := func(id string, kind SessionKind, parentID *string, result *string) {
		t.Helper()
		dir := filepath.Join(root, id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		manifest := &Manifest{
			SessionID:       id,
			SessionKind:     kind,
			ParentSessionID: parentID,
		}
		if result != nil {
			finishedAt := "2026-08-14T12:00:00.000Z"
			manifest.Epochs = []Epoch{{
				ID:         1,
				StartedAt:  "2026-08-14T11:59:00.000Z",
				FinishedAt: &finishedAt,
				Result:     result,
			}}
		}
		if err := WriteManifest(filepath.Join(dir, "session.json"), manifest); err != nil {
			t.Fatal(err)
		}
	}
	write("sess_root", KindController, nil, nil)
	write("sess_child", KindController, &parent, nil)
	write("sess_task", KindTask, nil, nil)
	write("sess_stopped", KindController, nil, &stopped)

	sessions, err := store.ListRootWorkerSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].SessionID != "sess_root" {
		t.Fatalf("root worker sessions: %#v", sessions)
	}
	if sessions[0].SessionDir == nil || *sessions[0].SessionDir != filepath.Join(root, "sess_root") {
		t.Fatalf("session dir: %#v", sessions[0].SessionDir)
	}
}

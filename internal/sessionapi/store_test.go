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

func TestStopBeforeWorkerEmitsBoundFinalizationExactlyOnce(t *testing.T) {
	store, session := createCloudStoreSession(t)

	stopped, err := store.Stop(session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Status != StatusStopped {
		t.Fatalf("status: got %s want %s", stopped.Status, StatusStopped)
	}

	manifest, err := ReadManifest(store.manifestPath(session.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	epoch := manifest.LastEpoch()
	if epoch == nil {
		t.Fatal("missing stopped epoch")
	}
	if epoch.SpecName != manifest.SpecName || epoch.SpecVersion == nil {
		t.Fatalf("missing bound spec identity: %#v", epoch)
	}
	if epoch.PackageDigest == nil || *epoch.PackageDigest == "" || epoch.SpecSHA256 == "" {
		t.Fatalf("missing bound package identity: %#v", epoch)
	}
	if epoch.FinalizationKey != session.SessionID+":epoch:00000001:finalized" {
		t.Fatalf("finalization key: %q", epoch.FinalizationKey)
	}
	if !epoch.FinalizationEventEmitted {
		t.Fatal("finalization marker was not persisted")
	}

	assertSingleStoppedFinalization(t, store, session.SessionID, epoch)
	if _, err := store.Stop(session.SessionID); err != nil {
		t.Fatal(err)
	}
	assertSingleStoppedFinalization(t, store, session.SessionID, epoch)
}

func TestStopOpenEpochPreservesBoundIdentity(t *testing.T) {
	store, session := createCloudStoreSession(t)
	manifest, err := ReadManifest(store.manifestPath(session.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	oldVersion := 1
	oldRevision := "revision-old"
	oldDigest := "sha256:old"
	manifest.CurrentSpecVersion = &oldVersion
	manifest.CurrentRevision = &oldRevision
	manifest.PackageDigest = &oldDigest
	manifest.SpecVersions = []map[string]any{{
		"version":     oldVersion,
		"spec_sha256": "sha256:old-spec",
	}}
	epoch := Epoch{
		ID:        1,
		StartedAt: "2026-08-14T11:59:00.000Z",
		Runner:    &Runner{PID: 999999},
	}
	BindEpochFinalizationIdentity(manifest, &epoch)
	manifest.Epochs = append(manifest.Epochs, epoch)
	newVersion := 2
	newRevision := "revision-new"
	newDigest := "sha256:new"
	manifest.CurrentSpecVersion = &newVersion
	manifest.CurrentRevision = &newRevision
	manifest.PackageDigest = &newDigest
	manifest.SpecVersions = append(manifest.SpecVersions, map[string]any{
		"version":     newVersion,
		"spec_sha256": "sha256:new-spec",
	})
	if err := WriteManifest(store.manifestPath(session.SessionID), manifest); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Stop(session.SessionID); err != nil {
		t.Fatal(err)
	}
	events, err := store.Events(session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	finalized := finalizedEvents(events)
	if len(finalized) != 1 {
		t.Fatalf("epoch_finalized events: got %d want 1", len(finalized))
	}
	data := finalized[0].Data
	if data["revision"] != oldRevision || data["package_digest"] != oldDigest {
		t.Fatalf("event rebound to mutable desired state: %#v", data)
	}
	if data["spec_version"] != float64(oldVersion) || data["spec_sha256"] != "sha256:old-spec" {
		t.Fatalf("unexpected bound spec identity: %#v", data)
	}
}

func TestStopLegacyOpenEpochEmitsSeparateCurrentStopIdentity(t *testing.T) {
	store, session := createCloudStoreSession(t)
	manifest, err := ReadManifest(store.manifestPath(session.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	currentVersion := 2
	currentRevision := "revision-current"
	currentDigest := "sha256:current"
	manifest.CurrentSpecVersion = &currentVersion
	manifest.CurrentRevision = &currentRevision
	manifest.PackageDigest = &currentDigest
	manifest.SpecVersions = append(manifest.SpecVersions, map[string]any{
		"version":     currentVersion,
		"spec_sha256": "sha256:current-spec",
	})
	manifest.Epochs = append(manifest.Epochs, Epoch{
		ID:        1,
		StartedAt: "2026-08-14T11:59:00.000Z",
		Runner:    &Runner{PID: 999999},
	})
	if err := WriteManifest(store.manifestPath(session.SessionID), manifest); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Stop(session.SessionID); err != nil {
		t.Fatal(err)
	}
	updated, err := ReadManifest(store.manifestPath(session.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Epochs) != 2 {
		t.Fatalf("epochs: got %d want 2", len(updated.Epochs))
	}
	legacy := updated.Epochs[0]
	if legacy.Result == nil || *legacy.Result != "stopped" || legacy.FinalizationKey != "" {
		t.Fatalf("legacy epoch was rebound: %#v", legacy)
	}
	synthetic := updated.Epochs[1]
	if synthetic.FinalizationKey == "" || !synthetic.FinalizationEventEmitted {
		t.Fatalf("synthetic stop was not finalized: %#v", synthetic)
	}
	events, err := store.Events(session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	finalized := finalizedEvents(events)
	if len(finalized) != 1 {
		t.Fatalf("epoch_finalized events: got %d want 1", len(finalized))
	}
	data := finalized[0].Data
	if data["revision"] != currentRevision || data["package_digest"] != currentDigest {
		t.Fatalf("synthetic stop identity: %#v", data)
	}
	if data["spec_version"] != float64(currentVersion) || data["spec_sha256"] != "sha256:current-spec" {
		t.Fatalf("synthetic stop spec identity: %#v", data)
	}
}

func TestStopIdleControllerAppendsEpochWithoutRewritingHistory(t *testing.T) {
	store, session := createCloudStoreSession(t)
	manifest, err := ReadManifest(store.manifestPath(session.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	completed := "completed"
	finishedAt := "2026-08-14T11:58:00.000Z"
	epoch := Epoch{
		ID:                       1,
		StartedAt:                "2026-08-14T11:57:00.000Z",
		FinishedAt:               &finishedAt,
		Result:                   &completed,
		FinalizationEventEmitted: true,
	}
	BindEpochFinalizationIdentity(manifest, &epoch)
	manifest.Epochs = append(manifest.Epochs, epoch)
	if err := WriteManifest(store.manifestPath(session.SessionID), manifest); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Stop(session.SessionID); err != nil {
		t.Fatal(err)
	}
	updated, err := ReadManifest(store.manifestPath(session.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Epochs) != 2 {
		t.Fatalf("epochs: got %d want 2", len(updated.Epochs))
	}
	if updated.Epochs[0].Result == nil || *updated.Epochs[0].Result != completed {
		t.Fatalf("completed history was rewritten: %#v", updated.Epochs[0])
	}
	if updated.Epochs[1].Result == nil || *updated.Epochs[1].Result != "stopped" {
		t.Fatalf("missing synthetic stopped epoch: %#v", updated.Epochs[1])
	}
}

func TestSecondStopRepairsPersistedFinalizationOutbox(t *testing.T) {
	store, session := createCloudStoreSession(t)
	manifest, err := ReadManifest(store.manifestPath(session.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	badEvidencePath := t.TempDir()
	manifest.Specs[0].EvidencePath = &badEvidencePath
	if err := WriteManifest(store.manifestPath(session.SessionID), manifest); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Stop(session.SessionID); err == nil {
		t.Fatal("expected finalization append failure")
	}
	pending, err := ReadManifest(store.manifestPath(session.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	if !epochFinalizationPending(pending.LastEpoch()) {
		t.Fatalf("stop did not persist repairable outbox: %#v", pending.LastEpoch())
	}
	evidencePath := filepath.Join(t.TempDir(), "evidence.jsonl")
	pending.Specs[0].EvidencePath = &evidencePath
	if err := WriteManifest(store.manifestPath(session.SessionID), pending); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Stop(session.SessionID); err != nil {
		t.Fatal(err)
	}
	assertSingleStoppedFinalization(t, store, session.SessionID, pending.LastEpoch())
}

func createCloudStoreSession(t *testing.T) (*FileStore, *Session) {
	t.Helper()
	store := NewFileStore(t.TempDir(), RuntimeCloud)
	markdown := "---\nversion: 0.1.0\nname: service\nplatform: cloud\n---\n# Service\n"
	session, err := store.Create(SessionCreateRequest{SpecMarkdown: &markdown})
	if err != nil {
		t.Fatal(err)
	}
	return store, session
}

func assertSingleStoppedFinalization(
	t *testing.T,
	store *FileStore,
	sessionID string,
	epoch *Epoch,
) {
	t.Helper()
	events, err := store.Events(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	finalized := finalizedEvents(events)
	if len(finalized) != 1 {
		t.Fatalf("epoch_finalized events: got %d want 1", len(finalized))
	}
	data := finalized[0].Data
	if data["finalization_key"] != epoch.FinalizationKey || data["result"] != "stopped" {
		t.Fatalf("unexpected stopped finalization: %#v", data)
	}
	if data["checkpoint_saved"] != false || data["verifier_conceded"] != false {
		t.Fatalf("unexpected stop completion metadata: %#v", data)
	}
}

func finalizedEvents(events []SessionEvent) []SessionEvent {
	finalized := make([]SessionEvent, 0)
	for _, event := range events {
		if event.Event == "epoch_finalized" {
			finalized = append(finalized, event)
		}
	}
	return finalized
}

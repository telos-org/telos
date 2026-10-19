package sessionapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestEvidenceResumeRestoresTotalsAndActiveTurn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "evidence.jsonl")
	// Activity may be incomplete or stale when the process dies between a
	// durable checkpoint and its best-effort projection.
	history := `{"event":"agent_complete","round":1,"role":"prover","data":{"cost_usd":9,"input_tokens":99}}` + "\n" +
		`{"event":"round_start","round":1,"role":"prover"}` + "\n"
	resume := `{"event":"game_resume","round":1,"data":{"prover_rounds":1,"verifier_rounds":0,"total_cost_usd":0.25,"total_input_tokens":10,"total_output_tokens":20,"total_cache_read_tokens":30,"total_cache_creation_tokens":40,"verifier_conceded":false}}` + "\n"
	end := `{"event":"game_end","round":2,"data":{"prover_rounds":1,"verifier_rounds":1,"total_cost_usd":0.75,"total_input_tokens":13,"total_output_tokens":24,"total_cache_read_tokens":35,"total_cache_creation_tokens":46,"verifier_conceded":true,"completion_reason":"verifier_conceded"}}` + "\n"
	for _, phase := range []struct {
		name, events                        string
		cost                                float64
		input, output, read, create, rounds int
		active, complete                    bool
	}{
		{"resume replaces stale totals and active round", resume, 0.25, 10, 20, 30, 40, 1, false, false},
		{"resumed verifier becomes active", `{"event":"round_start","round":2,"role":"verifier"}` + "\n", 0.25, 10, 20, 30, 40, 1, true, false},
		{"completed totals replace turn additions", `{"event":"agent_complete","round":2,"role":"verifier","data":{"cost_usd":0.5,"input_tokens":3}}` + "\n" + end, 0.75, 13, 24, 35, 46, 2, false, true},
		{"repeated terminal projection is idempotent", end, 0.75, 13, 24, 35, 46, 2, false, true},
	} {
		history += phase.events
		t.Run(phase.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(history), 0o600); err != nil {
				t.Fatal(err)
			}
			summary, err := readEvidenceSummary(&path)
			if err != nil || summary == nil {
				t.Fatalf("summary = %+v, %v", summary, err)
			}
			if ptrOr(summary.TotalCostUSD, -1) != phase.cost || ptrOr(summary.TotalInputTokens, -1) != phase.input || ptrOr(summary.TotalOutputTokens, -1) != phase.output ||
				ptrOr(summary.TotalCacheReadTokens, -1) != phase.read || ptrOr(summary.TotalCacheCreateTokens, -1) != phase.create || ptrOr(summary.RoundCount, -1) != phase.rounds || ptrOr(summary.VerifierConceded, false) != phase.complete {
				t.Fatalf("incorrect restored totals: %+v", summary)
			}
			if phase.active {
				if ptrOr(summary.CurrentRound, -1) != 2 || ptrOr(summary.CurrentRole, "") != "verifier" {
					t.Fatalf("resumed verifier missing: %+v", summary)
				}
			} else if summary.CurrentRound != nil || summary.CurrentRole != nil {
				t.Fatalf("stale active turn survived aggregate projection: %+v", summary)
			}
		})
	}
}

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
	write("sess_interrupted", KindController, nil, &stopped)
	if _, err := MutateManifest(
		filepath.Join(root, "sess_interrupted", "session.json"),
		func(manifest *Manifest) error {
			manifest.DesiredStatus = DesiredStatusRunning
			return nil
		},
	); err != nil {
		t.Fatal(err)
	}

	sessions, err := store.ListRootWorkerSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("root worker sessions: %#v", sessions)
	}
	byID := map[string]Session{}
	for _, session := range sessions {
		byID[session.SessionID] = session
	}
	rootSession, ok := byID["sess_root"]
	if !ok {
		t.Fatalf("missing root session: %#v", sessions)
	}
	if _, ok := byID["sess_interrupted"]; !ok {
		t.Fatalf("explicit running intent was lost: %#v", sessions)
	}
	if rootSession.SessionDir == nil || *rootSession.SessionDir != filepath.Join(root, "sess_root") {
		t.Fatalf("session dir: %#v", rootSession.SessionDir)
	}
}

func TestStopCreatesOneBoundTerminalEpoch(t *testing.T) {
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
	if _, err := store.Stop(session.SessionID); err != nil {
		t.Fatal(err)
	}
	manifest, err = ReadManifest(store.manifestPath(session.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Epochs) != 1 {
		t.Fatalf("repeated stop appended epochs: %#v", manifest.Epochs)
	}
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
	epoch := NewEpoch(
		manifest,
		1,
		"2026-08-14T11:59:00.000Z",
		&Runner{PID: 999999},
	)
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
	updated, err := ReadManifest(store.manifestPath(session.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Epochs) != 1 {
		t.Fatalf("epochs: got %d want 1", len(updated.Epochs))
	}
	stopped := updated.Epochs[0]
	if stopped.Result == nil || *stopped.Result != "stopped" {
		t.Fatalf("epoch result: %#v", stopped)
	}
	if ptrOr(stopped.Revision, "") != oldRevision || ptrOr(stopped.PackageDigest, "") != oldDigest ||
		ptrOr(stopped.SpecVersion, 0) != oldVersion || stopped.SpecSHA256 != "sha256:old-spec" {
		t.Fatalf("stopped epoch rebound to mutable desired state: %#v", stopped)
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
	epoch := NewEpoch(manifest, 1, "2026-08-14T11:57:00.000Z", nil)
	epoch.FinishedAt = &finishedAt
	epoch.Result = &completed
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

func TestSessionReconciliationUsesCurrentEpoch(t *testing.T) {
	store, session := createCloudStoreSession(t)
	manifest, err := ReadManifest(store.manifestPath(session.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	completed := "completed"
	finishedAt := "2026-08-14T12:00:00.000Z"
	completionReason := "verifier_conceded"
	conceded := true
	checkpointSaved := true
	epoch := NewEpoch(manifest, 1, "2026-08-14T11:59:00.000Z", nil)
	epoch.FinishedAt = &finishedAt
	epoch.Result = &completed
	epoch.CompletionReason = &completionReason
	epoch.VerifierConceded = &conceded
	epoch.CheckpointSaved = &checkpointSaved
	manifest.Epochs = append(manifest.Epochs, epoch)
	if err := WriteManifest(store.manifestPath(session.SessionID), manifest); err != nil {
		t.Fatal(err)
	}

	accepted, err := store.Get(session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Reconciliation == nil || accepted.Reconciliation.State != ReconciliationAccepted {
		t.Fatalf("accepted reconciliation: %#v", accepted.Reconciliation)
	}

	manifest.Epochs = append(
		manifest.Epochs,
		NewEpoch(manifest, 2, "2026-08-14T12:00:30.000Z", nil),
	)
	if err := WriteManifest(store.manifestPath(session.SessionID), manifest); err != nil {
		t.Fatal(err)
	}
	retrying, err := store.Get(session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if retrying.Reconciliation == nil || retrying.Reconciliation.State != ReconciliationPending {
		t.Fatalf("retrying reconciliation: %#v", retrying.Reconciliation)
	}

	failed := "failed"
	failure := "verification failed"
	manifest.Epochs[1].FinishedAt = &finishedAt
	manifest.Epochs[1].Result = &failed
	manifest.Epochs[1].Error = &failure
	if err := WriteManifest(store.manifestPath(session.SessionID), manifest); err != nil {
		t.Fatal(err)
	}
	rejected, err := store.Get(session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if rejected.Reconciliation == nil || rejected.Reconciliation.State != ReconciliationFailed ||
		rejected.Reconciliation.Error != failure {
		t.Fatalf("failed reconciliation: %#v", rejected.Reconciliation)
	}

	newVersion := *manifest.CurrentSpecVersion + 1
	newDigest := "sha256:new-package"
	manifest.CurrentSpecVersion = &newVersion
	manifest.PackageDigest = &newDigest
	manifest.Epochs = append(
		manifest.Epochs,
		NewEpoch(manifest, 3, "2026-08-14T12:01:00.000Z", nil),
	)
	if err := WriteManifest(store.manifestPath(session.SessionID), manifest); err != nil {
		t.Fatal(err)
	}
	running, err := store.Get(session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if running.Reconciliation == nil || running.Reconciliation.State != ReconciliationPending {
		t.Fatalf("current reconciliation: %#v", running.Reconciliation)
	}
	if running.Reconciliation.PackageDigest != newDigest {
		t.Fatalf("current package: %#v", running.Reconciliation)
	}
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

func TestNewManifestsRecordRuntimeOnce(t *testing.T) {
	for _, runtime := range []SessionRuntime{RuntimeLocal, RuntimeCloud} {
		store := NewFileStore(t.TempDir(), runtime)
		markdown := "---\nversion: 0.1.0\nname: once\n---\n# Once\n"
		session, err := store.Create(SessionCreateRequest{SpecMarkdown: &markdown})
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(*session.SessionDir, "session.json"))
		if err != nil {
			t.Fatal(err)
		}
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatal(err)
		}
		if raw["runtime"] != string(runtime) {
			t.Fatalf("runtime = %v, want %s", raw["runtime"], runtime)
		}
		if _, ok := raw["launcher"]; ok {
			t.Fatalf("%s manifest records launcher: %s", runtime, data)
		}
		if provenance, _ := raw["provenance"].(map[string]any); provenance == nil || provenance["mode"] != nil {
			t.Fatalf("%s provenance = %v, want an object without mode", runtime, raw["provenance"])
		}
	}
}

func TestManifestRuntimeReadsLegacyProvenanceMode(t *testing.T) {
	legacy := &Manifest{Provenance: map[string]any{"mode": "cloud"}}
	if got := manifestRuntime(legacy, RuntimeLocal); got != RuntimeCloud {
		t.Fatalf("runtime = %s, want %s", got, RuntimeCloud)
	}
}

func TestCloudSessionIDsAcceptGoalAndSessionPrefixes(t *testing.T) {
	for id, want := range map[string]bool{
		"goal_c7d2f0a4e8":  true,
		"sess_c7d2f0a4e8":  true,
		"local_c7d2f0a4e8": false,
		"goal_":            false,
		"c7d2f0a4e8":       false,
	} {
		if got := cloudSessionIDRE.MatchString(id); got != want {
			t.Errorf("cloudSessionIDRE.MatchString(%q) = %v, want %v", id, got, want)
		}
	}
}

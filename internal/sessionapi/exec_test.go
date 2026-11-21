package sessionapi

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

const execTestSessionID = "sess_root"

// writeExecTestSession writes a cloud session manifest with an agent workspace.
func writeExecTestSession(t *testing.T, root, id string, kind SessionKind, parentID *string) {
	t.Helper()
	dir := filepath.Join(root, id)
	specDir := filepath.Join(dir, "specs", id)
	if err := os.MkdirAll(filepath.Join(dir, "workspace"), 0o755); err != nil {
		t.Fatal(err)
	}
	evidence := filepath.Join(specDir, "evidence.jsonl")
	transcript := filepath.Join(specDir, "transcript-"+id+".md")
	m := ManifestFromInitial(InitialManifest{
		SessionID:       id,
		SessionKind:     kind,
		Runtime:         RuntimeCloud,
		CreatedAt:       "2026-11-20T12:00:00.000Z",
		ParentSessionID: parentID,
		SpecName:        id,
		Access:          &ScopedToken{APIToken: "tok_" + id, SubjectSessionID: id, Scopes: []string{string(ScopeSessionsRead)}},
		Specs: []InitialManifestSpec{{
			Name:           id,
			DirName:        id,
			EvidencePath:   &evidence,
			TranscriptPath: &transcript,
		}},
	})
	if err := WriteManifest(filepath.Join(dir, "session.json"), &m); err != nil {
		t.Fatal(err)
	}
}

// newExecTestStore returns a store holding one root session.
func newExecTestStore(t *testing.T) *FileStore {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeExecTestSession(t, root, execTestSessionID, KindController, nil)
	return NewFileStore(root, RuntimeCloud)
}

// execStartRequest is a valid Start request.
func execStartRequest(startID string) ExecStartRequest {
	return ExecStartRequest{StartID: startID, Reason: "Fix", Operator: "ada@example.com"}
}

func mustReadExecJournal(t *testing.T, path string) *execJournal {
	t.Helper()
	j, err := readExecJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

// holdRunnerLock takes the session's runner lock, as a live worker does, and
// returns a function that releases it.
func holdRunnerLock(t *testing.T, sessionDir string) func() {
	t.Helper()
	lock, err := os.OpenFile(filepath.Join(sessionDir, "runner.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	return func() { lock.Close() }
}

func TestExecJournalRecordsEventsAndDerivesState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "exec_000001_0123456789ab", execJournalName)
	j := &execJournal{ExecID: "exec_000001_0123456789ab", SessionID: "sess_root", StartID: "start-1", Reason: "fix", StartedAt: tsNow(), Events: []execEvent{}}
	if err := writeExecJournal(path, j); err != nil {
		t.Fatal(err)
	}
	if got := mustReadExecJournal(t, path).state(); got != ExecStarting {
		t.Fatalf("a journal with no events is %s, want starting", got)
	}
	for _, step := range []struct {
		event execEvent
		want  ExecState
	}{
		{execEvent{Event: execEventSessionStarted}, ExecActive},
		{execEvent{Event: execEventEndRequested, Summary: "Cart loads once."}, ExecEnding},
		{execEvent{Event: execEventSessionEnded}, ExecEnded},
	} {
		if err := appendExecEvent(path, j, step.event); err != nil {
			t.Fatal(err)
		}
		// Every update replaces the document: a fresh read sees the same events.
		read := mustReadExecJournal(t, path)
		if len(read.Events) != len(j.Events) || read.state() != step.want {
			t.Fatalf("after %s: state %s, %d events; want %s", step.event.Event, read.state(), len(read.Events), step.want)
		}
	}
	if got := mustReadExecJournal(t, path).find(execEventEndRequested).Summary; got != "Cart loads once." {
		t.Fatalf("saved summary = %q", got)
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Fatalf("atomic replace left files beside the journal: %v", entries)
	}
	// A takeover that was ended before it became active is ended, not starting.
	if got := (&execJournal{Events: []execEvent{{Event: execEventEndRequested}, {Event: execEventSessionEnded}}}).state(); got != ExecEnded {
		t.Fatalf("ended while starting = %s", got)
	}
}

func TestExecJournalRejectsUnreadableDocuments(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "exec_000001_0123456789ab")
	path := filepath.Join(dir, execJournalName)
	if _, err := readExecJournal(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing journal: %v", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{
		`{"exec_id":"exec_000001_0123456789ab","events":[`,   // cut short
		`{"exec_id":"exec_000002_0123456789ab","events":[]}`, // another takeover's
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readExecJournal(path); err == nil {
			t.Fatalf("journal %q was accepted", content)
		}
	}
}

func TestExecJournalFailedAppendLeavesJournalUnchanged(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := filepath.Join(t.TempDir(), "exec_000001_0123456789ab")
	path := filepath.Join(dir, execJournalName)
	j := &execJournal{ExecID: filepath.Base(dir), Events: []execEvent{}}
	if err := writeExecJournal(path, j); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)
	if err := appendExecEvent(path, j, execEvent{Event: execEventSessionStarted}); err == nil {
		t.Fatal("append to a read-only directory succeeded")
	}
	if read := mustReadExecJournal(t, path); len(read.Events) != 0 || len(j.Events) != 0 {
		t.Fatalf("failed append changed the journal: disk %+v, memory %+v", read.Events, j.Events)
	}
}

func TestAgentTurnRunningNeedsOpenEpochAndLiveWorker(t *testing.T) {
	store := newExecTestStore(t)
	child, other := "sess_child", "sess_other"
	root := execTestSessionID
	writeExecTestSession(t, store.Root, child, KindTask, &root)
	writeExecTestSession(t, store.Root, other, KindController, nil)

	openEpoch := func(id string) {
		t.Helper()
		if _, err := MutateManifest(store.manifestPath(id), func(m *Manifest) error {
			m.Epochs = append(m.Epochs, NewEpoch(m, len(m.Epochs)+1, tsNow(), nil))
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	closeEpochs := func(id string) {
		t.Helper()
		if _, err := MutateManifest(store.manifestPath(id), func(m *Manifest) error {
			now, held := tsNow(), EpochHeld
			for i := range m.Epochs {
				m.Epochs[i].FinishedAt, m.Epochs[i].Result = &now, &held
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}

	if store.agentTurnRunning(root) {
		t.Fatal("a session with no epoch runs no turn")
	}
	openEpoch(root)
	if store.agentTurnRunning(root) {
		t.Fatal("an open epoch with no live worker is stale, not a running turn")
	}
	releaseRoot := holdRunnerLock(t, store.sessionDir(root))
	if !store.agentTurnRunning(root) {
		t.Fatal("an open epoch under a live worker is a running turn")
	}
	closeEpochs(root)
	if store.agentTurnRunning(root) {
		t.Fatal("a worker waiting out the hold, with its epoch closed, runs no turn")
	}
	releaseRoot()

	// A child session's turn counts for its root, and for no other root.
	openEpoch(child)
	defer holdRunnerLock(t, store.sessionDir(child))()
	if !store.agentTurnRunning(root) {
		t.Fatal("a child's running turn must count for its root")
	}
	if store.agentTurnRunning(other) {
		t.Fatal("another goal's turn must not count")
	}
}

func TestExecRefusesSessionIDThatIsNotOneCleanSegment(t *testing.T) {
	store := newExecTestStore(t)
	if _, err := store.Pause(execTestSessionID, true); err != nil {
		t.Fatal(err)
	}
	start := execStartRequest("start-1")
	takeover, err := store.ExecStart(execTestSessionID, start)
	if err != nil {
		t.Fatal(err)
	}
	// Each of these names the root session's directory, or one above it.
	for _, alias := range []string{"", ".", "..", execTestSessionID + "/", execTestSessionID + "/.", "./" + execTestSessionID, execTestSessionID + "/../" + execTestSessionID, "x/../" + execTestSessionID, "/" + execTestSessionID} {
		if _, err := store.ExecStart(alias, start); !errors.Is(err, ErrNotFound) {
			t.Errorf("start on %q = %v, want not found", alias, err)
		}
		if _, err := store.ExecStatus(alias); !errors.Is(err, ErrNotFound) {
			t.Errorf("status of %q = %v, want not found", alias, err)
		}
		if _, err := store.ExecEnd(alias, takeover.ExecID, ExecEndRequest{Summary: "Done"}); !errors.Is(err, ErrNotFound) {
			t.Errorf("end on %q = %v, want not found", alias, err)
		}
	}
	if status, err := store.ExecStatus(execTestSessionID); err != nil || status.State != ExecActive {
		t.Fatalf("status = %+v, %v", status, err)
	}
}

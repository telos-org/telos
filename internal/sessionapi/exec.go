package sessionapi

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// An Exec takeover is a person's turn at a paused root session, in place of
// its agents. This store is the only owner of a takeover. Its record lives in
// the session directory:
//
//	takeover/<exec-id>/exec.json   the journal (see exec_journal.go)

// Limits on the text of a request, in characters. A takeover stores it, and
// every status read returns it.
const (
	execStartIDMaxRunes  = 128
	execOperatorMaxRunes = 320
	execReasonMaxRunes   = 4000
	execSummaryMaxRunes  = 4000
)

// A takeover's ID is exec_<sequence>_<random>. The sequence has a fixed
// width, so IDs sort in the order the takeovers were opened.
const execMaxSeq = 999999

var execIDRE = regexp.MustCompile(`^exec_[0-9]{6}_[0-9a-f]{12}$`)

const execNotPausedDetail = "The session is not paused. Pause it before you start an Exec takeover."

// ExecStore is implemented by stores that support Exec takeovers. FileStore
// does, and so does any store that embeds it.
type ExecStore interface {
	ExecStart(sessionID string, req ExecStartRequest) (*ExecTakeover, error)
	ExecStatus(sessionID string) (*ExecTakeover, error)
	ExecEnd(sessionID, execID string, req ExecEndRequest) (*ExecEndResponse, error)
}

// ExecStartRequest is the body of POST /api/sessions/{id}/exec/start.
type ExecStartRequest struct {
	StartID  string `json:"start_id"`
	Reason   string `json:"reason"`
	Operator string `json:"operator,omitempty"`
}

// ExecTakeover describes an Exec takeover. Start and GET .../exec return it.
type ExecTakeover struct {
	ExecID    string    `json:"exec_id"`
	State     ExecState `json:"state"`
	StartID   string    `json:"start_id,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	Operator  string    `json:"operator,omitempty"`
	StartedAt string    `json:"started_at,omitempty"`
}

// ExecEndRequest is the body of POST /api/sessions/{id}/exec/{exec_id}/end.
type ExecEndRequest struct {
	Summary  string `json:"summary"`
	Operator string `json:"operator,omitempty"`
}

// ExecEndResponse reports an ended takeover.
type ExecEndResponse struct {
	ExecID  string    `json:"exec_id"`
	StartID string    `json:"start_id"`
	State   ExecState `json:"state"`
}

// execError is an Exec failure and the HTTP status that reports it.
type execError struct {
	status int
	detail string
	execID string // the takeover in the way of a competing start
}

func (e *execError) Error() string { return e.detail }

func execErrorf(status int, format string, args ...any) *execError {
	return &execError{status: status, detail: fmt.Sprintf(format, args...)}
}

// execText trims a text field of a request and checks its length.
func execText(name, value string, maxRunes int, required bool) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" && required {
		return "", execErrorf(http.StatusBadRequest, "%s is required.", name)
	}
	if length := utf8.RuneCountInString(value); length > maxRunes {
		return "", execErrorf(http.StatusBadRequest, "%s is %d characters. It can be at most %d.", name, length, maxRunes)
	}
	return value, nil
}

// ExecStart opens an Exec takeover on a paused root session, or continues the
// one start_id already opened. The takeover is starting until no agent turn
// is running, and active after that. A session has one open takeover at most.
func (fs *FileStore) ExecStart(sessionID string, req ExecStartRequest) (*ExecTakeover, error) {
	startID, err := execText("start_id", req.StartID, execStartIDMaxRunes, true)
	if err != nil {
		return nil, err
	}
	reason, err := execText("reason", req.Reason, execReasonMaxRunes, true)
	if err != nil {
		return nil, err
	}
	operator, err := execText("operator", req.Operator, execOperatorMaxRunes, true)
	if err != nil {
		return nil, err
	}

	fs.execMu.Lock()
	defer fs.execMu.Unlock()

	takeoverDir, err := fs.execTakeoverDir(sessionID)
	if err != nil {
		return nil, err
	}
	m, err := fs.execRootManifest(sessionID)
	if err != nil {
		return nil, err
	}
	ids, err := execIDs(takeoverDir)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		path := filepath.Join(takeoverDir, id, execJournalName)
		j, err := readExecJournal(path)
		switch {
		case err != nil:
			// A takeover whose journal can't be read is in nobody's way.
		case j.StartID == startID:
			return fs.continueExecStart(sessionID, m, path, j)
		case j.state() != ExecEnded:
			return nil, &execError{
				status: http.StatusConflict,
				detail: fmt.Sprintf("Exec takeover %s has not ended. End it before you start another takeover.", id),
				execID: id,
			}
		}
	}
	if !m.Paused {
		return nil, execErrorf(http.StatusConflict, execNotPausedDetail)
	}

	seq := 1
	if len(ids) > 0 {
		if last, err := strconv.Atoi(ids[len(ids)-1][5:11]); err == nil {
			seq = last + 1
		}
	}
	if seq > execMaxSeq {
		return nil, execErrorf(http.StatusConflict, "This session can't open another Exec takeover. Its takeover IDs are used up.")
	}
	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return nil, fmt.Errorf("generate exec id: %w", err)
	}
	j := &execJournal{
		ExecID:    fmt.Sprintf("exec_%06d_%s", seq, hex.EncodeToString(suffix[:])),
		SessionID: sessionID,
		StartID:   startID,
		Reason:    reason,
		Operator:  operator,
		StartedAt: tsNow(),
		Events:    []execEvent{},
	}
	path := filepath.Join(takeoverDir, j.ExecID, execJournalName)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create exec takeover: %w", err)
	}
	if err := writeExecJournal(path, j); err != nil {
		_ = os.RemoveAll(filepath.Dir(path))
		return nil, fmt.Errorf("open exec journal: %w", err)
	}
	return fs.continueExecStart(sessionID, m, path, j)
}

// continueExecStart makes a starting takeover active once no agent turn is
// running, and reports the takeover either way.
func (fs *FileStore) continueExecStart(sessionID string, m *Manifest, path string, j *execJournal) (*ExecTakeover, error) {
	if j.state() != ExecStarting {
		return execTakeover(j), nil
	}
	if !m.Paused {
		// The takeover stays open. The error names it, so the caller can end it.
		return nil, &execError{status: http.StatusConflict, detail: execNotPausedDetail, execID: j.ExecID}
	}
	if !fs.agentTurnRunning(sessionID) {
		if err := appendExecEvent(path, j, execEvent{Event: execEventSessionStarted, Timestamp: tsNow()}); err != nil {
			return nil, fmt.Errorf("record exec start: %w", err)
		}
	}
	return execTakeover(j), nil
}

// ExecStatus returns the session's most recent takeover.
func (fs *FileStore) ExecStatus(sessionID string) (*ExecTakeover, error) {
	takeoverDir, err := fs.execTakeoverDir(sessionID)
	if err != nil {
		return nil, err
	}
	ids, err := execIDs(takeoverDir)
	if err != nil {
		return nil, err
	}
	for i := len(ids) - 1; i >= 0; i-- {
		if j, err := readExecJournal(filepath.Join(takeoverDir, ids[i], execJournalName)); err == nil {
			return execTakeover(j), nil
		}
	}
	return nil, execErrorf(http.StatusNotFound, "Session %s has no Exec takeover.", sessionID)
}

func execTakeover(j *execJournal) *ExecTakeover {
	return &ExecTakeover{
		ExecID:    j.ExecID,
		State:     j.state(),
		StartID:   j.StartID,
		Reason:    j.Reason,
		Operator:  j.Operator,
		StartedAt: j.StartedAt,
	}
}

// ExecEnd ends a takeover and saves its summary. A takeover that is still
// starting can be ended too. Repeating End returns the same result.
func (fs *FileStore) ExecEnd(sessionID, execID string, req ExecEndRequest) (*ExecEndResponse, error) {
	summary, err := execText("summary", req.Summary, execSummaryMaxRunes, true)
	if err != nil {
		return nil, err
	}
	operator, err := execText("operator", req.Operator, execOperatorMaxRunes, false)
	if err != nil {
		return nil, err
	}

	fs.execMu.Lock()
	defer fs.execMu.Unlock()

	path, j, err := fs.loadExec(sessionID, execID)
	if err != nil {
		return nil, err
	}
	if j.find(execEventEndRequested) == nil && j.find(execEventSessionEnded) == nil {
		if err := appendExecEvent(path, j, execEvent{
			Event:     execEventEndRequested,
			Timestamp: tsNow(),
			Operator:  operator,
			Summary:   summary,
		}); err != nil {
			return nil, fmt.Errorf("record end request: %w", err)
		}
	}
	if j.find(execEventSessionEnded) == nil {
		if err := appendExecEvent(path, j, execEvent{Event: execEventSessionEnded, Timestamp: tsNow()}); err != nil {
			return nil, fmt.Errorf("close exec journal: %w", err)
		}
	}
	return &ExecEndResponse{ExecID: j.ExecID, StartID: j.StartID, State: ExecEnded}, nil
}

// execRootManifest reads the manifest of a session that can be taken over:
// like pause, only a root session.
func (fs *FileStore) execRootManifest(sessionID string) (*Manifest, error) {
	m, err := ReadManifest(fs.manifestPath(sessionID))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("session %s: %w", sessionID, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	if !isRootController(m) {
		return nil, fmt.Errorf("only root sessions support Exec: %w", ErrInvalidSession)
	}
	return m, nil
}

// execTakeoverDir returns where an existing session keeps its takeovers.
// Every Exec request resolves its session here, so this is where a session ID
// that is not one clean path segment is refused: "sess_1/." would name the
// same directory as "sess_1" and match none of the checks made by ID.
func (fs *FileStore) execTakeoverDir(sessionID string) (string, error) {
	if sessionID == "" || sessionID == "." || !filepath.IsLocal(sessionID) || filepath.Base(sessionID) != sessionID || !fileExists(fs.manifestPath(sessionID)) {
		return "", fmt.Errorf("session %q: %w", sessionID, ErrNotFound)
	}
	return filepath.Join(fs.sessionDir(sessionID), "takeover"), nil
}

// execIDs lists a session's takeovers, oldest first: IDs start with a
// fixed-width sequence number, and ReadDir sorts by name.
func execIDs(takeoverDir string) ([]string, error) {
	entries, err := os.ReadDir(takeoverDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, entry := range entries {
		if entry.IsDir() && execIDRE.MatchString(entry.Name()) {
			ids = append(ids, entry.Name())
		}
	}
	return ids, nil
}

// loadExec reads a takeover's journal and returns its path with it. A journal
// that can't be read is a conflict: nothing can be done with that takeover,
// and it is in nobody's way.
func (fs *FileStore) loadExec(sessionID, execID string) (string, *execJournal, error) {
	takeoverDir, err := fs.execTakeoverDir(sessionID)
	if err != nil {
		return "", nil, err
	}
	path := filepath.Join(takeoverDir, execID, execJournalName)
	if !execIDRE.MatchString(execID) || !fileExists(filepath.Dir(path)) {
		return "", nil, execErrorf(http.StatusNotFound, "Exec takeover %s was not found.", execID)
	}
	j, err := readExecJournal(path)
	if err != nil {
		return "", nil, execErrorf(http.StatusConflict, "The journal of Exec takeover %s can't be read. Start a new takeover.", execID)
	}
	return path, j, nil
}

// agentTurnRunning reports whether a live worker has an epoch open for the
// root session or one of its children. A hold stops the game loop, which
// closes its epoch once the interrupted turn has returned and the workspace
// checkpoint is written, so a paused goal with no such epoch runs no agent
// turn. An epoch that opens later sees the hold before its first turn.
//
// The check errs toward true: an epoch that a crashed worker left open still
// counts while a restarted worker waits out the hold, so Start stays
// `starting` until the pause lifts and that worker finishes the epoch. An
// open epoch with no live worker does not count.
func (fs *FileStore) agentTurnRunning(rootID string) bool {
	entries, err := os.ReadDir(fs.Root)
	if err != nil {
		return true
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		m, err := ReadManifest(fs.manifestPath(entry.Name()))
		if err != nil || (entry.Name() != rootID && strValue(m.ParentSessionID) != rootID) {
			continue
		}
		if m.OpenEpoch() == nil {
			continue
		}
		if held, err := runnerLockHeld(fs.sessionDir(entry.Name())); err != nil || held {
			return true
		}
	}
	return false
}

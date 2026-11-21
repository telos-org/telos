package sessionapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ExecState is the lifecycle state of an Exec takeover.
type ExecState string

const (
	ExecStarting ExecState = "starting"
	ExecActive   ExecState = "active"
	ExecEnding   ExecState = "ending"
	ExecEnded    ExecState = "ended"
)

// Journal events, in the order a takeover writes them.
const (
	execEventSessionStarted = "session_started" // no agent turn is running
	execEventEndRequested   = "end_requested"   // holds the summary
	execEventSessionEnded   = "session_ended"   // written last; the only sign End finished
)

const execJournalName = "exec.json"

// execJournal is takeover/<exec-id>/exec.json, the record of one Exec
// takeover. Every update replaces the whole document and syncs it to disk.
type execJournal struct {
	ExecID    string      `json:"exec_id"`
	SessionID string      `json:"session_id"`
	StartID   string      `json:"start_id"`
	Reason    string      `json:"reason"`
	Operator  string      `json:"operator,omitempty"`
	StartedAt string      `json:"started_at"`
	Events    []execEvent `json:"events"`
}

// execEvent is one journal entry. Which fields are set depends on Event.
type execEvent struct {
	Event     string `json:"event"`
	Timestamp string `json:"ts"`
	Operator  string `json:"operator,omitempty"`

	// end_requested
	Summary string `json:"summary,omitempty"`
}

func readExecJournal(path string) (*execJournal, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var j execJournal
	if err := json.Unmarshal(data, &j); err != nil {
		return nil, fmt.Errorf("parse exec journal %s: %w", path, err)
	}
	if j.ExecID != filepath.Base(filepath.Dir(path)) {
		return nil, fmt.Errorf("exec journal %s names takeover %q", path, j.ExecID)
	}
	return &j, nil
}

// writeExecJournal atomically replaces the journal and syncs it and its
// directory, so an event recorded in a takeover survives a crash. A takeover
// that a crash loses before its first sync is opened again by the repeated
// Start.
func writeExecJournal(path string, j *execJournal) error {
	var data bytes.Buffer
	enc := json.NewEncoder(&data)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(j); err != nil {
		return err
	}
	if err := writeFileAtomic(path, data.Bytes(), 0o600); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

// appendExecEvent records event durably. On failure j is unchanged, so it
// still matches the journal on disk.
func appendExecEvent(path string, j *execJournal, event execEvent) error {
	next := *j
	next.Events = append(append(make([]execEvent, 0, len(j.Events)+1), j.Events...), event)
	if err := writeExecJournal(path, &next); err != nil {
		return err
	}
	j.Events = next.Events
	return nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// find returns the first event with the given name.
func (j *execJournal) find(event string) *execEvent {
	for i := range j.Events {
		if j.Events[i].Event == event {
			return &j.Events[i]
		}
	}
	return nil
}

// state derives the takeover's state from the events recorded so far.
func (j *execJournal) state() ExecState {
	switch {
	case j.find(execEventSessionEnded) != nil:
		return ExecEnded
	case j.find(execEventEndRequested) != nil:
		return ExecEnding
	case j.find(execEventSessionStarted) != nil:
		return ExecActive
	default:
		return ExecStarting
	}
}

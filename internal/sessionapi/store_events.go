package sessionapi

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type StoreEventType string

const (
	EventStopRequested       StoreEventType = "stop_requested"
	EventStopSignalSent      StoreEventType = "stop_signal_sent"
	EventProcessExitObserved StoreEventType = "process_exit_observed"
	EventForcedKill          StoreEventType = "forced_kill"
	EventScopedTokenRevoked  StoreEventType = "scoped_token_revoked"
)

type StoreEvent struct {
	Sequence  int64           `json:"sequence"`
	SessionID string          `json:"session_id"`
	Type      StoreEventType  `json:"type"`
	CreatedAt string          `json:"created_at"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

func (fs *FileStore) storeEventLogPath(sessionID string) string {
	return filepath.Join(fs.sessionDir(sessionID), "events.jsonl")
}

func (fs *FileStore) AppendStoreEvent(sessionID string, eventType StoreEventType, payload map[string]any) (StoreEvent, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	var event StoreEvent
	err := fs.withStoreLock(func() error {
		var err error
		event, err = fs.appendStoreEventLocked(sessionID, eventType, payload)
		return err
	})
	return event, err
}

func (fs *FileStore) appendStoreEventLocked(sessionID string, eventType StoreEventType, payload map[string]any) (StoreEvent, error) {
	if strings.TrimSpace(sessionID) == "" {
		return StoreEvent{}, fmt.Errorf("session id is required")
	}
	if strings.TrimSpace(string(eventType)) == "" {
		return StoreEvent{}, fmt.Errorf("store event type is required")
	}
	path := fs.storeEventLogPath(sessionID)
	lastSeq, err := lastStoreEventSequence(path)
	if err != nil {
		return StoreEvent{}, err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return StoreEvent{}, fmt.Errorf("encode store event payload: %w", err)
	}
	if string(raw) == "null" {
		raw = nil
	}
	event := StoreEvent{
		Sequence:  lastSeq + 1,
		SessionID: sessionID,
		Type:      eventType,
		CreatedAt: tsNow(),
		Payload:   raw,
	}
	data, err := json.Marshal(event)
	if err != nil {
		return StoreEvent{}, fmt.Errorf("encode store event: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return StoreEvent{}, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return StoreEvent{}, fmt.Errorf("append store event: %w", err)
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		_ = file.Close()
		return StoreEvent{}, fmt.Errorf("append store event: %w", err)
	}
	// Store events are audit-critical: callers must see append/fsync failures
	// instead of continuing with an unrecorded stop or token-revocation action.
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return StoreEvent{}, fmt.Errorf("sync store event: %w", err)
	}
	if err := file.Close(); err != nil {
		return StoreEvent{}, fmt.Errorf("close store event: %w", err)
	}
	if err := syncDir(filepath.Dir(path)); err != nil {
		return StoreEvent{}, err
	}
	return event, nil
}

func lastStoreEventSequence(path string) (int64, error) {
	events, err := readStoreEvents(path)
	if err != nil {
		return 0, err
	}
	for i, event := range events {
		want := int64(i + 1)
		if event.Sequence != want {
			return 0, fmt.Errorf("store event sequence integrity: got %d at index %d, want %d", event.Sequence, i, want)
		}
	}
	if len(events) == 0 {
		return 0, nil
	}
	return events[len(events)-1].Sequence, nil
}

func CheckStoreEventLogIntegrity(path string) error {
	events, err := readStoreEvents(path)
	if err != nil {
		return err
	}
	for i, event := range events {
		want := int64(i + 1)
		if event.Sequence != want {
			return fmt.Errorf("store event sequence integrity: got %d at index %d, want %d", event.Sequence, i, want)
		}
	}
	return nil
}

func readStoreEvents(path string) ([]StoreEvent, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read store events: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var events []StoreEvent
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event StoreEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return nil, fmt.Errorf("parse store event %s:%d: %w", path, lineNo, err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan store events %s: %w", path, err)
	}
	return events, nil
}

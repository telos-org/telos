// Package evidence handles append-only PVG evidence JSONL persistence.
package evidence

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

const SchemaVersion = "telos.evidence.v2"

// Evidence is an append-only writer for PVG evidence logs.
type Evidence struct {
	SystemName string
	SessionID  string
	EpochID    int
	StartedAt  string
	Path       string
	Dir        string

	mu       sync.Mutex
	eventSeq int
}

// New creates a new Evidence writer.
func New(systemName string, path string, sessionID string, epochID int) *Evidence {
	dir := filepath.Dir(path)
	os.MkdirAll(dir, 0o755)
	return &Evidence{
		SystemName: systemName,
		SessionID:  sessionID,
		EpochID:    epochID,
		StartedAt:  tsNow(),
		Path:       path,
		Dir:        dir,
		eventSeq:   lastEventSeq(path),
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// HasActivity reports whether an evidence log has a display log holding all
// of its rows: one begun with it and never removed. Logs an older runtime
// began have none. (A log later appended to by an older runtime again would
// miss those rows.)
func HasActivity(path string) bool {
	return fileExists(ActivityPath(path)) || isEmpty(path)
}

// isEmpty reports an evidence log with no events yet, including a missing one.
func isEmpty(path string) bool {
	info, err := os.Stat(path)
	return err != nil || info.Size() == 0
}

// Log writes a single best-effort evidence event.
func (e *Evidence) Log(event string, roundNum int, role string, data map[string]interface{}) {
	_ = e.log(event, roundNum, role, data)
}

func (e *Evidence) log(event string, roundNum int, role string, data map[string]interface{}) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	lock, err := lockEvidenceFile(e.Path)
	if err != nil {
		return err
	}
	defer unlockEvidenceFile(lock)

	if lastSeq := lastEventSeq(e.Path); lastSeq > e.eventSeq {
		e.eventSeq = lastSeq
	}
	// The display log begins with the evidence log, and holds a row for every
	// user-facing event in it, or is removed.
	activityPath := ActivityPath(e.Path)
	if isEmpty(e.Path) {
		if f, err := os.OpenFile(activityPath, os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			f.Close()
		}
	} else if fileExists(activityPath) {
		syncActivity(e.Path, activityPath)
	}
	e.eventSeq++
	record := map[string]interface{}{
		"schema":             SchemaVersion,
		"session_id":         e.SessionID,
		"event_id":           fmt.Sprintf("%s:%s:%08d", e.SessionID, e.SystemName, e.eventSeq),
		"event_seq":          e.eventSeq,
		"epoch_id":           e.EpochID,
		"session_started_at": e.StartedAt,
		"ts":                 tsNow(),
		"event":              event,
		"system":             e.SystemName,
		"round":              roundNum,
		"role":               role,
		"data":               data,
	}
	if data == nil {
		record["data"] = map[string]interface{}{}
	}

	if err := appendLine(e.Path, record); err != nil {
		return err
	}
	if row := activityRow(record); row != nil && fileExists(activityPath) {
		appendActivity(activityPath, row, e.eventSeq)
	}
	return nil
}

// SyncActivity restores the newest event's row when its writer stopped before
// writing it, such as after a session's final event, then reports whether
// the display log still holds all of its rows. Only a log missing that row
// takes the evidence lock.
func SyncActivity(path string) bool {
	activityPath := ActivityPath(path)
	if fileExists(activityPath) {
		if row, _ := missingRow(path, activityPath); row != nil {
			if lock, err := lockEvidenceFile(path); err == nil {
				syncActivity(path, activityPath)
				unlockEvidenceFile(lock)
			}
		}
	}
	return HasActivity(path)
}

// syncActivity restores the newest event's row; callers hold the evidence
// lock. Writers run it before each write, so only the newest event can lack
// its row.
func syncActivity(path, activityPath string) {
	if row, seq := missingRow(path, activityPath); row != nil {
		appendActivity(activityPath, row, seq)
	}
}

// missingRow returns the newest evidence event's row, and that event's
// event_seq, when the display log lacks it.
func missingRow(path, activityPath string) (map[string]interface{}, int) {
	last := lastRecord(path)
	row := activityRow(last)
	seq, _ := last["event_seq"].(float64)
	synced, _ := lastRecord(activityPath)["evidence_seq"].(float64)
	if row == nil || seq <= synced {
		return nil, 0
	}
	return row, int(seq)
}

// appendActivity appends the row of evidence event evidenceSeq. A display log
// it cannot complete is removed, so readers serve the raw events instead.
func appendActivity(activityPath string, row map[string]interface{}, evidenceSeq int) {
	row["event_seq"] = lastEventSeq(activityPath) + 1
	row["evidence_seq"] = evidenceSeq
	if appendLine(activityPath, row) != nil {
		os.Remove(activityPath)
	}
}

func appendLine(path string, value any) error {
	line, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	line = append(line, '\n')
	if needsLeadingNewline(path) {
		line = append([]byte{'\n'}, line...)
	}
	if _, err := f.Write(line); err != nil {
		return err
	}
	return f.Sync()
}

func needsLeadingNewline(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return false
	}
	last := []byte{0}
	if _, err := f.ReadAt(last, info.Size()-1); err != nil {
		return false
	}
	return last[0] != '\n'
}

// LogAgent logs an agent completion event.
func (e *Evidence) LogAgent(roundNum int, role string, status string, logsTail string, stats interface{}) {
	data := map[string]interface{}{
		"status":    status,
		"logs_tail": truncate(logsTail, 8000),
	}
	if stats != nil {
		// Marshal stats to map
		b, err := json.Marshal(stats)
		if err == nil {
			var m map[string]interface{}
			if json.Unmarshal(b, &m) == nil {
				for k, v := range m {
					data[k] = v
				}
			}
		}
	}
	// Technical turn completion is distinct from runtime-confirmed completion.
	data["audience"] = "agent"
	e.Log("agent_complete", roundNum, role, data)
}

// LogGameEnd logs the terminal game result.
func (e *Evidence) LogGameEnd(result string, rounds, proverRounds, verifierRounds int, conceded bool, costUSD float64, inputTokens, outputTokens, cacheRead, cacheCreate int, errMsg string, completionReason string) {
	e.Log("game_end", rounds, "system", map[string]interface{}{
		"audience":                    "user",
		"game_result":                 result,
		"completion_reason":           completionReason,
		"prover_rounds":               proverRounds,
		"verifier_rounds":             verifierRounds,
		"verifier_conceded":           conceded,
		"total_cost_usd":              costUSD,
		"total_input_tokens":          inputTokens,
		"total_output_tokens":         outputTokens,
		"total_cache_read_tokens":     cacheRead,
		"total_cache_creation_tokens": cacheCreate,
		"error":                       errMsg,
	})
}

// LogWorkspaceCheckpoint logs a workspace checkpoint event.
func (e *Evidence) LogWorkspaceCheckpoint(roundNum int, path string) {
	data := map[string]interface{}{"path": path}
	info, err := os.Stat(path)
	if err == nil {
		data["bytes"] = info.Size()
	} else {
		data["missing"] = true
	}
	e.Log("workspace_checkpoint", roundNum, "system", data)
}

// Close is a no-op; evidence is flushed on each write.
func (e *Evidence) Close() {}

func lastRecord(path string) map[string]interface{} {
	var last map[string]interface{}
	visitEvidenceLinesReverse(path, func(record map[string]interface{}) bool {
		last = record
		return true
	})
	return last
}

func lastEventSeq(path string) int {
	seq := 0
	visitEvidenceLinesReverse(path, func(record map[string]interface{}) bool {
		value, ok := record["event_seq"].(float64)
		if !ok {
			return false
		}
		seq = int(value)
		return true
	})
	return seq
}

func visitEvidenceLinesReverse(path string, visit func(map[string]interface{}) bool) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return
	}
	_ = VisitLinesReverse(f, info.Size(), visit)
}

// VisitLinesReverse reads a fixed snapshot from the end, stopping when visit
// returns true. Invalid JSON and blank lines are skipped. Readers can bound a
// tail request without decoding or indexing the file's older history.
func VisitLinesReverse(reader io.ReaderAt, size int64, visit func(map[string]any) bool) error {
	const chunkSize int64 = 64 * 1024
	end := size
	var carry []byte
	for end > 0 {
		start := max(end-chunkSize, 0)
		chunk := make([]byte, end-start)
		if _, err := reader.ReadAt(chunk, start); err != nil {
			return err
		}
		combined := append(chunk, carry...)
		lines := bytes.Split(combined, []byte{'\n'})
		firstComplete := 0
		if start > 0 {
			firstComplete = 1
		}
		for i := len(lines) - 1; i >= firstComplete; i-- {
			line := bytes.TrimSpace(lines[i])
			if len(line) == 0 {
				continue
			}
			var record map[string]interface{}
			if json.Unmarshal(line, &record) == nil && visit(record) {
				return nil
			}
		}
		carry = append([]byte(nil), lines[0]...)
		end = start
	}
	return nil
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func lockEvidenceFile(path string) (*os.File, error) {
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		_ = lock.Close()
		return nil, err
	}
	return lock, nil
}

func unlockEvidenceFile(lock *os.File) {
	_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	_ = lock.Close()
}

func tsNow() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[len(s)-max:]
}

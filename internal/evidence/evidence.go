// Package evidence handles append-only PVG evidence JSONL persistence.
package evidence

import (
	"encoding/json"
	"fmt"
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

// Log writes a single evidence event.
func (e *Evidence) Log(event string, roundNum int, role string, data map[string]interface{}) {
	e.mu.Lock()
	defer e.mu.Unlock()

	lock, err := lockEvidenceFile(e.Path)
	if err != nil {
		return
	}
	defer unlockEvidenceFile(lock)

	if lastSeq := lastEventSeq(e.Path); lastSeq > e.eventSeq {
		e.eventSeq = lastSeq
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

	line, _ := json.Marshal(record)
	f, err := os.OpenFile(e.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(line)
	f.Write([]byte("\n"))
	f.Sync()
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
	e.Log("agent_complete", roundNum, role, data)
}

// LogGameEnd logs the terminal game result.
func (e *Evidence) LogGameEnd(result string, rounds, proverRounds, verifierRounds int, conceded bool, costUSD float64, inputTokens, outputTokens, cacheRead, cacheCreate int, errMsg string, completionReason string) {
	e.Log("game_end", rounds, "system", map[string]interface{}{
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

func lastEventSeq(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	lines := splitLines(string(data))
	for i := len(lines) - 1; i >= 0; i-- {
		if lines[i] == "" {
			continue
		}
		var m map[string]interface{}
		if json.Unmarshal([]byte(lines[i]), &m) == nil {
			if seq, ok := m["event_seq"].(float64); ok {
				return int(seq)
			}
		}
		break
	}
	return 0
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

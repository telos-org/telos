package executor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/telos-org/telos/internal/game"
)

// -- Agent session contract --------------------------------------------------
//
// These types are the single typed contract for the per-turn session JSONL,
// written by nativeSessionLogger.

type sessionEvent struct {
	Type      string          `json:"type"`
	Version   int             `json:"version,omitempty"`
	ID        string          `json:"id,omitempty"`
	Timestamp string          `json:"timestamp,omitempty"`
	CWD       string          `json:"cwd,omitempty"`
	Runtime   string          `json:"runtime,omitempty"`
	Message   *sessionMessage `json:"message,omitempty"`
}

type sessionMessage struct {
	Role         string           `json:"role"`
	Timestamp    int64            `json:"timestamp,omitempty"`
	Provider     string           `json:"provider,omitempty"`
	Model        string           `json:"model,omitempty"`
	StopReason   string           `json:"stopReason,omitempty"`
	Content      []sessionContent `json:"content,omitempty"`
	Usage        *sessionUsage    `json:"usage,omitempty"`
	ToolCallID   string           `json:"toolCallId,omitempty"`
	ToolName     string           `json:"toolName,omitempty"`
	IsError      bool             `json:"isError,omitempty"`
	ErrorMessage string           `json:"errorMessage,omitempty"`
}

type sessionContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type sessionUsage struct {
	Input      int          `json:"input"`
	Output     int          `json:"output"`
	CacheRead  int          `json:"cacheRead"`
	CacheWrite int          `json:"cacheWrite"`
	Cost       *sessionCost `json:"cost,omitempty"`
}

type sessionCost struct {
	Total float64 `json:"total"`
}

// -- Session logging ---------------------------------------------------------

type nativeSessionLogger struct {
	path      string
	workspace string
}

func newNativeSessionLogger(path, workspace string) *nativeSessionLogger {
	return &nativeSessionLogger{path: path, workspace: workspace}
}

func (l *nativeSessionLogger) start() error {
	if l.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return err
	}
	return l.append(sessionEvent{
		Type:      "session",
		Version:   1,
		ID:        fmt.Sprintf("native-%d", time.Now().UnixNano()),
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		CWD:       l.workspace,
		Runtime:   "telos-native",
	})
}

func (l *nativeSessionLogger) user(text string) error {
	return l.message(&sessionMessage{
		Role:    "user",
		Content: []sessionContent{{Type: "text", Text: text}},
	})
}

func (l *nativeSessionLogger) assistant(text, provider, model, stopReason string, stats game.TurnStats) error {
	return l.message(&sessionMessage{
		Role:       "assistant",
		Provider:   provider,
		Model:      model,
		StopReason: stopReason,
		Content:    []sessionContent{{Type: "text", Text: text}},
		Usage: &sessionUsage{
			Input:      stats.InputTokens,
			Output:     stats.OutputTokens,
			CacheRead:  stats.CacheReadTokens,
			CacheWrite: stats.CacheCreationTokens,
			Cost:       &sessionCost{Total: stats.CostUSD},
		},
	})
}

func (l *nativeSessionLogger) tool(result nativeToolResult) error {
	return l.message(&sessionMessage{
		Role:       "toolResult",
		ToolCallID: result.CallID,
		ToolName:   result.Name,
		IsError:    result.IsError,
		Content:    []sessionContent{{Type: "text", Text: result.Output}},
	})
}

func (l *nativeSessionLogger) message(msg *sessionMessage) error {
	if l.path == "" {
		return nil
	}
	msg.Timestamp = time.Now().UnixMilli()
	return l.append(sessionEvent{
		Type:      "message",
		ID:        fmt.Sprintf("%s-%d", msg.Role, time.Now().UnixNano()),
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Message:   msg,
	})
}

func (l *nativeSessionLogger) append(event sessionEvent) error {
	if l.path == "" {
		return nil
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(event)
}

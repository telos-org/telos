// Package executor provides Telos agent executors and session parsing.
package executor

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/telos-org/telos/internal/game"
)

// -- Agent session contract --------------------------------------------------
//
// These types are the single typed contract for the per-turn session JSONL.
// Both the writer (nativeSessionLogger) and the reader (ReadSession) use them,
// so the two halves cannot silently drift.

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

// -- Agent session parsing ---------------------------------------------------

type SessionSummary struct {
	Logs  string
	Stats game.TurnStats
	Error string
}

// ReadSession reads the per-turn agent session JSONL file.
func ReadSession(path string) (SessionSummary, error) {
	f, err := os.Open(path)
	if err != nil {
		return SessionSummary{}, err
	}
	defer f.Close()

	var summary SessionSummary
	var finalAssistant *sessionMessage
	reader := bufio.NewReader(f)
	for {
		line, readErr := reader.ReadString('\n')
		if strings.TrimSpace(line) != "" {
			var event sessionEvent
			if json.Unmarshal([]byte(line), &event) == nil && event.Message != nil {
				switch event.Message.Role {
				case "assistant":
					finalAssistant = event.Message
					summary.Stats = mergeTurnStats(summary.Stats, statsFromSessionMessage(event.Message))
				case "toolResult", "bashExecution":
					summary.Stats.NumTurns++
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return SessionSummary{}, readErr
		}
	}
	if finalAssistant == nil {
		return SessionSummary{}, fmt.Errorf("no assistant message in agent session")
	}

	summary.Logs = assistantText(finalAssistant)
	summary.Error = errorFromSessionMessage(finalAssistant)
	return summary, nil
}

func assistantText(msg *sessionMessage) string {
	var parts []string
	for _, block := range msg.Content {
		if block.Type != "text" {
			continue
		}
		if strings.TrimSpace(block.Text) != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "")
}

func statsFromSessionMessage(msg *sessionMessage) game.TurnStats {
	stats := game.TurnStats{Model: msg.Model}
	if msg.Usage == nil {
		return stats
	}
	stats.InputTokens += msg.Usage.Input
	stats.OutputTokens += msg.Usage.Output
	stats.CacheReadTokens += msg.Usage.CacheRead
	stats.CacheCreationTokens += msg.Usage.CacheWrite
	if msg.Usage.Cost != nil {
		stats.CostUSD += msg.Usage.Cost.Total
	}
	return stats
}

func errorFromSessionMessage(msg *sessionMessage) string {
	if msg.StopReason == "length" {
		return "agent_output_truncated:length"
	}
	if msg.ErrorMessage == "" || isTransientError(msg.ErrorMessage) {
		return ""
	}
	return msg.ErrorMessage
}

func mergeTurnStats(base, extra game.TurnStats) game.TurnStats {
	base.CostUSD += extra.CostUSD
	base.DurationMS += extra.DurationMS
	base.NumTurns += extra.NumTurns
	base.InputTokens += extra.InputTokens
	base.OutputTokens += extra.OutputTokens
	base.CacheReadTokens += extra.CacheReadTokens
	base.CacheCreationTokens += extra.CacheCreationTokens
	if base.Model == "" {
		base.Model = extra.Model
	}
	return base
}

func isTransientError(err string) bool {
	for _, t := range []string{"overloaded_error", "rate_limit_error", "api_error"} {
		if strings.Contains(err, t) {
			return true
		}
	}
	return false
}

func orDefault(s, def string) string {
	if s != "" {
		return s
	}
	return def
}

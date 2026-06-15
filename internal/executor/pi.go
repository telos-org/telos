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

// -- Agent session parsing ----------------------------------------------------

type PiSessionSummary struct {
	Logs  string
	Stats game.TurnStats
	Error string
}

// ReadPiSession reads Telos' historical per-turn session JSONL file.
func ReadPiSession(path string) (PiSessionSummary, error) {
	f, err := os.Open(path)
	if err != nil {
		return PiSessionSummary{}, err
	}
	defer f.Close()

	var summary PiSessionSummary
	var finalAssistant map[string]interface{}
	reader := bufio.NewReader(f)
	for {
		line, err := reader.ReadString('\n')
		if strings.TrimSpace(line) != "" {
			var entry map[string]interface{}
			dec := json.NewDecoder(strings.NewReader(line))
			dec.UseNumber()
			if dec.Decode(&entry) == nil {
				if msg, ok := entry["message"].(map[string]interface{}); ok {
					switch getString(msg, "role") {
					case "assistant":
						finalAssistant = msg
						summary.Stats = mergeTurnStats(summary.Stats, statsFromPiMessage(msg))
					case "toolResult", "bashExecution":
						summary.Stats.NumTurns++
					}
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return PiSessionSummary{}, err
		}
	}
	if finalAssistant == nil {
		return PiSessionSummary{}, fmt.Errorf("no assistant message in pi session")
	}

	summary.Logs = assistantText(finalAssistant)
	summary.Error = errorFromPiMessage(finalAssistant)
	return summary, nil
}

func assistantText(msg map[string]interface{}) string {
	content, _ := msg["content"].([]interface{})
	var parts []string
	for _, block := range content {
		bm, ok := block.(map[string]interface{})
		if !ok {
			continue
		}
		if getString(bm, "type") != "text" {
			continue
		}
		text := getString(bm, "text")
		if strings.TrimSpace(text) != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "")
}

func statsFromPiMessage(msg map[string]interface{}) game.TurnStats {
	stats := game.TurnStats{}
	if model := getString(msg, "model"); model != "" {
		stats.Model = model
	}
	usage, _ := msg["usage"].(map[string]interface{})
	if usage == nil {
		return stats
	}
	stats.InputTokens += intFromAny(usage["input"])
	stats.OutputTokens += intFromAny(usage["output"])
	stats.CacheReadTokens += intFromAny(usage["cacheRead"])
	stats.CacheCreationTokens += intFromAny(usage["cacheWrite"])
	cost, _ := usage["cost"].(map[string]interface{})
	if cost != nil {
		stats.CostUSD += floatFromAny(cost["total"])
	}
	return stats
}

func errorFromPiMessage(msg map[string]interface{}) string {
	if getString(msg, "stopReason") == "length" {
		return "agent_output_truncated:length"
	}
	return extractMessageError(msg)
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

func extractMessageError(msg map[string]interface{}) string {
	em := getString(msg, "errorMessage")
	if em == "" {
		return ""
	}
	if isTransientError(em) {
		return ""
	}
	return em
}

func isTransientError(err string) bool {
	for _, t := range []string{"overloaded_error", "rate_limit_error", "api_error"} {
		if strings.Contains(err, t) {
			return true
		}
	}
	return false
}

func getString(m map[string]interface{}, key string) string {
	v, _ := m[key].(string)
	return v
}

func intFromAny(v interface{}) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	}
	return 0
}

func floatFromAny(v interface{}) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	}
	return 0
}

func orDefault(s, def string) string {
	if s != "" {
		return s
	}
	return def
}

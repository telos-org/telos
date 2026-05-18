// Package executor provides the Pi executor for PVG turns.
package executor

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/telos-org/telos-go/internal/game"
	"github.com/telos-org/telos-go/internal/platform"
)

// PiExecutor runs Pi as one PVG agent turn on the given LocalPlatform.
type PiExecutor struct {
	Platform *platform.LocalPlatform
	Model    string
	Thinking string
	Timeout  int
}

// NewPiExecutor creates a new Pi executor.
func NewPiExecutor(p *platform.LocalPlatform, model, thinking string, timeout int) *PiExecutor {
	if thinking == "" {
		thinking = "medium"
	}
	if timeout <= 0 {
		timeout = 1800
	}
	return &PiExecutor{
		Platform: p,
		Model:    model,
		Thinking: thinking,
		Timeout:  timeout,
	}
}

// ExecuteTurn runs one Pi agent turn.
func (pe *PiExecutor) ExecuteTurn(task string, role string, turnState *game.TurnState) game.TurnResult {
	var textParts []string
	var stats game.TurnStats
	stats.Model = pe.Model
	var agentError string
	var stopReason string
	var rawLogPath string
	var taskPath string
	if turnState != nil {
		rawLogPath = turnState.RawLogPath()
		taskPath = turnState.TaskPath()
		if err := os.MkdirAll(turnState.Dir, 0o755); err != nil {
			return game.TurnResult{
				Role:   role,
				Status: game.StatusContinue,
				Logs:   fmt.Sprintf("turn_state_mkdir_failed:%v", err),
				Stats:  stats,
				Error:  fmt.Sprintf("turn_state_mkdir_failed:%v", err),
			}
		}
		if task != "" {
			if err := os.WriteFile(taskPath, []byte(task), 0o644); err != nil {
				return game.TurnResult{
					Role:   role,
					Status: game.StatusContinue,
					Logs:   fmt.Sprintf("turn_task_write_failed:%v", err),
					Stats:  stats,
					Error:  fmt.Sprintf("turn_task_write_failed:%v", err),
				}
			}
		}
		if err := os.WriteFile(rawLogPath, nil, 0o644); err != nil {
			return game.TurnResult{
				Role:   role,
				Status: game.StatusContinue,
				Logs:   fmt.Sprintf("raw_log_write_failed:%v", err),
				Stats:  stats,
				Error:  fmt.Sprintf("raw_log_write_failed:%v", err),
			}
		}
	}

	onLine := func(line string) {
		if rawLogPath != "" {
			AppendRawLogLine(rawLogPath, line)
		}
		event := ParsePiJSONLine(line)
		if event == nil {
			return
		}
		if agentError == "" {
			agentError = ExtractPiEventError(event)
		}
		if sr := ExtractPiStopReason(event); sr != "" {
			stopReason = sr
		}
		HandlePiEvent(event, &textParts, &stats)
	}

	argv := BuildPiArgv(pe.Model, pe.Thinking, taskPath)
	taskEnv := task
	if taskPath != "" {
		taskEnv = ""
	}
	result := pe.Platform.Run(argv, taskEnv, map[string]string{"TELOS_ROLE": role}, pe.Timeout, onLine)
	if stopReason == "length" {
		agentError = "agent_output_truncated:length"
	}

	logs := strings.Join(textParts, "")
	if result.InfraError != "" {
		return game.TurnResult{
			Role:   role,
			Status: game.StatusContinue,
			Logs:   orDefault(logs, result.InfraError),
			Stats:  stats,
			Error:  result.InfraError,
		}
	}

	stderrTrimmed := strings.TrimSpace(result.Stderr)
	if result.ReturnCode != 0 {
		detail := strings.TrimSpace(logs)
		if stderrTrimmed != "" {
			detail = fmt.Sprintf("%s\n[stderr]\n%s", detail, stderrTrimmed)
			detail = strings.TrimSpace(detail)
		}
		return game.TurnResult{
			Role:   role,
			Status: game.StatusContinue,
			Logs:   orDefault(detail, fmt.Sprintf("pi exited %d", result.ReturnCode)),
			Stats:  stats,
			Error:  orDefault(agentError, fmt.Sprintf("pi_failed:%d", result.ReturnCode)),
		}
	}

	if agentError != "" {
		return game.TurnResult{
			Role:   role,
			Status: game.StatusContinue,
			Logs:   orDefault(logs, agentError),
			Stats:  stats,
			Error:  agentError,
		}
	}

	if strings.TrimSpace(logs) == "" {
		detail := "Pi produced no JSON/text output."
		if stderrTrimmed != "" {
			detail = fmt.Sprintf("%s\n[stderr]\n%s", detail, stderrTrimmed)
		}
		return game.TurnResult{
			Role:   role,
			Status: game.StatusContinue,
			Logs:   detail,
			Stats:  stats,
			Error:  "agent_no_output",
		}
	}

	return game.TurnResult{
		Role:   role,
		Status: game.ExtractStatus(logs),
		Logs:   logs,
		Stats:  stats,
	}
}

// WorkspaceState returns the workspace state from the platform.
func (pe *PiExecutor) WorkspaceState() string {
	return pe.Platform.WorkspaceState()
}

// CheckpointWorkspace creates a workspace checkpoint.
func (pe *PiExecutor) CheckpointWorkspace(dest string) bool {
	return pe.Platform.CheckpointWorkspace(dest)
}

// BuildPiArgv builds the Pi command line.
func BuildPiArgv(model, thinking, taskPath string) []string {
	script := `export PATH="$HOME/.local/bin:$HOME/.npm-global/bin:$PATH"; ` +
		`if ! command -v pi >/dev/null 2>&1; then ` +
		`for nvm_script in "${NVM_DIR:-}/nvm.sh" "$HOME/.nvm/nvm.sh" "/usr/local/nvm/nvm.sh"; do ` +
		`[ -s "$nvm_script" ] || continue; ` +
		`. "$nvm_script"; ` +
		`break; ` +
		`done; ` +
		`fi; ` +
		fmt.Sprintf(`prompt="${%s}"; `, platform.TaskEnvVar) +
		`if [ -n "${3:-}" ]; then prompt="$3"; fi; ` +
		`exec pi --mode json --model "$1" --thinking "$2" --no-session --no-extensions -p "$prompt"`
	if taskPath != "" {
		return []string{"sh", "-c", script, "pi", model, thinking, "@" + taskPath}
	}
	return []string{"sh", "-c", script, "pi", model, thinking}
}

// -- Pi JSON event parsing ----------------------------------------------------

// ParsePiJSONLine parses one Pi JSON stdout line.
func ParsePiJSONLine(line string) map[string]interface{} {
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		return nil
	}
	return m
}

// HandlePiEvent folds one Pi event into text and stats accumulators.
func HandlePiEvent(event map[string]interface{}, textParts *[]string, stats *game.TurnStats) {
	etype, _ := event["type"].(string)

	switch etype {
	case "message_end":
		msg, ok := event["message"].(map[string]interface{})
		if !ok {
			return
		}
		if role, _ := msg["role"].(string); role == "user" {
			return
		}
		content, _ := msg["content"].([]interface{})
		for _, block := range content {
			bm, ok := block.(map[string]interface{})
			if !ok {
				continue
			}
			if bt, _ := bm["type"].(string); bt == "text" {
				text, _ := bm["text"].(string)
				if strings.TrimSpace(text) != "" {
					*textParts = append(*textParts, text)
				}
			}
		}
		usage, _ := msg["usage"].(map[string]interface{})
		if usage != nil {
			stats.InputTokens += intFromAny(usage["input"])
			stats.OutputTokens += intFromAny(usage["output"])
			stats.CacheReadTokens += intFromAny(usage["cacheRead"])
			stats.CacheCreationTokens += intFromAny(usage["cacheWrite"])
			cost, _ := usage["cost"].(map[string]interface{})
			if cost != nil {
				stats.CostUSD += floatFromAny(cost["total"])
			}
		}
		if stats.Model == "" {
			if m, ok := msg["model"].(string); ok {
				stats.Model = m
			}
		}

	case "tool_execution_end":
		stats.NumTurns++
	}
}

// ExtractPiEventError extracts any error from a Pi event.
func ExtractPiEventError(event map[string]interface{}) string {
	etype, _ := event["type"].(string)
	switch etype {
	case "message_end", "turn_end":
		msg, ok := event["message"].(map[string]interface{})
		if !ok {
			return ""
		}
		return extractMessageError(msg)
	case "agent_end":
		messages, _ := event["messages"].([]interface{})
		for i := len(messages) - 1; i >= 0; i-- {
			m, ok := messages[i].(map[string]interface{})
			if !ok {
				continue
			}
			if role, _ := m["role"].(string); role == "assistant" {
				if err := extractMessageError(m); err != "" {
					return err
				}
			}
		}
	}
	return ""
}

// ExtractPiStopReason extracts the stop reason from a Pi event.
func ExtractPiStopReason(event map[string]interface{}) string {
	etype, _ := event["type"].(string)
	var msg map[string]interface{}

	switch etype {
	case "message_end", "turn_end":
		candidate, ok := event["message"].(map[string]interface{})
		if ok && getString(candidate, "role") == "assistant" {
			msg = candidate
		}
	case "agent_end":
		messages, _ := event["messages"].([]interface{})
		for i := len(messages) - 1; i >= 0; i-- {
			m, ok := messages[i].(map[string]interface{})
			if ok && getString(m, "role") == "assistant" {
				msg = m
				break
			}
		}
	}

	if msg == nil {
		return ""
	}
	sr, _ := msg["stopReason"].(string)
	return sr
}

// AppendRawLogLine appends a raw log line to the JSONL file.
func AppendRawLogLine(path, line string) {
	event := ParsePiJSONLine(line)
	var out string
	if event == nil {
		b, _ := json.Marshal(map[string]interface{}{"event": "unparsed", "line": line})
		out = string(b)
	} else {
		b, _ := json.Marshal(event)
		out = string(b)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(out + "\n")
}

func extractMessageError(msg map[string]interface{}) string {
	em, _ := msg["errorMessage"].(string)
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

// Package executor provides the Pi executor for PVG turns.
package executor

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/telos-org/telos/internal/game"
	"github.com/telos-org/telos/internal/platform"
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
	return &PiExecutor{
		Platform: p,
		Model:    model,
		Thinking: thinking,
		Timeout:  timeout,
	}
}

// ExecuteTurn runs one Pi agent turn.
func (pe *PiExecutor) ExecuteTurn(task string, role string, turnState *game.TurnState) game.TurnResult {
	var stats game.TurnStats
	stats.Model = pe.Model
	var agentError string
	var taskPath string
	var sessionPath string
	var stopRequested func() bool
	if turnState != nil {
		taskPath = turnState.TaskPath()
		sessionPath = turnState.PiSessionPath()
		stopRequested = turnState.StopRequested
	}

	argv := BuildPiArgv(pe.Model, pe.Thinking, taskPath, sessionPath)
	taskEnv := task
	if taskPath != "" {
		taskEnv = ""
	}
	projector := startPiLiveProjector(sessionPath, turnState)
	if projector != nil {
		defer projector.Stop()
	}
	result := pe.Platform.Run(argv, taskEnv, map[string]string{"TELOS_ROLE": role}, pe.Timeout, stopRequested, func(line string) {
		if projector != nil {
			projector.ObserveLine(line)
		}
	})

	logs := strings.Join(result.RawLines, "\n")
	if sessionPath != "" {
		summary, err := ReadPiSession(sessionPath)
		if err == nil {
			logs = summary.Logs
			stats = mergeTurnStats(stats, summary.Stats)
			agentError = summary.Error
		} else if result.ReturnCode == 0 && result.InfraError == "" {
			return game.TurnResult{
				Role:        role,
				Status:      game.StatusContinue,
				Logs:        fmt.Sprintf("pi_session_unavailable:%v", err),
				Stats:       stats,
				Error:       fmt.Sprintf("pi_session_unavailable:%v", err),
				Recoverable: true,
			}
		}
	}

	if result.InfraError != "" {
		return game.TurnResult{
			Role:        role,
			Status:      game.StatusContinue,
			Logs:        result.InfraError,
			Stats:       stats,
			Error:       result.InfraError,
			Recoverable: !result.TimedOut,
		}
	}

	stderrTrimmed := strings.TrimSpace(result.Stderr)
	if result.ReturnCode != 0 {
		reason := orDefault(agentError, fmt.Sprintf("pi_failed:%d", result.ReturnCode))
		if agentError == "" && stderrTrimmed != "" {
			reason = fmt.Sprintf("%s\n[stderr]\n%s", reason, stderrTrimmed)
		}
		return game.TurnResult{
			Role:        role,
			Status:      game.StatusContinue,
			Logs:        reason,
			Stats:       stats,
			Error:       reason,
			Recoverable: true,
		}
	}

	if agentError != "" {
		return game.TurnResult{
			Role:        role,
			Status:      game.StatusContinue,
			Logs:        agentError,
			Stats:       stats,
			Error:       agentError,
			Recoverable: true,
		}
	}

	if strings.TrimSpace(logs) == "" {
		detail := "Pi produced no assistant text."
		if stderrTrimmed != "" {
			detail = fmt.Sprintf("%s\n[stderr]\n%s", detail, stderrTrimmed)
		}
		return game.TurnResult{
			Role:        role,
			Status:      game.StatusContinue,
			Logs:        detail,
			Stats:       stats,
			Error:       "agent_no_output",
			Recoverable: true,
		}
	}

	return game.TurnResult{
		Role:   role,
		Status: game.ExtractStatus(logs),
		Logs:   logs,
		Stats:  stats,
	}
}

type piLiveProjector struct {
	sessionPath string
	turnState   *game.TurnState

	mu   sync.Mutex
	seen map[string]bool
	stop chan struct{}
	done chan struct{}
}

func startPiLiveProjector(sessionPath string, turnState *game.TurnState) *piLiveProjector {
	if sessionPath == "" || turnState == nil || turnState.OnLiveEvent == nil {
		return nil
	}
	p := &piLiveProjector{
		sessionPath: sessionPath,
		turnState:   turnState,
		seen:        map[string]bool{},
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
	}
	go p.watch()
	return p
}

func (p *piLiveProjector) Stop() {
	close(p.stop)
	<-p.done
	p.observeSessionFile()
}

func (p *piLiveProjector) ObserveLine(line string) {
	for _, event := range piLineEvents(line) {
		p.emit(event)
	}
}

func (p *piLiveProjector) watch() {
	defer close(p.done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			p.observeSessionFile()
		case <-p.stop:
			return
		}
	}
}

func (p *piLiveProjector) observeSessionFile() {
	data, err := os.ReadFile(p.sessionPath)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		p.ObserveLine(line)
	}
}

func (p *piLiveProjector) emit(event game.LiveAgentEvent) {
	key := event.Kind + "\x00" + event.Text
	p.mu.Lock()
	if p.seen[key] {
		p.mu.Unlock()
		return
	}
	p.seen[key] = true
	p.mu.Unlock()
	p.turnState.OnLiveEvent(event)
}

func piLineEvents(line string) []game.LiveAgentEvent {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	var entry map[string]interface{}
	dec := json.NewDecoder(strings.NewReader(line))
	dec.UseNumber()
	if dec.Decode(&entry) != nil {
		return nil
	}
	msg, ok := entry["message"].(map[string]interface{})
	if !ok || getString(msg, "role") != "assistant" {
		return nil
	}
	events := game.ExtractLiveAgentEvents(assistantText(msg))
	events = append(events, piToolCallEvents(msg)...)
	return events
}

func piToolCallEvents(msg map[string]interface{}) []game.LiveAgentEvent {
	content, _ := msg["content"].([]interface{})
	var events []game.LiveAgentEvent
	for _, block := range content {
		bm, ok := block.(map[string]interface{})
		if !ok || getString(bm, "type") != "toolCall" {
			continue
		}
		text := safeToolProgressText(bm)
		if text == "" {
			continue
		}
		events = append(events, game.LiveAgentEvent{
			Kind: "progress_update",
			Text: text,
		})
	}
	return events
}

func safeToolProgressText(block map[string]interface{}) string {
	name := getString(block, "name")
	args, _ := block["arguments"].(map[string]interface{})
	switch name {
	case "read":
		if path := safePathLabel(getString(args, "path")); path != "" {
			return "Reading " + path
		}
		return "Reading files"
	case "write", "edit":
		if path := safePathLabel(getString(args, "path")); path != "" {
			return "Editing " + path
		}
		return "Editing workspace files"
	case "bash":
		return safeShellProgressText(getString(args, "command"))
	case "":
		return ""
	default:
		return "Using " + name
	}
}

func safePathLabel(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	path = strings.TrimRight(path, "/")
	if path == "" {
		return ""
	}
	parts := strings.Split(path, "/")
	label := parts[len(parts)-1]
	if label == "" || label == "." || label == ".." {
		return "file"
	}
	return label
}

func safeShellProgressText(command string) string {
	command = strings.TrimSpace(command)
	switch {
	case strings.HasPrefix(command, "kubectl "):
		return "Running kubectl"
	case strings.HasPrefix(command, "git "):
		return "Updating workspace"
	case strings.HasPrefix(command, "npm "), strings.HasPrefix(command, "npx "), strings.Contains(command, " npm "):
		return "Running Node build step"
	case command == "":
		return "Running shell command"
	default:
		return "Running shell command"
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
func BuildPiArgv(model, thinking, taskPath, sessionPath string) []string {
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
		`if [ -n "${4:-}" ]; then ` +
		`exec pi --mode text --model "$1" --thinking "$2" --session "$4" -p "$prompt"; ` +
		`fi; ` +
		`exec pi --mode text --model "$1" --thinking "$2" --no-session -p "$prompt"`
	argv := []string{"sh", "-c", script, "pi", model, thinking}
	if taskPath != "" {
		argv = append(argv, "@"+taskPath)
	} else if sessionPath != "" {
		argv = append(argv, "")
	}
	if sessionPath != "" {
		argv = append(argv, sessionPath)
	}
	return argv
}

// -- Pi session parsing -------------------------------------------------------

type PiSessionSummary struct {
	Logs  string
	Stats game.TurnStats
	Error string
}

// ReadPiSession reads Pi's compact session JSONL file for a completed turn.
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

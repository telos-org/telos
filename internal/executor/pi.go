// Package executor provides the Pi executor for PVG turns.
package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/telos-org/telos/internal/game"
	"github.com/telos-org/telos/internal/platform"
)

// PiExecutor runs one Pi subprocess per PVG turn. Its initial settings are fixed
// defaults; SetModel and SetThinkingLevel configure only the active invocation.
type PiExecutor struct {
	Platform *platform.LocalPlatform
	Model    string
	Thinking string
	Timeout  int
	// BeforePrompt may configure this invocation before its first model request.
	// It runs off the protocol reader and must not call ExecuteTurn recursively.
	BeforePrompt    func(context.Context) error
	ModelConfigPath string

	mu     sync.Mutex
	active *piRPC
}

// NewPiExecutor creates a new Pi executor.
func NewPiExecutor(p *platform.LocalPlatform, model, thinking string, timeout int) *PiExecutor {
	if thinking == "" {
		thinking = "medium"
	}
	return &PiExecutor{Platform: p, Model: model, Thinking: thinking, Timeout: timeout}
}

// ExecuteTurn runs one Pi invocation, including its tools and automatic retries.
func (pe *PiExecutor) ExecuteTurn(task, role string, turnState *game.TurnState) game.TurnResult {
	rpc := newPiRPC()
	pe.mu.Lock()
	if pe.active != nil {
		pe.mu.Unlock()
		return game.TurnResult{Role: role, Status: game.StatusContinue, Error: "pi_already_running"}
	}
	pe.active = rpc
	pe.mu.Unlock()
	defer func() {
		pe.mu.Lock()
		pe.active = nil
		pe.mu.Unlock()
	}()
	childEnv := map[string]string{"TELOS_ROLE": role}
	if pe.ModelConfigPath != "" {
		extension, err := os.CreateTemp("", "telos-pi-models-*.mjs")
		if err != nil {
			return game.TurnResult{Role: role, Error: err.Error()}
		}
		defer os.Remove(extension.Name())
		_, writeErr := extension.Write(piModelsExtension)
		closeErr := extension.Close()
		if writeErr != nil {
			return game.TurnResult{Role: role, Error: writeErr.Error()}
		}
		if closeErr != nil {
			return game.TurnResult{Role: role, Error: closeErr.Error()}
		}
		childEnv["TELOS_PI_MODEL_CONFIG"] = pe.ModelConfigPath
		childEnv["TELOS_PI_MODEL_EXTENSION"] = extension.Name()
	}

	var sessionPath string
	if turnState != nil {
		sessionPath = turnState.PiSessionPath()
	}
	argv := BuildPiArgv(pe.Model, pe.Thinking, sessionPath)
	stream := piStream{stats: game.TurnStats{Model: pe.Model}}
	var protocolError string
	var userStopped atomic.Bool
	// Live projections are optional. Keep slow observers off the protocol reader;
	// a full queue drops progress only, never completion, errors, or usage.
	var liveEvents chan game.LiveAgentEvent
	if turnState != nil && turnState.OnLiveEvent != nil {
		liveEvents = make(chan game.LiveAgentEvent, 128)
		liveDone := make(chan struct{})
		defer func() { close(liveEvents); <-liveDone }()
		go func() {
			defer close(liveDone)
			for event := range liveEvents {
				turnState.OnLiveEvent(event)
			}
		}()
	}
	promptDone := make(chan struct{})
	go func() {
		defer close(promptDone)
		ctx, cancel := context.WithTimeout(context.Background(), piCommandTimeout)
		defer cancel()
		if pe.BeforePrompt != nil {
			if err := pe.BeforePrompt(ctx); err != nil {
				rpc.close(err)
				return
			}
		}
		data, err := rpc.call(ctx, "prompt", map[string]interface{}{"message": task})
		if err == nil && len(data) > 0 {
			var accepted struct{ Disposition string }
			if err = json.Unmarshal(data, &accepted); err == nil && accepted.Disposition == "handled" {
				err = fmt.Errorf("pi_prompt_handled: no agent run started")
			}
		}
		if err != nil {
			rpc.close(err)
		}
	}()
	interrupt := func() bool {
		if turnState != nil && turnState.StopRequested != nil && turnState.StopRequested() {
			userStopped.Store(true)
			return true
		}
		return rpc.shutdownExpired()
	}
	result := pe.Platform.RunWithStdin(argv,
		childEnv, pe.Timeout, interrupt, rpc.start, func(line string) {
			var record map[string]interface{}
			if err := json.Unmarshal([]byte(line), &record); err != nil {
				protocolError = fmt.Sprintf("pi_rpc_protocol: %v", err)
				rpc.close(fmt.Errorf("%s", protocolError))
				return
			}
			switch getString(record, "type") {
			case "telos_pi_capabilities":
				supported, _ := record["liveSettings"].(bool)
				rpc.setLiveSettingsSupport(supported)
			case "response":
				rpc.respond(line)
			case "message_end":
				stream.message(record)
				if liveEvents != nil {
					for _, event := range piLineEvents(line) {
						select {
						case liveEvents <- event:
						default:
						}
					}
				}
			case "agent_settled":
				stream.settled = true
				rpc.close(ErrPiNotRunning)
			case "extension_ui_request":
				switch getString(record, "method") {
				case "select", "confirm", "input", "editor":
					// Headless turns cannot answer dialogs. Match Pi's non-UI
					// cancellation behavior without ever blocking its reader.
					go func() {
						ctx, cancel := context.WithTimeout(context.Background(), piCommandTimeout)
						defer cancel()
						_ = rpc.send(ctx, map[string]interface{}{"type": "extension_ui_response", "id": record["id"], "cancelled": true})
					}()
				}
			}
		})
	rpc.close(ErrPiNotRunning)
	<-promptDone
	stream.stats.DurationMS = result.DurationMS
	failure := func(reason string, recoverable bool) game.TurnResult {
		return game.TurnResult{Role: role, Status: game.StatusContinue, Logs: reason,
			Stats: stream.stats, Error: reason, Recoverable: recoverable}
	}
	if result.TimedOut {
		return failure(fmt.Sprintf("local_timeout:%d", pe.Timeout), false)
	}
	if userStopped.Load() {
		return failure("local_interrupted:stop_requested", false)
	}
	if protocolError != "" {
		return failure(protocolError, true)
	}
	if cause := rpc.closedError(); cause != ErrPiNotRunning {
		reason := cause.Error()
		if stderr := strings.TrimSpace(result.Stderr); stderr != "" {
			reason += "\n[stderr]\n" + stderr
		}
		return failure(reason, recoverableAgentFailure(reason))
	}
	if result.Interrupted {
		reason := orDefault(stream.err, "pi_rpc_shutdown_timeout")
		return failure(reason, recoverableAgentFailure(reason))
	}
	if result.InfraError != "" {
		return failure(result.InfraError, recoverableAgentFailure(result.InfraError))
	}
	if result.ReturnCode != 0 {
		reason := orDefault(stream.err, fmt.Sprintf("pi_failed:%d", result.ReturnCode))
		if stderr := strings.TrimSpace(result.Stderr); stderr != "" {
			reason += "\n[stderr]\n" + stderr
		}
		return failure(reason, recoverableAgentFailure(reason))
	}
	if stream.err != "" {
		return failure(stream.err, recoverableAgentFailure(stream.err))
	}
	if !stream.settled {
		return failure("pi_rpc_exited_before_settled", true)
	}
	if strings.TrimSpace(stream.logs) == "" {
		return failure("agent_no_output", true)
	}
	return game.TurnResult{Role: role, Status: game.ExtractStatus(stream.logs), Logs: stream.logs, Stats: stream.stats}
}

func recoverableAgentFailure(errorText string) bool {
	_, blocked := game.AgentFailureBlocker(errorText)
	return !blocked
}

// Only completed messages count toward text and usage. message_update contains
// repeated partial snapshots. agent_end is not final: Pi may retry afterward.
type piStream struct {
	logs    string
	stats   game.TurnStats
	err     string
	settled bool
}

func (stream *piStream) message(record map[string]interface{}) {
	msg, ok := record["message"].(map[string]interface{})
	if !ok {
		return
	}
	switch getString(msg, "role") {
	case "assistant":
		stream.logs = assistantText(msg)
		stream.err = errorFromPiMessage(msg)
		// With multiple models, Model labels the last response; usage and cost
		// are summed from the actual messages, including earlier models.
		stream.stats = mergeTurnStats(stream.stats, statsFromPiMessage(msg))
	case "toolResult", "bashExecution":
		stream.stats.NumTurns++
	}
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
			Kind: "tool",
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
	if len(parts) > 1 {
		parent := parts[len(parts)-2]
		if parent != "" && parent != "." && parent != ".." {
			return parent + "/" + label
		}
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

// Only live settings changes require this audited version. Older Pi releases
// persist those commands as global defaults, but can still execute normal turns.
const piLiveSettingsVersion = "1.0.4"

// BuildPiArgv starts RPC mode. Prompts travel over stdin, so large tasks need
// neither environment variables nor @file arguments (unsupported by Pi RPC).
func BuildPiArgv(model, thinking, sessionPath string) []string {
	script := `export PATH="$HOME/.local/bin:$HOME/.npm-global/bin:$PATH"; ` +
		`if ! command -v pi >/dev/null 2>&1; then ` +
		`for nvm_script in "${NVM_DIR:-}/nvm.sh" "$HOME/.nvm/nvm.sh" "/usr/local/nvm/nvm.sh"; do ` +
		`[ -s "$nvm_script" ] || continue; . "$nvm_script"; break; done; fi; ` +
		`pi_version="$(pi --version 2>/dev/null)" || pi_version=""; ` +
		`live_settings=false; if [ "$pi_version" = "$4" ]; then live_settings=true; fi; ` +
		`printf '{"type":"telos_pi_capabilities","liveSettings":%s}\n' "$live_settings"; ` +
		`model="$1"; thinking="$2"; session="${3:-}"; ` +
		`set -- --mode rpc --model "$model" --thinking "$thinking"; ` +
		`if [ -n "$session" ]; then set -- "$@" --session "$session"; else set -- "$@" --no-session; fi; ` +
		`if [ -n "${TELOS_PI_MODEL_EXTENSION:-}" ]; then set -- "$@" --extension "$TELOS_PI_MODEL_EXTENSION"; fi; ` +
		`append_prompt="${TELOS_PI_APPEND_SYSTEM_PROMPT:-}"; ` +
		`if [ -n "$append_prompt" ]; then append_file="$(mktemp)"; printf '%s' "$append_prompt" > "$append_file"; ` +
		`set -- "$@" --append-system-prompt "$append_file"; fi; ` +
		`exec pi "$@"`
	return []string{"sh", "-c", script, "pi", model, thinking, sessionPath, piLiveSettingsVersion}
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
	if message := getString(msg, "errorMessage"); message != "" {
		return message
	}
	switch getString(msg, "stopReason") {
	case "error", "aborted":
		return "agent_failed:" + getString(msg, "stopReason")
	}
	return ""
}

func mergeTurnStats(base, extra game.TurnStats) game.TurnStats {
	base.CostUSD += extra.CostUSD
	base.DurationMS += extra.DurationMS
	base.NumTurns += extra.NumTurns
	base.InputTokens += extra.InputTokens
	base.OutputTokens += extra.OutputTokens
	base.CacheReadTokens += extra.CacheReadTokens
	base.CacheCreationTokens += extra.CacheCreationTokens
	if extra.Model != "" {
		base.Model = extra.Model
	}
	return base
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

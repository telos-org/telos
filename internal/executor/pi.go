// Package executor provides the Pi executor for PVG turns.
package executor

import (
	"bufio"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/telos-org/telos/internal/game"
	"github.com/telos-org/telos/internal/gateway"
	"github.com/telos-org/telos/internal/platform"
	"github.com/telos-org/telos/internal/sessionapi"
)

//go:embed pi_extensions/telos_bifrost.ts
var telosBifrostExtension string

// PiExecutor runs Pi as one PVG agent turn on the given LocalPlatform.
type PiExecutor struct {
	Platform           *platform.LocalPlatform
	Model              string
	Thinking           string
	Timeout            int
	SessionID          string
	SessionDir         string
	ModelProfile       sessionapi.ModelProfile
	GatewayEnv         map[string]string
	GatewayCleanup     func() error
	CostHardLimitValue bool
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

// ConfigureGateway injects an OpenAI-compatible gateway into the Pi process.
func (pe *PiExecutor) ConfigureGateway(cred gateway.Credential) error {
	if cred.Transport == gateway.TransportBifrostAsync {
		if cred.Cleanup != nil {
			_ = cred.Cleanup()
		}
		return fmt.Errorf("bifrost_async transport requires the native executor (not available in this build); configure openai_sync instead")
	}
	if strings.TrimSpace(cred.BaseURL) == "" || strings.TrimSpace(cred.APIKey) == "" {
		if cred.Cleanup != nil {
			_ = cred.Cleanup()
		}
		return fmt.Errorf("gateway returned incomplete OpenAI-compatible credentials")
	}
	profile, err := sessionapi.NormalizeModelProfile(string(cred.ModelProfile))
	if err != nil {
		if cred.Cleanup != nil {
			_ = cred.Cleanup()
		}
		return err
	}
	headers := "{}"
	if len(cred.Headers) > 0 {
		if data, err := json.Marshal(cred.Headers); err == nil {
			headers = string(data)
		}
	}
	pe.GatewayEnv = map[string]string{
		"OPENAI_API_KEY":          strings.TrimSpace(cred.APIKey),
		"OPENAI_BASE_URL":         strings.TrimRight(strings.TrimSpace(cred.BaseURL), "/"),
		"TELOS_GATEWAY_API_KEY":   strings.TrimSpace(cred.APIKey),
		"TELOS_GATEWAY_BASE_URL":  strings.TrimRight(strings.TrimSpace(cred.BaseURL), "/"),
		"TELOS_GATEWAY_HEADERS":   headers,
		"TELOS_GATEWAY_KIND":      strings.TrimSpace(cred.Kind),
		"TELOS_GATEWAY_TRANSPORT": strings.TrimSpace(cred.Transport),
		"TELOS_MODEL_PROFILE":     string(profile),
	}
	pe.ModelProfile = profile
	pe.GatewayCleanup = cred.Cleanup
	pe.CostHardLimitValue = cred.CostHardLimit
	return nil
}

// Cleanup reconciles managed gateway usage, when this executor owns a session key.
func (pe *PiExecutor) Cleanup() error {
	if pe.GatewayCleanup == nil {
		return nil
	}
	return pe.GatewayCleanup()
}

// CostHardLimit reports whether the active gateway enforces cost caps server-side.
func (pe *PiExecutor) CostHardLimit() bool {
	return pe.CostHardLimitValue
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

	profile, _ := sessionapi.NormalizeModelProfile(string(pe.ModelProfile))
	routing := pe.currentRoutingState(profile)
	extensionPath := ""
	if pe.needsBifrostExtension() {
		var err error
		extensionPath, err = pe.ensureBifrostExtension()
		if err != nil {
			return game.TurnResult{
				Role:        role,
				Status:      game.StatusContinue,
				Logs:        fmt.Sprintf("pi_extension_unavailable:%v", err),
				Stats:       stats,
				Error:       fmt.Sprintf("pi_extension_unavailable:%v", err),
				Recoverable: true,
			}
		}
	}

	argv := BuildPiArgv(pe.Model, pe.Thinking, taskPath, sessionPath, extensionPath)
	taskEnv := task
	if taskPath != "" {
		taskEnv = ""
	}
	env := map[string]string{"TELOS_ROLE": role}
	for k, v := range pe.GatewayEnv {
		env[k] = v
	}
	for k, v := range pe.bifrostTurnEnv(profile, routing, role, turnState) {
		env[k] = v
	}
	sessionOffset := int64(0)
	if sessionPath != "" {
		if info, err := os.Stat(sessionPath); err == nil {
			sessionOffset = info.Size()
		}
	}
	result := pe.Platform.Run(argv, taskEnv, env, pe.Timeout, stopRequested, nil, "")

	logs := strings.Join(result.RawLines, "\n")
	if sessionPath != "" {
		summary, err := ReadPiSessionFromOffset(sessionPath, sessionOffset)
		if err == nil {
			logs = summary.Logs
			stats = mergeTurnStats(stats, summary.Stats)
			agentError = summary.Error
			if summary.Routing != nil {
				if err := pe.updateRoutingState(profile, *summary.Routing); err != nil {
					return game.TurnResult{
						Role:        role,
						Status:      game.StatusContinue,
						Logs:        fmt.Sprintf("gateway_routing_update_failed:%v", err),
						Stats:       stats,
						Error:       fmt.Sprintf("gateway_routing_update_failed:%v", err),
						Recoverable: true,
					}
				}
			}
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
			Recoverable: true,
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

// WorkspaceState returns the workspace state from the platform.
func (pe *PiExecutor) WorkspaceState() platform.WorkspaceSnapshot {
	return pe.Platform.WorkspaceSnapshot()
}

// CheckpointWorkspace creates a workspace checkpoint.
func (pe *PiExecutor) CheckpointWorkspace(dest string) bool {
	return pe.Platform.CheckpointWorkspace(dest)
}

func (pe *PiExecutor) needsBifrostExtension() bool {
	if strings.HasPrefix(pe.Model, "telos-bifrost/") {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(pe.GatewayEnv["TELOS_GATEWAY_KIND"]), gateway.KindBifrost)
}

func (pe *PiExecutor) ensureBifrostExtension() (string, error) {
	base := pe.SessionDir
	if strings.TrimSpace(base) == "" {
		dir, err := os.MkdirTemp("", "telos-pi-extension-")
		if err != nil {
			return "", err
		}
		base = dir
	}
	dir := filepath.Join(base, "runtime")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "telos_bifrost.ts")
	if err := os.WriteFile(path, []byte(telosBifrostExtension), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func (pe *PiExecutor) currentRoutingState(profile sessionapi.ModelProfile) *sessionapi.GatewayRoutingState {
	if strings.TrimSpace(pe.SessionDir) == "" {
		return &sessionapi.GatewayRoutingState{ModelProfile: profile}
	}
	manifest, err := sessionapi.ReadManifest(filepath.Join(pe.SessionDir, "session.json"))
	if err != nil || manifest.GatewayRouting == nil {
		return &sessionapi.GatewayRoutingState{ModelProfile: profile}
	}
	stateProfile, err := sessionapi.NormalizeModelProfile(string(manifest.GatewayRouting.ModelProfile))
	if err != nil || stateProfile != profile {
		return &sessionapi.GatewayRoutingState{ModelProfile: profile}
	}
	return manifest.GatewayRouting
}

func (pe *PiExecutor) bifrostTurnEnv(profile sessionapi.ModelProfile, routing *sessionapi.GatewayRoutingState, role string, turnState *game.TurnState) map[string]string {
	sessionID := strings.TrimSpace(pe.SessionID)
	if sessionID == "" {
		sessionID = "unknown"
	}
	assignedProvider := "unset"
	phase := "new"
	if routing != nil && strings.TrimSpace(routing.AssignedProvider) != "" {
		assignedProvider = strings.TrimSpace(routing.AssignedProvider)
		phase = "existing"
	}
	requestID := sessionID
	if turnState != nil {
		requestID = fmt.Sprintf("%s:%d:%d:%s", sessionID, turnState.EpochID, turnState.RoundNum, role)
	}
	return map[string]string{
		"TELOS_MODEL_PROFILE":                 string(profile),
		"TELOS_BIFROST_SESSION_TTL":           "1h",
		"TELOS_BIFROST_AGENT_SESSION_ID":      sessionID,
		"TELOS_BIFROST_AGENT_CACHE_KEY":       sessionID,
		"TELOS_BIFROST_COMPACTION_SESSION_ID": sessionID + ":compaction",
		"TELOS_BIFROST_COMPACTION_CACHE_KEY":  sessionID + ":compaction",
		"TELOS_BIFROST_AGENT_PHASE":           phase,
		"TELOS_BIFROST_ASSIGNED_PROVIDER":     assignedProvider,
		"TELOS_BIFROST_REQUEST_ID":            requestID,
	}
}

func (pe *PiExecutor) updateRoutingState(profile sessionapi.ModelProfile, route PiRoutingObservation) error {
	if strings.TrimSpace(pe.SessionDir) == "" {
		return nil
	}
	manifestPath := filepath.Join(pe.SessionDir, "session.json")
	manifest, err := sessionapi.ReadManifest(manifestPath)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	state := manifest.GatewayRouting
	if state != nil {
		stateProfile, err := sessionapi.NormalizeModelProfile(string(state.ModelProfile))
		if err != nil || stateProfile != profile {
			state = nil
		}
	}
	if state == nil {
		state = &sessionapi.GatewayRoutingState{ModelProfile: profile}
		manifest.GatewayRouting = state
	}
	state.ModelProfile = profile
	if route.Model != "" {
		state.LastModel = route.Model
	}
	state.LastFallback = route.Fallback
	state.LastSeenAt = now
	if route.OK && route.Provider != "" && isBifrostAgentModel(route.Model) && state.AssignedProvider == "" {
		state.AssignedProvider = route.Provider
		state.AssignedAt = now
	}
	return sessionapi.WriteManifest(manifestPath, manifest)
}

func isBifrostAgentModel(model string) bool {
	model = strings.TrimPrefix(strings.TrimSpace(model), "telos-bifrost/")
	return strings.HasSuffix(model, "-agent")
}

// BuildPiArgv builds the Pi command line.
func BuildPiArgv(model, thinking, taskPath, sessionPath string, extensionPath string) []string {
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
		`if [ -n "${5:-}" ]; then exec pi --mode text --model "$1" --thinking "$2" --session "$4" --no-extensions --extension "$5" -p "$prompt"; fi; ` +
		`exec pi --mode text --model "$1" --thinking "$2" --session "$4" --no-extensions -p "$prompt"; ` +
		`fi; ` +
		`if [ -n "${5:-}" ]; then exec pi --mode text --model "$1" --thinking "$2" --no-session --no-extensions --extension "$5" -p "$prompt"; fi; ` +
		`exec pi --mode text --model "$1" --thinking "$2" --no-session --no-extensions -p "$prompt"`
	argv := []string{"sh", "-c", script, "pi", model, thinking}
	if taskPath != "" {
		argv = append(argv, "@"+taskPath)
	} else if sessionPath != "" {
		argv = append(argv, "")
	}
	if sessionPath != "" {
		argv = append(argv, sessionPath)
	}
	if extensionPath != "" {
		for len(argv) < 8 {
			argv = append(argv, "")
		}
		argv = append(argv, extensionPath)
	}
	return argv
}

// -- Pi session parsing -------------------------------------------------------

type PiSessionSummary struct {
	Logs    string
	Stats   game.TurnStats
	Error   string
	Routing *PiRoutingObservation
}

type PiRoutingObservation struct {
	Provider string
	Model    string
	Fallback bool
	OK       bool
}

// ReadPiSession reads Pi's compact session JSONL file for a completed turn.
func ReadPiSession(path string) (PiSessionSummary, error) {
	return ReadPiSessionFromOffset(path, 0)
}

// ReadPiSessionFromOffset reads Pi session entries appended from offset onward.
func ReadPiSessionFromOffset(path string, offset int64) (PiSessionSummary, error) {
	f, err := os.Open(path)
	if err != nil {
		return PiSessionSummary{}, err
	}
	defer f.Close()
	if offset > 0 {
		info, err := f.Stat()
		if err != nil {
			return PiSessionSummary{}, err
		}
		if offset <= info.Size() {
			if _, err := f.Seek(offset, io.SeekStart); err != nil {
				return PiSessionSummary{}, err
			}
		}
	}

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
				if route, ok := routingFromPiEntry(entry); ok {
					summary.Routing = &route
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

func routingFromPiEntry(entry map[string]interface{}) (PiRoutingObservation, bool) {
	if getString(entry, "type") != "custom" || getString(entry, "customType") != "telos-bifrost-routing" {
		return PiRoutingObservation{}, false
	}
	data, _ := entry["data"].(map[string]interface{})
	if data == nil {
		return PiRoutingObservation{}, false
	}
	provider := strings.TrimSpace(getString(data, "provider"))
	model := strings.TrimSpace(getString(data, "model"))
	if provider == "" && model == "" {
		return PiRoutingObservation{}, false
	}
	fallback, _ := data["fallback"].(bool)
	ok, hasOK := data["ok"].(bool)
	if !hasOK {
		ok = true
	}
	return PiRoutingObservation{
		Provider: provider,
		Model:    model,
		Fallback: fallback,
		OK:       ok,
	}, true
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

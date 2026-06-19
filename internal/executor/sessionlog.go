package executor

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/telos-org/telos/internal/game"
)

// -- Agent session contract --------------------------------------------------
//
// These types are the single typed contract for the per-turn session JSONL,
// written by nativeSessionLogger.

const agentSessionSchema = "telos.agent_session.v1"

type sessionEvent struct {
	Schema    string          `json:"schema,omitempty"`
	Type      string          `json:"type"`
	Version   int             `json:"version,omitempty"`
	ID        string          `json:"id,omitempty"`
	Timestamp string          `json:"timestamp,omitempty"`
	CWD       string          `json:"cwd,omitempty"`
	Runtime   string          `json:"runtime,omitempty"`
	Message   *sessionMessage `json:"message,omitempty"`
	Data      map[string]any  `json:"data,omitempty"`
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
	Input           int          `json:"input"`
	Output          int          `json:"output"`
	CacheRead       int          `json:"cacheRead"`
	CacheWrite      int          `json:"cacheWrite"`
	CostUnavailable bool         `json:"costUnavailable,omitempty"`
	Cost            *sessionCost `json:"cost,omitempty"`
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

func (l *nativeSessionLogger) contextPack(task string) error {
	return l.event("context_pack", map[string]any{
		"task_bytes":            len(task),
		"current_state_digest":  extractCurrentStateDigest(task),
		"current_state_present": strings.Contains(task, "## Current State Digest"),
	})
}

func (l *nativeSessionLogger) budget(maxToolLoops, maxOutputTokens int, budget game.TurnBudget) error {
	data := map[string]any{
		"max_tool_loops":    maxToolLoops,
		"max_output_tokens": maxOutputTokens,
	}
	if budget.MaxCostUSD != nil {
		data["max_cost_usd"] = *budget.MaxCostUSD
	}
	if budget.RemainingCostUSD != nil {
		data["remaining_cost_usd"] = *budget.RemainingCostUSD
	}
	if budget.MaxDurationSec > 0 {
		data["max_duration_sec"] = budget.MaxDurationSec
		data["remaining_duration_sec"] = budget.RemainingDurationSec
	}
	if budget.AgentTimeoutSec > 0 {
		data["agent_timeout_sec"] = budget.AgentTimeoutSec
	}
	if budget.MaxInputTokens > 0 {
		data["max_input_tokens"] = budget.MaxInputTokens
		data["remaining_input_tokens"] = budget.RemainingInputTokens
	}
	if budget.MaxOutputTokens > 0 {
		data["max_session_output_tokens"] = budget.MaxOutputTokens
		data["remaining_output_tokens"] = budget.RemainingOutputTokens
	}
	return l.event("budget", data)
}

// knobs records the resolved executor-internal env knobs for this turn, so a
// run is auditable and reproducible from the session log alone.
func (l *nativeSessionLogger) knobs(k envKnobs) error {
	return l.event("env_knobs", map[string]any{
		"tool_max_bytes":  k.ToolMaxBytes,
		"tool_max_lines":  k.ToolMaxLines,
		"keep_reasoning":  k.KeepReasoning,
	})
}

// providerConfig records the resolved model/provider configuration (minus
// secrets) so the capability profile and pricing availability are auditable
// per turn. API keys are never included.
func (l *nativeSessionLogger) providerConfig(cfg nativeProviderConfig) error {
	data := map[string]any{
		"provider":          cfg.Provider,
		"model":             cfg.Model,
		"state_mode":        cfg.Capability.StateMode,
		"strict_protocol":   cfg.Capability.StrictProtocol,
		"pricing_configured": pricingConfiguredFor(cfg.Model),
	}
	if cfg.Capability.MaxOutputTokens > 0 {
		data["capability_max_output_tokens"] = cfg.Capability.MaxOutputTokens
	}
	if cfg.Capability.SupportsReasoning != nil {
		data["supports_reasoning"] = *cfg.Capability.SupportsReasoning
	}
	if cfg.Capability.SupportsFunctionCalling != nil {
		data["supports_function_calling"] = *cfg.Capability.SupportsFunctionCalling
	}
	return l.event("provider_config", data)
}

func (l *nativeSessionLogger) assistant(text, provider, model, stopReason string, stats game.TurnStats) error {
	return l.message(&sessionMessage{
		Role:       "assistant",
		Provider:   provider,
		Model:      model,
		StopReason: stopReason,
		Content:    []sessionContent{{Type: "text", Text: text}},
		Usage: &sessionUsage{
			Input:           stats.InputTokens,
			Output:          stats.OutputTokens,
			CacheRead:       stats.CacheReadTokens,
			CacheWrite:      stats.CacheCreationTokens,
			CostUnavailable: stats.CostUnavailable,
			Cost:            sessionCostFromStats(stats),
		},
	})
}

func (l *nativeSessionLogger) tool(result nativeToolResult) error {
	data := map[string]any{
		"tool_call_id": result.CallID,
		"tool_name":    result.Name,
		"is_error":     result.IsError,
		"duration_ms":  result.DurationMS,
		"output_bytes": len(result.Output),
		"truncated":    result.Truncated,
	}
	if result.HasExitCode {
		data["exit_code"] = result.ExitCode
	}
	if result.ErrorCode != "" {
		data["error_code"] = string(result.ErrorCode)
	}
	if len(result.Metadata) > 0 {
		data["metadata"] = result.Metadata
	}
	if err := l.event("tool_result", data); err != nil {
		return err
	}
	return l.message(&sessionMessage{
		Role:       "toolResult",
		ToolCallID: result.CallID,
		ToolName:   result.Name,
		IsError:    result.IsError,
		Content:    []sessionContent{{Type: "text", Text: result.Output}},
	})
}

type modelRequestLogData struct {
	Sequence        int
	PreviousID      string
	StateMode       string
	Model           string
	MaxOutputTokens int
	ToolCount       int
	ReasoningEffort string
}

func (l *nativeSessionLogger) modelRequest(data modelRequestLogData) error {
	event := map[string]any{
		"sequence":             data.Sequence,
		"previous_response_id": data.PreviousID,
		"state_mode":           data.StateMode,
		"model":                data.Model,
		"max_output_tokens":    data.MaxOutputTokens,
		"tool_count":           data.ToolCount,
		"tools_enabled":        data.ToolCount > 0,
	}
	if data.ReasoningEffort != "" {
		event["reasoning_effort"] = data.ReasoningEffort
	}
	return l.event("model_request", event)
}

func (l *nativeSessionLogger) modelResponse(sequence int, responseID, stopReason string, stats game.TurnStats) error {
	return l.event("model_response", map[string]any{
		"sequence":    sequence,
		"response_id": responseID,
		"stop_reason": stopReason,
		"usage": map[string]any{
			"input":            stats.InputTokens,
			"output":           stats.OutputTokens,
			"cache_read":       stats.CacheReadTokens,
			"cache_write":      stats.CacheCreationTokens,
			"cost_usd":         stats.CostUSD,
			"cost_unavailable": stats.CostUnavailable,
		},
	})
}

func (l *nativeSessionLogger) toolCall(call nativeToolCall) error {
	return l.event("tool_call", map[string]any{
		"tool_call_id": call.ID,
		"tool_name":    call.Name,
		"arguments":    redactToolArguments(call.Arguments),
	})
}

func redactToolArguments(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var value any
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		if containsSensitiveArgumentKey(raw) {
			return "[REDACTED: non-JSON tool arguments contained sensitive key]"
		}
		return raw
	}
	value = redactArgumentValue(value)
	data, err := json.Marshal(value)
	if err != nil {
		return "[REDACTED: unmarshalable tool arguments]"
	}
	return string(data)
}

func redactArgumentValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, child := range v {
			if sensitiveArgumentKey(key) {
				out[key] = "[REDACTED]"
				continue
			}
			out[key] = redactArgumentValue(child)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			out[i] = redactArgumentValue(child)
		}
		return out
	default:
		return value
	}
}

func sensitiveArgumentKey(key string) bool {
	normalized := strings.ToLower(strings.NewReplacer("-", "_", ".", "_", " ", "_").Replace(strings.TrimSpace(key)))
	switch normalized {
	case "api_key", "apikey", "access_token", "refresh_token", "id_token", "auth_token", "token", "password", "passwd", "secret", "credential", "credentials", "authorization", "private_key", "client_secret", "bearer_token":
		return true
	default:
		return strings.HasSuffix(normalized, "_password") ||
			strings.HasSuffix(normalized, "_secret") ||
			strings.HasSuffix(normalized, "_credential") ||
			strings.HasSuffix(normalized, "_credentials") ||
			strings.HasSuffix(normalized, "_private_key")
	}
}

func containsSensitiveArgumentKey(text string) bool {
	lower := strings.ToLower(text)
	for _, marker := range []string{"api_key", "apikey", "access_token", "refresh_token", "auth_token", "password", "passwd", "secret", "credential", "authorization", "private_key", "client_secret", "bearer_token"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func (l *nativeSessionLogger) retry(sequence int, attempt int, delay time.Duration, err *executorError) error {
	data := map[string]any{
		"sequence": sequence,
		"attempt":  attempt,
		"delay_ms": delay.Milliseconds(),
	}
	if err != nil {
		data["error_code"] = string(err.Code)
		data["error"] = err.Message
		if err.StatusCode > 0 {
			data["provider_status_code"] = err.StatusCode
		}
	}
	return l.event("retry", data)
}

func (l *nativeSessionLogger) protocolCorrection(kind, prompt string) error {
	return l.event("protocol_correction", map[string]any{
		"kind":   kind,
		"prompt": prompt,
	})
}

func (l *nativeSessionLogger) reasoningLeak(removed string) error {
	return l.event("reasoning_sanitized", map[string]any{
		"removed": removed,
	})
}

func (l *nativeSessionLogger) skillOpened(name, path string, truncated bool) error {
	return l.event("skill_opened", map[string]any{
		"name":      name,
		"path":      path,
		"truncated": truncated,
	})
}

func (l *nativeSessionLogger) skillApplied(name, path string) error {
	return l.event("skill_applied", map[string]any{
		"name": name,
		"path": path,
	})
}

func (l *nativeSessionLogger) outsideWorkspaceAccess(action, path string, write bool) error {
	return l.event("outside_workspace_access", map[string]any{
		"action": action,
		"path":   path,
		"write":  write,
	})
}

func (l *nativeSessionLogger) errorEvent(sequence int, err error) error {
	data := map[string]any{
		"sequence": sequence,
		"error":    err.Error(),
	}
	var execErr *executorError
	if errors.As(err, &execErr) {
		data["error_code"] = string(execErr.Code)
		data["retryable"] = execErr.Retryable
		if execErr.StatusCode > 0 {
			data["provider_status_code"] = execErr.StatusCode
		}
	}
	return l.event("error", data)
}

func (l *nativeSessionLogger) message(msg *sessionMessage) error {
	if l == nil || l.path == "" {
		return nil
	}
	msg.Timestamp = time.Now().UnixMilli()
	return l.append(sessionEvent{
		Type:      "message",
		Version:   1,
		ID:        fmt.Sprintf("%s-%d", msg.Role, time.Now().UnixNano()),
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Message:   msg,
	})
}

func (l *nativeSessionLogger) append(event sessionEvent) error {
	if l == nil || l.path == "" {
		return nil
	}
	if event.Schema == "" {
		event.Schema = agentSessionSchema
	}
	if event.Version == 0 {
		event.Version = 1
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(event)
}

func (l *nativeSessionLogger) event(kind string, data map[string]any) error {
	if l == nil || l.path == "" {
		return nil
	}
	return l.append(sessionEvent{
		Type:      kind,
		Version:   1,
		ID:        fmt.Sprintf("%s-%d", kind, time.Now().UnixNano()),
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Data:      data,
	})
}

func sessionCostFromStats(stats game.TurnStats) *sessionCost {
	if stats.CostUnavailable {
		return nil
	}
	return &sessionCost{Total: stats.CostUSD}
}

func extractCurrentStateDigest(task string) string {
	const heading = "## Current State Digest"
	start := strings.Index(task, heading)
	if start < 0 {
		return ""
	}
	rest := task[start:]
	if next := strings.Index(rest[len(heading):], "\n## "); next >= 0 {
		rest = rest[:len(heading)+next]
	}
	const max = 4096
	if len(rest) > max {
		return rest[:max] + "\n... truncated ..."
	}
	return rest
}

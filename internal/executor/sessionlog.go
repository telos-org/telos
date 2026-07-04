package executor

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/telos-org/telos/internal/agentsession"
	"github.com/telos-org/telos/internal/game"
)

// -- Agent session contract --------------------------------------------------
//
// The envelope, message, and payload types live in internal/agentsession so
// the writer (this package) and the readers (replay, diagnostics) share a
// single typed contract.

type sessionEvent = agentsession.Event
type sessionMessage = agentsession.Message
type sessionContent = agentsession.Content
type sessionUsage = agentsession.Usage
type sessionCost = agentsession.Cost

// -- Session logging ---------------------------------------------------------
//
// PRIVACY / DATA HANDLING: session.jsonl is the redacted stream. Before events
// are written, assistant text, reasoning, tool arguments, and tool outputs are
// scrubbed for registry-declared sensitive fields and high-confidence secret
// patterns. Raw verbatim JSONL is written only when explicitly enabled with
// TELOS_SESSION_LOG_RAW (or TELOS_NATIVE_SESSION_LOG_RAW), and goes to a sibling
// *.raw.jsonl file; redacted logging remains enabled even then. Raw logs carry
// the same secret-bearing trust level as the workspace and must not back UI,
// API, SSE, or CLI surfaces.

type nativeSessionLogger struct {
	path            string
	rawPath         string
	workspace       string
	containmentMode ContainmentMode
	file            *os.File
	rawFile         *os.File
	rawEnabled      bool
	degraded        bool
	degradedErr     error
}

func newNativeSessionLogger(path, workspace string, mode ...ContainmentMode) *nativeSessionLogger {
	containment := ContainmentUncontained
	if len(mode) > 0 && mode[0] != "" {
		containment = mode[0]
	}
	return &nativeSessionLogger{
		path:            path,
		rawPath:         rawSessionLogPath(path),
		workspace:       workspace,
		containmentMode: containment,
		rawEnabled:      envBool("TELOS_SESSION_LOG_RAW", "TELOS_NATIVE_SESSION_LOG_RAW"),
	}
}

func (l *nativeSessionLogger) start() error {
	if l.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	l.file = f
	if l.rawEnabled {
		rf, err := os.OpenFile(l.rawPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			_ = l.close()
			return err
		}
		l.rawFile = rf
	}
	data := map[string]any{
		"containment_mode": string(l.containmentMode),
		"log_mode":         l.logMode(),
	}
	if rawPath := l.rawPathForEvent(); rawPath != "" {
		data["raw_log_path"] = rawPath
	}
	if err := l.append(sessionEvent{
		Type:      agentsession.KindSession,
		Version:   1,
		ID:        fmt.Sprintf("native-%d", time.Now().UnixNano()),
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		CWD:       l.workspace,
		Runtime:   "telos-native",
		Data:      mustRawJSON(data),
	}); err != nil {
		_ = l.close()
		return err
	}
	return nil
}

func (l *nativeSessionLogger) close() error {
	if l == nil {
		return nil
	}
	var err error
	if l.file != nil {
		err = l.file.Close()
		l.file = nil
	}
	if l.rawFile != nil {
		if rawErr := l.rawFile.Close(); err == nil {
			err = rawErr
		}
		l.rawFile = nil
	}
	return err
}

func (l *nativeSessionLogger) user(text string) error {
	return l.message(&sessionMessage{
		Role:    "user",
		Content: []sessionContent{{Type: "text", Text: text}},
	})
}

func (l *nativeSessionLogger) contextPack(task string) error {
	return l.event(agentsession.KindContextPack, agentsession.MarshalPayload(&agentsession.ContextPackPayload{
		TaskBytes:           len(task),
		CurrentStateDigest:  extractCurrentStateDigest(task),
		CurrentStatePresent: strings.Contains(task, "## Current State Digest"),
	}))
}

func (l *nativeSessionLogger) budget(maxToolLoops, maxOutputTokens int, budget game.TurnBudget) error {
	return l.event(agentsession.KindBudget, agentsession.MarshalPayload(&agentsession.BudgetPayload{
		MaxToolLoops:           maxToolLoops,
		MaxOutputTokens:        maxOutputTokens,
		MaxCostUSD:             budget.MaxCostUSD,
		RemainingCostUSD:       budget.RemainingCostUSD,
		CostHardLimit:          budget.CostHardLimit,
		MaxDurationSec:         budget.MaxDurationSec,
		RemainingDurationSec:   budget.RemainingDurationSec,
		AgentTimeoutSec:        budget.AgentTimeoutSec,
		MaxInputTokens:         budget.MaxInputTokens,
		RemainingInputTokens:   budget.RemainingInputTokens,
		MaxSessionOutputTokens: budget.MaxOutputTokens,
		RemainingOutputTokens:  budget.RemainingOutputTokens,
	}))
}

// knobs records the resolved executor-internal env knobs for this turn, so a
// run is auditable and reproducible from the session log alone.
func (l *nativeSessionLogger) knobs(k envKnobs) error {
	return l.event(agentsession.KindEnvKnobs, agentsession.MarshalPayload(&agentsession.EnvKnobsPayload{
		ToolMaxBytes:  k.ToolMaxBytes,
		ToolMaxLines:  k.ToolMaxLines,
		KeepReasoning: k.KeepReasoning,
	}))
}

// providerConfig records the resolved model/provider configuration (minus
// secrets) so the capability profile is auditable per turn. API keys are never
// included.
func (l *nativeSessionLogger) providerConfig(cfg nativeProviderConfig) error {
	return l.event(agentsession.KindProviderConfig, agentsession.MarshalPayload(&agentsession.ProviderConfigPayload{
		Provider:                cfg.Provider,
		Model:                   cfg.Model,
		Transport:               string(cfg.Transport),
		BaseURLKind:             string(cfg.Kind),
		StateMode:               cfg.Capability.StateMode,
		StrictProtocol:          cfg.Capability.StrictProtocol,
		CapabilityMaxOutput:     cfg.Capability.MaxOutputTokens,
		CapabilityContextWindow: cfg.Capability.effectiveContextWindow(cfg.Model),
		SupportsReasoning:       cfg.Capability.SupportsReasoning,
		SupportsFunctionCalling: cfg.Capability.SupportsFunctionCalling,
		CompactionContextWindow: cfg.Compaction.contextWindow,
		CompactionTriggerRatio:  cfg.Compaction.triggerRatio,
		CompactionKeepRecent:    cfg.Compaction.keepRecentTokens,
		CompactionStrategy:      cfg.Compaction.strategy,
	}))
}

// turnPolicy records the role and protocol mode for the turn so offline replay
// can validate the output contract with the same policy the live loop used,
// rather than guessing review-vs-pvg mode from the rendered prompt text.
func (l *nativeSessionLogger) turnPolicy(role, protocolMode string) error {
	return l.event(agentsession.KindTurnPolicy, agentsession.MarshalPayload(&agentsession.TurnPolicyPayload{
		Role:         role,
		ProtocolMode: protocolMode,
	}))
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
	if result.Metadata == nil {
		result.Metadata = map[string]any{}
	}
	result.Metadata["containment_mode"] = string(l.containmentMode)
	payload := agentsession.ToolResultPayload{
		ToolCallID:  result.CallID,
		ToolName:    result.Name,
		IsError:     result.IsError,
		DurationMS:  result.DurationMS,
		OutputBytes: len(result.Output),
		Truncated:   result.Truncated,
		ErrorCode:   string(result.ErrorCode),
		Metadata:    result.Metadata,
	}
	if result.HasExitCode {
		payload.ExitCode = result.ExitCode
	}
	if err := l.event(agentsession.KindToolResult, agentsession.MarshalPayload(&payload)); err != nil {
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
	Transport       string
	PreviousID      string
	StateMode       string
	Model           string
	MaxOutputTokens int
	ToolCount       int
	ReasoningEffort string
}

func (l *nativeSessionLogger) modelRequest(data modelRequestLogData) error {
	return l.event(agentsession.KindModelRequest, agentsession.MarshalPayload(&agentsession.ModelRequestPayload{
		Sequence:        data.Sequence,
		Transport:       data.Transport,
		PreviousID:      data.PreviousID,
		StateMode:       data.StateMode,
		Model:           data.Model,
		MaxOutputTokens: data.MaxOutputTokens,
		ToolCount:       data.ToolCount,
		ToolsEnabled:    data.ToolCount > 0,
		ReasoningEffort: data.ReasoningEffort,
	}))
}

func (l *nativeSessionLogger) modelAsyncJob(payload agentsession.ModelAsyncJobPayload) error {
	return l.event(agentsession.KindModelAsyncJob, agentsession.MarshalPayload(&payload))
}

func (l *nativeSessionLogger) modelResponse(sequence int, responseID, asyncJobID, stopReason string, stats game.TurnStats) error {
	return l.event(agentsession.KindModelResponse, agentsession.MarshalPayload(&agentsession.ModelResponsePayload{
		Sequence:   sequence,
		ResponseID: responseID,
		AsyncJobID: asyncJobID,
		StopReason: stopReason,
		Usage: agentsession.ModelResponseUsage{
			Input:           stats.InputTokens,
			Output:          stats.OutputTokens,
			CacheRead:       stats.CacheReadTokens,
			CacheWrite:      stats.CacheCreationTokens,
			CostUSD:         stats.CostUSD,
			CostUnavailable: stats.CostUnavailable,
		},
	}))
}

// toolCall records tool arguments verbatim. The session log also records tool
// outputs verbatim, so session.jsonl may contain workspace secrets and must be
// handled at the same trust level as the workspace itself; see tools.go for the
// native executor security model.
func (l *nativeSessionLogger) toolCall(call nativeToolCall) error {
	return l.event(agentsession.KindToolCall, agentsession.MarshalPayload(&agentsession.ToolCallPayload{
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Arguments:  call.Arguments,
	}))
}

func (l *nativeSessionLogger) retry(sequence int, attempt int, delay time.Duration, err *executorError) error {
	payload := agentsession.RetryPayload{
		Sequence: sequence,
		Attempt:  attempt,
		DelayMS:  delay.Milliseconds(),
	}
	if err != nil {
		payload.ErrorCode = string(err.Code)
		payload.Error = err.Message
		if err.StatusCode > 0 {
			payload.ProviderStatusCode = err.StatusCode
		}
	}
	return l.event(agentsession.KindRetry, agentsession.MarshalPayload(&payload))
}

func (l *nativeSessionLogger) protocolCorrection(kind, prompt string) error {
	return l.event(agentsession.KindProtocolCorrection, agentsession.MarshalPayload(&agentsession.ProtocolCorrectionPayload{
		Kind:   kind,
		Prompt: prompt,
	}))
}

func (l *nativeSessionLogger) reasoningLeak(removed string) error {
	return l.event(agentsession.KindReasoningSanitized, agentsession.MarshalPayload(&agentsession.ReasoningSanitizedPayload{
		Removed: removed,
	}))
}

func (l *nativeSessionLogger) skillOpened(name, path string, truncated bool) error {
	return l.event(agentsession.KindSkillOpened, agentsession.MarshalPayload(&agentsession.SkillOpenedPayload{
		Name:      name,
		Path:      path,
		Truncated: truncated,
	}))
}

func (l *nativeSessionLogger) skillApplied(name, path string) error {
	return l.event(agentsession.KindSkillApplied, agentsession.MarshalPayload(&agentsession.SkillAppliedPayload{
		Name: name,
		Path: path,
	}))
}

func (l *nativeSessionLogger) outsideWorkspaceAccess(action, path string, write bool) error {
	return l.event(agentsession.KindOutsideWorkspaceAccess, agentsession.MarshalPayload(&agentsession.OutsideWorkspaceAccessPayload{
		Action: action,
		Path:   path,
		Write:  write,
	}))
}

func (l *nativeSessionLogger) terminal(state TerminalState) error {
	return l.event("terminal", mustRawJSON(map[string]any{
		"terminal_state":   string(state),
		"containment_mode": string(l.containmentMode),
	}))
}

func (l *nativeSessionLogger) compaction(p compactionEventPayload) error {
	return l.event(agentsession.KindCompaction, agentsession.MarshalPayload(&p))
}

func (l *nativeSessionLogger) errorEvent(sequence int, err error) error {
	payload := agentsession.ErrorPayload{
		Sequence: sequence,
		Error:    err.Error(),
	}
	var execErr *executorError
	if errors.As(err, &execErr) {
		payload.ErrorCode = string(execErr.Code)
		payload.Retryable = execErr.Retryable
		if execErr.StatusCode > 0 {
			payload.ProviderStatusCode = execErr.StatusCode
		}
	}
	return l.event(agentsession.KindError, agentsession.MarshalPayload(&payload))
}

func (l *nativeSessionLogger) message(msg *sessionMessage) error {
	if l == nil || l.path == "" {
		return nil
	}
	msg.Timestamp = time.Now().UnixMilli()
	return l.append(sessionEvent{
		Type:      agentsession.KindMessage,
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
	if l.file == nil {
		return l.markDegraded(fmt.Errorf("native session logger not started"))
	}
	if event.Schema == "" {
		event.Schema = agentsession.Schema
	}
	if event.Version == 0 {
		event.Version = 1
	}
	if err := json.NewEncoder(l.file).Encode(l.redactedEvent(event)); err != nil {
		return l.markDegraded(err)
	}
	if l.rawEnabled {
		if l.rawFile == nil {
			return l.markDegraded(fmt.Errorf("native raw session logger not started"))
		}
		if err := json.NewEncoder(l.rawFile).Encode(event); err != nil {
			return l.markDegraded(err)
		}
	}
	return nil
}

func (l *nativeSessionLogger) event(kind string, data json.RawMessage) error {
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

func mustRawJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return data
}

func (l *nativeSessionLogger) redactedEvent(event sessionEvent) sessionEvent {
	if event.Message != nil {
		msg := *event.Message
		msg.Content = append([]sessionContent(nil), event.Message.Content...)
		for i := range msg.Content {
			if msg.Content[i].Type != "text" {
				continue
			}
			if msg.Role == "toolResult" {
				msg.Content[i].Text = redactToolOutput(msg.ToolName, msg.Content[i].Text)
			} else {
				msg.Content[i].Text = scrubSecrets(msg.Content[i].Text)
			}
		}
		event.Message = &msg
	}
	switch event.Type {
	case agentsession.KindToolCall:
		var p agentsession.ToolCallPayload
		if err := json.Unmarshal(event.Data, &p); err == nil {
			p.Arguments = redactToolArguments(p.ToolName, p.Arguments)
			event.Data = agentsession.MarshalPayload(&p)
		} else {
			event.Data = scrubJSONStrings(event.Data)
		}
	case agentsession.KindReasoningSanitized:
		var p agentsession.ReasoningSanitizedPayload
		if err := json.Unmarshal(event.Data, &p); err == nil {
			p.Removed = scrubSecrets(p.Removed)
			event.Data = agentsession.MarshalPayload(&p)
		} else {
			event.Data = scrubJSONStrings(event.Data)
		}
	default:
		event.Data = scrubJSONStrings(event.Data)
	}
	return event
}

func (l *nativeSessionLogger) markDegraded(err error) error {
	if err == nil {
		return nil
	}
	if l != nil {
		l.degraded = true
		l.degradedErr = err
		l.logDegradedEvent(err)
	}
	return err
}

func (l *nativeSessionLogger) logDegradedEvent(err error) {
	if l == nil || err == nil {
		return
	}
	event := sessionEvent{
		Schema:    agentsession.Schema,
		Type:      "session_degraded",
		Version:   1,
		ID:        fmt.Sprintf("session_degraded-%d", time.Now().UnixNano()),
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Data: mustRawJSON(map[string]any{
			"reason": err.Error(),
		}),
	}
	if l.file != nil {
		_ = json.NewEncoder(l.file).Encode(event)
	}
	if l.rawEnabled && l.rawFile != nil {
		_ = json.NewEncoder(l.rawFile).Encode(event)
	}
}

func (l *nativeSessionLogger) degradedError() error {
	if l == nil || !l.degraded || l.degradedErr == nil {
		return nil
	}
	return newExecutorError(errToolInfra, "native_session_degraded:"+l.degradedErr.Error())
}

func (l *nativeSessionLogger) logMode() string {
	if l != nil && l.rawEnabled {
		return "redacted+raw"
	}
	return "redacted"
}

func (l *nativeSessionLogger) rawPathForEvent() string {
	if l == nil || !l.rawEnabled {
		return ""
	}
	return l.rawPath
}

func rawSessionLogPath(path string) string {
	if path == "" {
		return ""
	}
	ext := filepath.Ext(path)
	if ext == "" {
		return path + ".raw"
	}
	return strings.TrimSuffix(path, ext) + ".raw" + ext
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

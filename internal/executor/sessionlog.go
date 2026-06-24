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
// PRIVACY / DATA HANDLING: this logger records assistant text, tool arguments,
// and tool outputs verbatim and applies no redaction. Because the executor is
// unsandboxed (see tools.go), `bash` output and file reads routinely place
// environment variables, tokens, and raw file contents into session.jsonl. The
// file therefore carries the same secret-bearing trust level as the workspace
// itself, and downstream handling must respect that: it may be uploaded to and
// retained in artifact storage (e.g. the evals object store), so anywhere it is
// shipped or granted read access must be treated as exposing workspace secrets.
// This is an accepted property of the trust model, not an incidental leak.

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
		Type:      agentsession.KindSession,
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
// secrets) so the capability profile and pricing availability are auditable
// per turn. API keys are never included.
func (l *nativeSessionLogger) providerConfig(cfg nativeProviderConfig) error {
	return l.event(agentsession.KindProviderConfig, agentsession.MarshalPayload(&agentsession.ProviderConfigPayload{
		Provider:                cfg.Provider,
		Model:                   cfg.Model,
		StateMode:               cfg.Capability.StateMode,
		StrictProtocol:          cfg.Capability.StrictProtocol,
		PricingConfigured:       cfg.PricingConfigured,
		CapabilityMaxOutput:     cfg.Capability.MaxOutputTokens,
		CapabilityContextWindow: cfg.Capability.effectiveContextWindow(cfg.Model),
		SupportsReasoning:       cfg.Capability.SupportsReasoning,
		SupportsFunctionCalling: cfg.Capability.SupportsFunctionCalling,
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
		PreviousID:      data.PreviousID,
		StateMode:       data.StateMode,
		Model:           data.Model,
		MaxOutputTokens: data.MaxOutputTokens,
		ToolCount:       data.ToolCount,
		ToolsEnabled:    data.ToolCount > 0,
		ReasoningEffort: data.ReasoningEffort,
	}))
}

func (l *nativeSessionLogger) modelResponse(sequence int, responseID, stopReason string, stats game.TurnStats) error {
	return l.event(agentsession.KindModelResponse, agentsession.MarshalPayload(&agentsession.ModelResponsePayload{
		Sequence:   sequence,
		ResponseID: responseID,
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

func (l *nativeSessionLogger) compaction(p agentsession.CompactionPayload) error {
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
	if event.Schema == "" {
		event.Schema = agentsession.Schema
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

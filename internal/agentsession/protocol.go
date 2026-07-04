// Package agentsession defines the typed contract for the per-turn session
// JSONL written by the native executor and read by replay and diagnostics.
// Both the executor (writer) and sessionapi (reader) import this package
// so the event kinds, data keys, and payload shapes cannot drift apart.
package agentsession

import "encoding/json"

// Schema is the schema identifier stamped on every session JSONL file.
const Schema = "telos.agent_session.v1"

// SchemaVersion is the major protocol version for Event. Within one major
// version, changes are additive only: writers may add fields and readers must
// ignore unknown fields. Removing fields, changing meanings, or changing JSON
// types requires a new major version.
const SchemaVersion = 1

// Event kind constants. These are the string values written to Event.Type
// and read by consumers to dispatch on event kind.
const (
	KindSession                = "session"
	KindMessage                = "message"
	KindContextPack            = "context_pack"
	KindBudget                 = "budget"
	KindEnvKnobs               = "env_knobs"
	KindProviderConfig         = "provider_config"
	KindTurnPolicy             = "turn_policy"
	KindModelRequest           = "model_request"
	KindModelAsyncJob          = "model_async_job"
	KindModelResponse          = "model_response"
	KindToolCall               = "tool_call"
	KindToolResult             = "tool_result"
	KindRetry                  = "retry"
	KindProtocolCorrection     = "protocol_correction"
	KindReasoningSanitized     = "reasoning_sanitized"
	KindSkillOpened            = "skill_opened"
	KindSkillApplied           = "skill_applied"
	KindOutsideWorkspaceAccess = "outside_workspace_access"
	KindCompaction             = "compaction"
	KindError                  = "error"
	KindAssistantText          = "assistant_text"
	KindReasoning              = "reasoning"
	KindToolCallStart          = "tool_call_start"
	KindToolCallResult         = "tool_call_result"
	KindUsage                  = "usage"
	KindLifecycle              = "lifecycle"
	KindTerminal               = "terminal"
)

// Event is the JSONL envelope. Data carries the kind-specific payload as raw
// JSON so readers unmarshal into the appropriate typed struct.
type Event struct {
	Schema        string          `json:"schema,omitempty"`
	SchemaVersion int             `json:"schema_version,omitempty"`
	Type          string          `json:"type"`
	Version       int             `json:"version,omitempty"`
	Sequence      int64           `json:"sequence,omitempty"`
	ID            string          `json:"id,omitempty"`
	Timestamp     string          `json:"timestamp,omitempty"`
	CWD           string          `json:"cwd,omitempty"`
	Runtime       string          `json:"runtime,omitempty"`
	Message       *Message        `json:"message,omitempty"`
	Data          json.RawMessage `json:"data,omitempty"`
}

// Message is the typed payload for KindMessage events (user, assistant,
// toolResult roles).
type Message struct {
	Role         string    `json:"role"`
	Timestamp    int64     `json:"timestamp,omitempty"`
	Provider     string    `json:"provider,omitempty"`
	Model        string    `json:"model,omitempty"`
	StopReason   string    `json:"stopReason,omitempty"`
	Content      []Content `json:"content,omitempty"`
	Usage        *Usage    `json:"usage,omitempty"`
	ToolCallID   string    `json:"toolCallId,omitempty"`
	ToolName     string    `json:"toolName,omitempty"`
	IsError      bool      `json:"isError,omitempty"`
	ErrorMessage string    `json:"errorMessage,omitempty"`
}

type Content struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type Usage struct {
	Input           int   `json:"input"`
	Output          int   `json:"output"`
	CacheRead       int   `json:"cacheRead"`
	CacheWrite      int   `json:"cacheWrite"`
	CostUnavailable bool  `json:"costUnavailable,omitempty"`
	Cost            *Cost `json:"cost,omitempty"`
}

type Cost struct {
	Total float64 `json:"total"`
}

type TerminalState string

const (
	TerminalCompleted      TerminalState = "completed"
	TerminalIncomplete     TerminalState = "incomplete"
	TerminalExhausted      TerminalState = "exhausted"
	TerminalInterrupted    TerminalState = "interrupted"
	TerminalPolicyBlocked  TerminalState = "policy_blocked"
	TerminalProviderFailed TerminalState = "provider_failed"
	TerminalToolFailed     TerminalState = "tool_failed"
)

// -- Typed payloads for Data -----------------------------------------------

type ContextPackPayload struct {
	TaskBytes           int    `json:"task_bytes"`
	CurrentStateDigest  string `json:"current_state_digest"`
	CurrentStatePresent bool   `json:"current_state_present"`
}

type BudgetPayload struct {
	MaxToolLoops           int      `json:"max_tool_loops"`
	MaxOutputTokens        int      `json:"max_output_tokens"`
	MaxCostUSD             *float64 `json:"max_cost_usd,omitempty"`
	RemainingCostUSD       *float64 `json:"remaining_cost_usd,omitempty"`
	CostHardLimit          bool     `json:"cost_hard_limit,omitempty"`
	MaxDurationSec         int      `json:"max_duration_sec,omitempty"`
	RemainingDurationSec   int      `json:"remaining_duration_sec,omitempty"`
	AgentTimeoutSec        int      `json:"agent_timeout_sec,omitempty"`
	MaxInputTokens         int      `json:"max_input_tokens,omitempty"`
	RemainingInputTokens   int      `json:"remaining_input_tokens,omitempty"`
	MaxSessionOutputTokens int      `json:"max_session_output_tokens,omitempty"`
	RemainingOutputTokens  int      `json:"remaining_output_tokens,omitempty"`
}

type EnvKnobsPayload struct {
	ToolMaxBytes  int  `json:"tool_max_bytes"`
	ToolMaxLines  int  `json:"tool_max_lines"`
	KeepReasoning bool `json:"keep_reasoning"`
}

type ProviderConfigPayload struct {
	Provider                string  `json:"provider"`
	Model                   string  `json:"model"`
	Transport               string  `json:"transport,omitempty"`
	BaseURLKind             string  `json:"base_url_kind,omitempty"`
	StateMode               string  `json:"state_mode"`
	StrictProtocol          bool    `json:"strict_protocol"`
	CapabilityMaxOutput     int     `json:"capability_max_output_tokens,omitempty"`
	CapabilityContextWindow int     `json:"capability_context_window,omitempty"`
	SupportsReasoning       *bool   `json:"supports_reasoning,omitempty"`
	SupportsFunctionCalling *bool   `json:"supports_function_calling,omitempty"`
	CompactionContextWindow int     `json:"compaction_context_window,omitempty"`
	CompactionTriggerRatio  float64 `json:"compaction_trigger_ratio,omitempty"`
	CompactionKeepRecent    int     `json:"compaction_keep_recent_tokens,omitempty"`
	CompactionStrategy      string  `json:"compaction_strategy,omitempty"`
}

type TurnPolicyPayload struct {
	Role         string `json:"role"`
	ProtocolMode string `json:"protocol_mode"`
}

type ModelRequestPayload struct {
	Sequence        int    `json:"sequence"`
	Transport       string `json:"transport,omitempty"`
	PreviousID      string `json:"previous_response_id"`
	StateMode       string `json:"state_mode"`
	Model           string `json:"model"`
	MaxOutputTokens int    `json:"max_output_tokens"`
	ToolCount       int    `json:"tool_count"`
	ToolsEnabled    bool   `json:"tools_enabled"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

type ModelAsyncJobPayload struct {
	Sequence  int    `json:"sequence"`
	JobID     string `json:"job_id"`
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	Transport string `json:"transport"`
	Status    string `json:"status"`
}

type ModelResponsePayload struct {
	Sequence   int                `json:"sequence"`
	ResponseID string             `json:"response_id"`
	AsyncJobID string             `json:"async_job_id,omitempty"`
	StopReason string             `json:"stop_reason"`
	Usage      ModelResponseUsage `json:"usage"`
}

type ModelResponseUsage struct {
	Input           int     `json:"input"`
	Output          int     `json:"output"`
	CacheRead       int     `json:"cache_read"`
	CacheWrite      int     `json:"cache_write"`
	CostUSD         float64 `json:"cost_usd"`
	CostUnavailable bool    `json:"cost_unavailable"`
}

type ToolCallPayload struct {
	ToolCallID      string         `json:"tool_call_id"`
	ToolName        string         `json:"tool_name"`
	Arguments       string         `json:"arguments,omitempty"`
	ContainmentMode string         `json:"containment_mode,omitempty"`
	ChangedFiles    []string       `json:"changed_files,omitempty"`
	Redacted        bool           `json:"redacted,omitempty"`
	Redaction       map[string]any `json:"redaction,omitempty"`
}

type ToolResultPayload struct {
	ToolCallID  string         `json:"tool_call_id"`
	ToolName    string         `json:"tool_name"`
	IsError     bool           `json:"is_error"`
	DurationMS  int64          `json:"duration_ms"`
	OutputBytes int            `json:"output_bytes"`
	Truncated   bool           `json:"truncated"`
	ExitCode    int            `json:"exit_code,omitempty"`
	ErrorCode   string         `json:"error_code,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

type RetryPayload struct {
	Sequence           int    `json:"sequence"`
	Attempt            int    `json:"attempt"`
	DelayMS            int64  `json:"delay_ms"`
	ErrorCode          string `json:"error_code,omitempty"`
	Error              string `json:"error,omitempty"`
	ProviderStatusCode int    `json:"provider_status_code,omitempty"`
}

type ProtocolCorrectionPayload struct {
	Kind   string `json:"kind"`
	Prompt string `json:"prompt"`
}

type ReasoningSanitizedPayload struct {
	Removed string `json:"removed"`
}

type SkillOpenedPayload struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Truncated bool   `json:"truncated"`
}

type SkillAppliedPayload struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type OutsideWorkspaceAccessPayload struct {
	Action string `json:"action"`
	Path   string `json:"path"`
	Write  bool   `json:"write"`
}

// CompactionPayload records a stateless_history autocompaction pass. It is
// emitted for successful LLM summaries and for failed compaction attempts that
// prevent the normal agent request from being sent.
type CompactionPayload struct {
	Reason          string             `json:"reason"`
	FirstKeptIndex  int                `json:"first_kept_index"`
	TokensBefore    int                `json:"tokens_before"`
	TokensAfter     int                `json:"tokens_after"`
	SummaryTokens   int                `json:"summary_tokens,omitempty"`
	ItemsSummarized int                `json:"items_summarized"`
	ItemsKept       int                `json:"items_kept"`
	Model           string             `json:"model,omitempty"`
	ResponseID      string             `json:"response_id,omitempty"`
	Usage           ModelResponseUsage `json:"usage,omitempty"`
	Details         CompactionDetails  `json:"details,omitempty"`
	Error           string             `json:"error,omitempty"`
}

type CompactionDetails struct {
	ReadFiles     []string `json:"read_files,omitempty"`
	ModifiedFiles []string `json:"modified_files,omitempty"`
}

type ErrorPayload struct {
	Sequence           int    `json:"sequence"`
	Error              string `json:"error"`
	ErrorCode          string `json:"error_code,omitempty"`
	Retryable          bool   `json:"retryable,omitempty"`
	ProviderStatusCode int    `json:"provider_status_code,omitempty"`
}

type AssistantTextPayload struct {
	Text       string `json:"text"`
	Provider   string `json:"provider,omitempty"`
	Model      string `json:"model,omitempty"`
	StopReason string `json:"stop_reason,omitempty"`
}

type ReasoningPayload struct {
	Text     string `json:"text,omitempty"`
	Redacted bool   `json:"redacted,omitempty"`
	Removed  bool   `json:"removed,omitempty"`
}

type ToolCallStartPayload = ToolCallPayload

type ToolCallResultStreamPayload struct {
	ToolCallID      string         `json:"tool_call_id"`
	ToolName        string         `json:"tool_name"`
	IsError         bool           `json:"is_error,omitempty"`
	DurationMS      int64          `json:"duration_ms,omitempty"`
	OutputBytes     int            `json:"output_bytes,omitempty"`
	Truncated       bool           `json:"truncated,omitempty"`
	ExitCode        int            `json:"exit_code,omitempty"`
	ErrorCode       string         `json:"error_code,omitempty"`
	Preview         string         `json:"preview,omitempty"`
	ContainmentMode string         `json:"containment_mode,omitempty"`
	ChangedFiles    []string       `json:"changed_files,omitempty"`
	Redacted        bool           `json:"redacted,omitempty"`
	Redaction       map[string]any `json:"redaction,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
}

type UsagePayload struct {
	Input           int     `json:"input"`
	Output          int     `json:"output"`
	CacheRead       int     `json:"cache_read,omitempty"`
	CacheWrite      int     `json:"cache_write,omitempty"`
	CostUSD         float64 `json:"cost_usd,omitempty"`
	CostUnavailable bool    `json:"cost_unavailable,omitempty"`
	Provider        string  `json:"provider,omitempty"`
	Model           string  `json:"model,omitempty"`
}

type LifecyclePayload struct {
	State    string         `json:"state"`
	Status   string         `json:"status,omitempty"`
	PID      int            `json:"pid,omitempty"`
	PGID     int            `json:"pgid,omitempty"`
	Signal   string         `json:"signal,omitempty"`
	ExitCode int            `json:"exit_code,omitempty"`
	Error    string         `json:"error,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type TerminalPayload struct {
	TerminalState   TerminalState `json:"terminal_state"`
	ContainmentMode string        `json:"containment_mode,omitempty"`
	Error           string        `json:"error,omitempty"`
}

// Unmarshal decodes the Event's Data field into the provided payload.
func Unmarshal[T any](e *Event) (*T, error) {
	var p T
	if err := json.Unmarshal(e.Data, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// MarshalPayload marshals a payload into json.RawMessage for the Data field.
func MarshalPayload(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return data
}

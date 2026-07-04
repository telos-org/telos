// Package sessionapi defines the canonical JSON request/response types and
// HTTP routes for the Telos Sessions API. Both local and cloud deployments
// serve the same contract; they differ by adapters for auth, store, launcher,
// and workspace.
package sessionapi

import (
	"encoding/json"
	"fmt"

	"github.com/telos-org/telos/internal/gatewaycred"
)

// --------- Enums ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------

// SessionStatus enumerates the lifecycle states of a session.
type SessionStatus string

const (
	StatusPending   SessionStatus = "pending"
	StatusRunning   SessionStatus = "running"
	StatusCompleted SessionStatus = "completed"
	StatusFailed    SessionStatus = "failed"
	StatusStopped   SessionStatus = "stopped"
	StatusStale     SessionStatus = "stale"
)

// IsTerminal returns true for statuses that indicate no further progress.
func (s SessionStatus) IsTerminal() bool {
	switch s {
	case StatusCompleted, StatusFailed, StatusStopped, StatusStale:
		return true
	}
	return false
}

// SessionRuntime distinguishes local from cloud deployments.
type SessionRuntime string

const (
	RuntimeLocal SessionRuntime = "local"
	RuntimeCloud SessionRuntime = "cloud"
)

func (r *SessionRuntime) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	runtime := SessionRuntime(value)
	switch runtime {
	case RuntimeLocal, RuntimeCloud:
		*r = runtime
		return nil
	default:
		return fmt.Errorf("invalid session runtime %q", value)
	}
}

// ModelProfile aliases the gatewaycred type so the managed tier selection is
// one type from CLI flag to minted gateway credential.
type ModelProfile = gatewaycred.ModelProfile

const (
	ModelProfileStandard = gatewaycred.ModelProfileStandard
	ModelProfilePremium  = gatewaycred.ModelProfilePremium
)

func NormalizeModelProfile(value string) (ModelProfile, error) {
	return gatewaycred.NormalizeModelProfile(value)
}

func BifrostAgentModel(profile ModelProfile) string {
	if profile == ModelProfilePremium {
		return "telos-bifrost/premium-agent"
	}
	return "telos-bifrost/standard-agent"
}

func BifrostCompactionModel(profile ModelProfile) string {
	if profile == ModelProfilePremium {
		return "telos-bifrost/premium-compaction"
	}
	return "telos-bifrost/standard-compaction"
}

// SessionKind is the persisted worker kind backing root and child sessions.
type SessionKind string

const (
	KindController SessionKind = "controller"
	KindTask       SessionKind = "task"
)

func (k *SessionKind) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	kind := SessionKind(value)
	switch kind {
	case KindController, KindTask:
		*k = kind
		return nil
	default:
		return fmt.Errorf("invalid session_kind %q", value)
	}
}

// --------- Request types ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------

// SessionCreateRequest is the body of POST /api/sessions.
type SessionCreateRequest struct {
	SpecMarkdown       *string      `json:"spec_markdown,omitempty"`
	ApplyPackagePath   string       `json:"-"`
	ApplyPackageDigest string       `json:"-"`
	SessionKind        *SessionKind `json:"-"`
	ParentSessionID    *string      `json:"parent_session_id,omitempty"`
	UserAuthorization  string       `json:"-"`
	UserOrgID          string       `json:"-"`
	Until              *int         `json:"until,omitempty"`
	Model              string       `json:"model,omitempty"`
	ModelProfile       ModelProfile `json:"model_profile,omitempty"`
	Thinking           string       `json:"thinking,omitempty"`
	MaxCostUSD         *float64     `json:"max_cost_usd,omitempty"`
	MaxRounds          *int         `json:"max_rounds,omitempty"`
	MaxDurationSec     *int         `json:"max_duration_sec,omitempty"`
	MaxInputTokens     *int         `json:"max_input_tokens,omitempty"`
	MaxOutputTokens    *int         `json:"max_output_tokens,omitempty"`
	MaxToolLoops       *int         `json:"max_tool_loops,omitempty"`
	AgentTimeoutSec    *int         `json:"agent_timeout_sec,omitempty"`
}

// SessionSpecUpdateRequest is the body of PUT /api/sessions/{name}/spec.
type SessionSpecUpdateRequest struct {
	SpecMarkdown      string       `json:"spec_markdown"`
	PackageDigest     string       `json:"package_digest,omitempty"`
	UserAuthorization string       `json:"-"`
	UserOrgID         string       `json:"-"`
	Model             string       `json:"model,omitempty"`
	ModelProfile      ModelProfile `json:"model_profile,omitempty"`
	Thinking          string       `json:"thinking,omitempty"`
	MaxCostUSD        *float64     `json:"max_cost_usd,omitempty"`
	MaxRounds         *int         `json:"max_rounds,omitempty"`
	MaxDurationSec    *int         `json:"max_duration_sec,omitempty"`
	MaxInputTokens    *int         `json:"max_input_tokens,omitempty"`
	MaxOutputTokens   *int         `json:"max_output_tokens,omitempty"`
	MaxToolLoops      *int         `json:"max_tool_loops,omitempty"`
	AgentTimeoutSec   *int         `json:"agent_timeout_sec,omitempty"`
}

// SessionSpecUpdateResponse is returned by PUT /api/sessions/{name}/spec.
type SessionSpecUpdateResponse struct {
	Operation string   `json:"operation"`
	Session   *Session `json:"session"`
}

// SessionSpecResponse is returned by GET /api/sessions/{id}/spec.
type SessionSpecResponse struct {
	DirName     string `json:"dir_name"`
	Markdown    string `json:"markdown"`
	Environment string `json:"environment"`
	Version     *int   `json:"version,omitempty"`
}

// --------- Spec types ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------

// SessionSpec describes one compiled spec entry inside a session.
type SessionSpec struct {
	Index                  *int     `json:"index,omitempty"`
	Name                   *string  `json:"name,omitempty"`
	DirName                *string  `json:"dir_name,omitempty"`
	SessionSpecPath        *string  `json:"session_spec_path,omitempty"`
	ContentHash            *string  `json:"content_hash,omitempty"`
	EvidencePath           *string  `json:"evidence_path,omitempty"`
	EvidenceExists         *bool    `json:"evidence_exists,omitempty"`
	TranscriptPath         *string  `json:"transcript_path,omitempty"`
	TranscriptExists       *bool    `json:"transcript_exists,omitempty"`
	ObjectiveLedgerPath    *string  `json:"objective_ledger_path,omitempty"`
	ObjectiveLedgerExists  *bool    `json:"objective_ledger_exists,omitempty"`
	WorkspacePath          *string  `json:"workspace_path,omitempty"`
	WorkspaceExists        *bool    `json:"workspace_exists,omitempty"`
	IntervalSeconds        *int     `json:"interval_seconds,omitempty"`
	TotalCostUSD           *float64 `json:"total_cost_usd,omitempty"`
	CostUnavailable        *bool    `json:"cost_unavailable,omitempty"`
	TotalInputTokens       *int     `json:"total_input_tokens,omitempty"`
	TotalOutputTokens      *int     `json:"total_output_tokens,omitempty"`
	TotalCacheReadTokens   *int     `json:"total_cache_read_tokens,omitempty"`
	TotalCacheCreateTokens *int     `json:"total_cache_creation_tokens,omitempty"`
	RoundCount             *int     `json:"round_count,omitempty"`
	CompletionReason       *string  `json:"completion_reason,omitempty"`
	VerifierConceded       *bool    `json:"verifier_conceded,omitempty"`
	CurrentRound           *int     `json:"current_round,omitempty"`
	CurrentRole            *string  `json:"current_role,omitempty"`
}

// CurrentSpec identifies the spec currently being executed.
type CurrentSpec struct {
	Index   *int    `json:"index,omitempty"`
	Name    *string `json:"name,omitempty"`
	DirName *string `json:"dir_name,omitempty"`
}

// --------- Session types ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------

// SessionSummary is the minimal identification of a session.
type SessionSummary struct {
	SessionID       string        `json:"session_id"`
	SessionKind     *SessionKind  `json:"-"`
	ParentSessionID *string       `json:"parent_session_id,omitempty"`
	SpecName        *string       `json:"spec_name,omitempty"`
	Status          SessionStatus `json:"status"`
	CreatedAt       *string       `json:"created_at,omitempty"`
}

// Session is the full API representation returned by get/create/stop.
type Session struct {
	SessionID       string        `json:"session_id"`
	SessionKind     *SessionKind  `json:"-"`
	ParentSessionID *string       `json:"parent_session_id,omitempty"`
	SpecName        *string       `json:"spec_name,omitempty"`
	Status          SessionStatus `json:"status"`
	CreatedAt       *string       `json:"created_at,omitempty"`

	Runtime                 SessionRuntime       `json:"runtime"`
	Launcher                *string              `json:"launcher,omitempty"`
	SessionSpecPath         *string              `json:"session_spec_path,omitempty"`
	SessionDir              *string              `json:"session_dir,omitempty"`
	ActiveWorkspacePath     *string              `json:"active_workspace_path,omitempty"`
	ActiveWorkspaceExists   *bool                `json:"active_workspace_exists,omitempty"`
	Config                  map[string]any       `json:"config"`
	Workspace               *Workspace           `json:"workspace,omitempty"`
	Provenance              map[string]any       `json:"provenance"`
	GatewayRouting          *GatewayRoutingState `json:"gateway_routing,omitempty"`
	Specs                   []SessionSpec        `json:"specs"`
	Epochs                  []map[string]any     `json:"epochs"`
	CurrentEpoch            *int                 `json:"current_epoch,omitempty"`
	CurrentSpec             *CurrentSpec         `json:"current_spec,omitempty"`
	CurrentRound            *int                 `json:"current_round,omitempty"`
	CurrentRole             *string              `json:"current_role,omitempty"`
	FinishedAt              *string              `json:"finished_at,omitempty"`
	Result                  *string              `json:"result,omitempty"`
	Error                   *string              `json:"error,omitempty"`
	ErrorCode               *string              `json:"error_code,omitempty"`
	TotalCostUSD            *float64             `json:"total_cost_usd,omitempty"`
	CostUnavailable         *bool                `json:"cost_unavailable,omitempty"`
	TotalInputTokens        *int                 `json:"total_input_tokens,omitempty"`
	TotalOutputTokens       *int                 `json:"total_output_tokens,omitempty"`
	TotalCacheReadTokens    *int                 `json:"total_cache_read_tokens,omitempty"`
	TotalCacheCreateTokens  *int                 `json:"total_cache_creation_tokens,omitempty"`
	RoundCount              *int                 `json:"round_count,omitempty"`
	CompletionReason        *string              `json:"completion_reason,omitempty"`
	VerifierConceded        *bool                `json:"verifier_conceded,omitempty"`
	ServiceURL              *string              `json:"service_url,omitempty"`
	DashboardURL            *string              `json:"dashboard_url,omitempty"`
	CurrentSpecVersion      *int                 `json:"current_spec_version,omitempty"`
	SpecVersions            []map[string]any     `json:"spec_versions"`
	LatestDescendantSession *SessionSummary      `json:"latest_descendant_session,omitempty"`
}

// --------- Response types ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------

// SessionListItem is the product-facing summary returned by GET /api/sessions.
type SessionListItem struct {
	SessionID          string         `json:"session_id"`
	ParentSessionID    *string        `json:"parent_session_id,omitempty"`
	SpecName           *string        `json:"spec_name,omitempty"`
	Status             SessionStatus  `json:"status"`
	CreatedAt          *string        `json:"created_at,omitempty"`
	Runtime            SessionRuntime `json:"runtime"`
	CurrentRound       *int           `json:"current_round,omitempty"`
	CurrentRole        *string        `json:"current_role,omitempty"`
	Result             *string        `json:"result,omitempty"`
	Error              *string        `json:"error,omitempty"`
	TotalCostUSD       *float64       `json:"total_cost_usd,omitempty"`
	ServiceURL         *string        `json:"service_url,omitempty"`
	DashboardURL       *string        `json:"dashboard_url,omitempty"`
	CurrentSpecVersion *int           `json:"current_spec_version,omitempty"`
}

// SessionListResponse wraps GET /api/sessions.
type SessionListResponse struct {
	Sessions []SessionListItem `json:"sessions"`
}

// SessionListItems derives public list summaries from full session records.
func SessionListItems(sessions []Session) []SessionListItem {
	items := make([]SessionListItem, 0, len(sessions))
	for _, session := range sessions {
		items = append(items, SessionListItemFromSession(session))
	}
	return items
}

// SessionListItemFromSession strips debug-heavy fields from a full Session.
func SessionListItemFromSession(session Session) SessionListItem {
	return SessionListItem{
		SessionID:          session.SessionID,
		ParentSessionID:    session.ParentSessionID,
		SpecName:           session.SpecName,
		Status:             session.Status,
		CreatedAt:          session.CreatedAt,
		Runtime:            session.Runtime,
		CurrentRound:       session.CurrentRound,
		CurrentRole:        session.CurrentRole,
		Result:             session.Result,
		Error:              session.Error,
		TotalCostUSD:       session.TotalCostUSD,
		ServiceURL:         session.ServiceURL,
		DashboardURL:       session.DashboardURL,
		CurrentSpecVersion: session.CurrentSpecVersion,
	}
}

// AsSession preserves the existing Go client shape for list callers.
func (item SessionListItem) AsSession() Session {
	return Session{
		SessionID:          item.SessionID,
		ParentSessionID:    item.ParentSessionID,
		SpecName:           item.SpecName,
		Status:             item.Status,
		CreatedAt:          item.CreatedAt,
		Runtime:            item.Runtime,
		CurrentRound:       item.CurrentRound,
		CurrentRole:        item.CurrentRole,
		Result:             item.Result,
		Error:              item.Error,
		TotalCostUSD:       item.TotalCostUSD,
		ServiceURL:         item.ServiceURL,
		DashboardURL:       item.DashboardURL,
		CurrentSpecVersion: item.CurrentSpecVersion,
	}
}

// SessionsFromListItems converts list summaries to the legacy Go client shape.
func SessionsFromListItems(items []SessionListItem) []Session {
	sessions := make([]Session, 0, len(items))
	for _, item := range items {
		sessions = append(sessions, item.AsSession())
	}
	return sessions
}

// SessionEvent represents one evidence event from a session.
type SessionEvent struct {
	Event       string         `json:"event"`
	SessionID   *string        `json:"session_id,omitempty"`
	SpecIndex   *int           `json:"spec_index,omitempty"`
	SpecName    *string        `json:"spec_name,omitempty"`
	SpecDirName *string        `json:"spec_dir_name,omitempty"`
	Round       *int           `json:"round,omitempty"`
	Role        *string        `json:"role,omitempty"`
	Data        map[string]any `json:"data,omitempty"`
}

// SessionEventsResponse wraps GET /api/sessions/{id}/events.
type SessionEventsResponse struct {
	Events []SessionEvent `json:"events"`
}

// SessionDiagnosticsResponse is the production inspection payload for one
// session. It consolidates manifest state, evidence events, and native
// per-turn session logs into a single operator-facing document.
type SessionDiagnosticsResponse struct {
	SessionID        string                                     `json:"session_id"`
	Status           SessionStatus                              `json:"status"`
	Runtime          SessionRuntime                             `json:"runtime"`
	SessionKind      *SessionKind                               `json:"session_kind,omitempty"`
	ParentSessionID  *string                                    `json:"parent_session_id,omitempty"`
	Result           *string                                    `json:"result,omitempty"`
	CompletionReason *string                                    `json:"completion_reason,omitempty"`
	Error            *string                                    `json:"error,omitempty"`
	Config           map[string]any                             `json:"config,omitempty"`
	Limits           SessionBudgetDiagnostics                   `json:"limits"`
	Totals           SessionDiagnosticsTotals                   `json:"totals"`
	Failures         map[string]int                             `json:"failures"`
	BudgetExceeded   map[string]int                             `json:"budget_exceeded"`
	StopReasons      map[string]int                             `json:"stop_reasons"`
	SessionLogEvents map[string]int                             `json:"session_log_events,omitempty"`
	Retries          []SessionRetryDiagnostics                  `json:"retries"`
	Errors           []SessionErrorDiagnostics                  `json:"errors"`
	OutsideWorkspace []SessionOutsideWorkspaceAccessDiagnostics `json:"outside_workspace_access,omitempty"`
	Artifacts        []SessionArtifactDiagnostics               `json:"artifacts"`
	Specs            []SessionSpecDiagnostics                   `json:"specs"`
	ScanErrors       []string                                   `json:"scan_errors,omitempty"`
}

type SessionBudgetDiagnostics struct {
	MaxCostUSD      *float64 `json:"max_cost_usd,omitempty"`
	MaxRounds       int      `json:"max_rounds,omitempty"`
	MaxDurationSec  int      `json:"max_duration_sec,omitempty"`
	MaxInputTokens  int      `json:"max_input_tokens,omitempty"`
	MaxOutputTokens int      `json:"max_output_tokens,omitempty"`
	MaxToolLoops    int      `json:"max_tool_loops,omitempty"`
	AgentTimeoutSec int      `json:"agent_timeout_sec,omitempty"`
}

type SessionDiagnosticsTotals struct {
	CostUSD          float64 `json:"cost_usd,omitempty"`
	CostUnavailable  bool    `json:"cost_unavailable,omitempty"`
	InputTokens      int     `json:"input_tokens,omitempty"`
	OutputTokens     int     `json:"output_tokens,omitempty"`
	CacheReadTokens  int     `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int     `json:"cache_write_tokens,omitempty"`
	Rounds           int     `json:"rounds,omitempty"`
}

type SessionArtifactDiagnostics struct {
	SpecName              string `json:"spec_name,omitempty"`
	SpecDirName           string `json:"spec_dir_name,omitempty"`
	EvidencePath          string `json:"evidence_path,omitempty"`
	EvidenceExists        bool   `json:"evidence_exists"`
	TranscriptPath        string `json:"transcript_path,omitempty"`
	TranscriptExists      bool   `json:"transcript_exists"`
	ObjectiveLedgerPath   string `json:"objective_ledger_path,omitempty"`
	ObjectiveLedgerExists bool   `json:"objective_ledger_exists"`
	WorkspacePath         string `json:"workspace_path,omitempty"`
	WorkspaceExists       bool   `json:"workspace_exists"`
}

type SessionSpecDiagnostics struct {
	Name             string                   `json:"name,omitempty"`
	DirName          string                   `json:"dir_name,omitempty"`
	Result           string                   `json:"result,omitempty"`
	CompletionReason string                   `json:"completion_reason,omitempty"`
	Totals           SessionDiagnosticsTotals `json:"totals"`
	CurrentRound     *int                     `json:"current_round,omitempty"`
	CurrentRole      *string                  `json:"current_role,omitempty"`
	Failures         map[string]int           `json:"failures,omitempty"`
}

type SessionRetryDiagnostics struct {
	SpecName           string `json:"spec_name,omitempty"`
	TurnID             string `json:"turn_id,omitempty"`
	Sequence           int    `json:"sequence,omitempty"`
	Attempt            int    `json:"attempt,omitempty"`
	DelayMS            int    `json:"delay_ms,omitempty"`
	ErrorCode          string `json:"error_code,omitempty"`
	Error              string `json:"error,omitempty"`
	ProviderStatusCode int    `json:"provider_status_code,omitempty"`
}

type SessionErrorDiagnostics struct {
	SpecName           string `json:"spec_name,omitempty"`
	TurnID             string `json:"turn_id,omitempty"`
	Sequence           int    `json:"sequence,omitempty"`
	ErrorCode          string `json:"error_code,omitempty"`
	Error              string `json:"error,omitempty"`
	Retryable          *bool  `json:"retryable,omitempty"`
	ProviderStatusCode int    `json:"provider_status_code,omitempty"`
}

type SessionOutsideWorkspaceAccessDiagnostics struct {
	SpecName string `json:"spec_name,omitempty"`
	TurnID   string `json:"turn_id,omitempty"`
	Action   string `json:"action,omitempty"`
	Path     string `json:"path,omitempty"`
	Write    bool   `json:"write,omitempty"`
}

// Package sessionapi defines the canonical JSON request/response types and
// HTTP routes for the Telos Sessions API. Both local and cloud deployments
// serve the same contract; they differ by adapters for auth, store, launcher,
// and workspace.
package sessionapi

import "encoding/json"

// --------- Enums ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------

// SessionStatus enumerates the lifecycle states of a session.
type SessionStatus string

const (
	StatusPending   SessionStatus = "pending"
	StatusRunning   SessionStatus = "running"
	StatusScheduled SessionStatus = "scheduled"
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
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	switch raw {
	case "hosted": // Legacy wire value from older cloud APIs.
		*r = RuntimeCloud
	case string(RuntimeLocal), string(RuntimeCloud):
		*r = SessionRuntime(raw)
	default:
		*r = SessionRuntime(raw)
	}
	return nil
}

// SessionKind distinguishes controller sessions from task sessions.
type SessionKind string

const (
	KindController SessionKind = "controller"
	KindTask       SessionKind = "task"
)

// --------- Request types ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------

// SessionCreateRequest is the body of POST /api/sessions.
type SessionCreateRequest struct {
	SpecPath        *string  `json:"spec_path,omitempty"`
	SpecMarkdown    *string  `json:"spec_markdown,omitempty"`
	SpecID          *string  `json:"spec_id,omitempty"`
	ParentSessionID *string  `json:"parent_session_id,omitempty"`
	FromWorkspace   *string  `json:"from_workspace,omitempty"`
	MaxRounds       *int     `json:"max_rounds,omitempty"`
	Model           string   `json:"model,omitempty"`
	Thinking        string   `json:"thinking,omitempty"`
	MaxCostUSD      *float64 `json:"max_cost_usd,omitempty"`
	AgentTimeoutSec *int     `json:"agent_timeout_sec,omitempty"`
	Workspace       *string  `json:"workspace,omitempty"`
}

// --------- Spec types ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------

// SessionSpec describes one compiled spec entry inside a session.
type SessionSpec struct {
	Index                  *int     `json:"index,omitempty"`
	Name                   *string  `json:"name,omitempty"`
	DirName                *string  `json:"dir_name,omitempty"`
	EnvironmentPath        *string  `json:"environment_path,omitempty"`
	SessionSpecPath        *string  `json:"session_spec_path,omitempty"`
	ContentHash            *string  `json:"content_hash,omitempty"`
	EvidencePath           *string  `json:"evidence_path,omitempty"`
	EvidenceExists         *bool    `json:"evidence_exists,omitempty"`
	TranscriptPath         *string  `json:"transcript_path,omitempty"`
	TranscriptExists       *bool    `json:"transcript_exists,omitempty"`
	WorkspacePath          *string  `json:"workspace_path,omitempty"`
	WorkspaceExists        *bool    `json:"workspace_exists,omitempty"`
	IntervalSeconds        *int     `json:"interval_seconds,omitempty"`
	TotalCostUSD           *float64 `json:"total_cost_usd,omitempty"`
	TotalInputTokens       *int     `json:"total_input_tokens,omitempty"`
	TotalOutputTokens      *int     `json:"total_output_tokens,omitempty"`
	TotalCacheReadTokens   *int     `json:"total_cache_read_tokens,omitempty"`
	TotalCacheCreateTokens *int     `json:"total_cache_creation_tokens,omitempty"`
	RoundCount             *int     `json:"round_count,omitempty"`
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
	SessionKind     *SessionKind  `json:"session_kind,omitempty"`
	ParentSessionID *string       `json:"parent_session_id,omitempty"`
	SpecName        *string       `json:"spec_name,omitempty"`
	Status          SessionStatus `json:"status"`
	CreatedAt       *string       `json:"created_at,omitempty"`
}

// Session is the full API representation returned by get/list/create/stop.
type Session struct {
	SessionID       string        `json:"session_id"`
	SessionKind     *SessionKind  `json:"session_kind,omitempty"`
	ParentSessionID *string       `json:"parent_session_id,omitempty"`
	SpecName        *string       `json:"spec_name,omitempty"`
	Status          SessionStatus `json:"status"`
	CreatedAt       *string       `json:"created_at,omitempty"`

	Runtime                 SessionRuntime   `json:"runtime"`
	Launcher                *string          `json:"launcher,omitempty"`
	SourceSpecPath          *string          `json:"source_spec_path,omitempty"`
	SessionSpecPath         *string          `json:"session_spec_path,omitempty"`
	SessionDir              *string          `json:"session_dir,omitempty"`
	Config                  map[string]any   `json:"config"`
	Provenance              map[string]any   `json:"provenance"`
	Specs                   []SessionSpec    `json:"specs"`
	Epochs                  []map[string]any `json:"epochs"`
	CurrentEpoch            *int             `json:"current_epoch,omitempty"`
	CurrentSpec             *CurrentSpec     `json:"current_spec,omitempty"`
	CurrentRound            *int             `json:"current_round,omitempty"`
	CurrentRole             *string          `json:"current_role,omitempty"`
	HeartbeatAt             *string          `json:"heartbeat_at,omitempty"`
	NextRunAt               *string          `json:"next_run_at,omitempty"`
	FinishedAt              *string          `json:"finished_at,omitempty"`
	Result                  *string          `json:"result,omitempty"`
	Error                   *string          `json:"error,omitempty"`
	TotalCostUSD            *float64         `json:"total_cost_usd,omitempty"`
	TotalInputTokens        *int             `json:"total_input_tokens,omitempty"`
	TotalOutputTokens       *int             `json:"total_output_tokens,omitempty"`
	TotalCacheReadTokens    *int             `json:"total_cache_read_tokens,omitempty"`
	TotalCacheCreateTokens  *int             `json:"total_cache_creation_tokens,omitempty"`
	RoundCount              *int             `json:"round_count,omitempty"`
	ArtifactURI             *string          `json:"artifact_uri,omitempty"`
	CurrentSpecVersion      *int             `json:"current_spec_version,omitempty"`
	SpecVersions            []map[string]any `json:"spec_versions"`
	LatestDescendantSession *SessionSummary  `json:"latest_descendant_session,omitempty"`
}

// --------- Response types ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------

// SessionListResponse wraps GET /api/sessions.
type SessionListResponse struct {
	Sessions []Session `json:"sessions"`
}

// SessionEvent represents one evidence event from a session.
type SessionEvent struct {
	Event       string         `json:"event"`
	SessionID   *string        `json:"session_id,omitempty"`
	SpecIndex   *int           `json:"spec_index,omitempty"`
	SpecName    *string        `json:"spec_name,omitempty"`
	SpecDirName *string        `json:"spec_dir_name,omitempty"`
	Data        map[string]any `json:"data,omitempty"`
}

// SessionEventsResponse wraps GET /api/sessions/{id}/events.
type SessionEventsResponse struct {
	Events []SessionEvent `json:"events"`
}

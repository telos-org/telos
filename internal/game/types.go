// Package game implements the PVG (Prover-Verifier Game) loop.
package game

import (
	"path/filepath"
	"regexp"
	"strings"
)

// GameResult is the terminal outcome of a PVG run.
type GameResult string

const (
	GameSuccess GameResult = "success"
	GameFailure GameResult = "failure"
	GameStopped GameResult = "stopped"
)

// AgentStatus is the normalized status from an agent turn.
type AgentStatus string

const (
	StatusContinue AgentStatus = "CONTINUE"
	StatusConcede  AgentStatus = "CONCEDE"
)

// TurnStats holds token and cost data from a single agent turn.
type TurnStats struct {
	CostUSD             float64 `json:"cost_usd"`
	DurationMS          int     `json:"duration_ms"`
	NumTurns            int     `json:"num_turns"`
	InputTokens         int     `json:"input_tokens"`
	OutputTokens        int     `json:"output_tokens"`
	CacheReadTokens     int     `json:"cache_read_tokens"`
	CacheCreationTokens int     `json:"cache_creation_tokens"`
	Model               string  `json:"model"`
}

// TurnResult is the result of one agent turn.
type TurnResult struct {
	Role        string
	Status      AgentStatus
	Logs        string
	Stats       TurnStats
	Error       string
	Recoverable bool
}

// LiveAgentEvent is a user-visible progress artifact emitted before a turn exits.
type LiveAgentEvent struct {
	Kind string
	Text string
}

// AgentExecutor runs one PVG agent turn.
type AgentExecutor interface {
	ExecuteTurn(task string, role string, turnState *TurnState) TurnResult
	WorkspaceState() string
	CheckpointWorkspace(dest string) bool
}

// PVGConfig holds runtime settings for a PVG run.
type PVGConfig struct {
	Until           int
	UntilSeconds    int
	MaxCostUSD      *float64
	Verbose         bool
	EpochID         int
	IsController    bool
	PrimarySpecPath string
	StopRequested   func() bool
}

// PVGResult holds the aggregated result of a PVG run.
type PVGResult struct {
	SystemName              string
	GameResult              GameResult
	Rounds                  int
	ProverRounds            int
	VerifierRounds          int
	VerifierConceded        bool
	CompletionReason        string
	TotalCostUSD            float64
	TotalInputTokens        int
	TotalOutputTokens       int
	TotalCacheReadTokens    int
	TotalCacheCreateTokens  int
	Error                   string
	EvidencePath            string
	TranscriptPath          string
	WorkspaceCheckpointPath string
}

// Accumulate adds turn stats to the result totals.
func (r *PVGResult) Accumulate(s TurnStats) {
	r.TotalCostUSD += s.CostUSD
	r.TotalInputTokens += s.InputTokens
	r.TotalOutputTokens += s.OutputTokens
	r.TotalCacheReadTokens += s.CacheReadTokens
	r.TotalCacheCreateTokens += s.CacheCreationTokens
}

// -- Turn state --------------------------------------------------------------

// TurnState holds filesystem paths for one PVG turn.
type TurnState struct {
	RoundNum      int
	Role          string
	Dir           string
	StopRequested func() bool
	OnLiveEvent   func(LiveAgentEvent)
}

// TurnID returns the canonical turn identifier.
func (ts *TurnState) TurnID() string {
	return ts.Role
}

// TaskPath returns the path to the task.md file.
func (ts *TurnState) TaskPath() string {
	return filepath.Join(ts.Dir, "task.md")
}

// PiSessionPath returns the path to Pi's compact session JSONL file.
func (ts *TurnState) PiSessionPath() string {
	return filepath.Join(ts.Dir, "pi-session.jsonl")
}

// -- Status extraction -------------------------------------------------------

var statusRE = regexp.MustCompile(`(?:^|\n)\s*<status>(\w+)</status>\s*$`)

// ExtractStatus parses the final status tag from agent output.
func ExtractStatus(text string) AgentStatus {
	matches := statusRE.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return StatusContinue
	}
	last := matches[len(matches)-1][1]
	switch strings.ToUpper(last) {
	case "CONCEDE":
		return StatusConcede
	default:
		return StatusContinue
	}
}

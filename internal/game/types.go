// Package game implements the PVG (Prover-Verifier Game) loop.
package game

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/telos-org/telos/internal/platform"
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
	CostUnavailable     bool    `json:"cost_unavailable,omitempty"`
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

// AgentExecutor runs one PVG agent turn. The role is carried on turnState.Role
// rather than passed separately, so the two can never disagree.
type AgentExecutor interface {
	ExecuteTurn(task string, turnState *TurnState) TurnResult
	WorkspaceSnapshot() platform.WorkspaceSnapshot
	CheckpointWorkspace(dest string) bool
}

// PVGConfig holds runtime settings for a PVG run.
type PVGConfig struct {
	Until           int
	MaxCostUSD      *float64
	CostHardLimit   bool
	MaxRounds       int
	MaxDurationSec  int
	MaxInputTokens  int
	MaxOutputTokens int
	MaxToolLoops    int
	AgentTimeoutSec int
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
	CostUnavailable         bool
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
	r.CostUnavailable = r.CostUnavailable || s.CostUnavailable
}

// -- Turn state --------------------------------------------------------------

// TurnState holds filesystem paths for one PVG turn.
type TurnState struct {
	EpochID       int
	RoundNum      int
	Role          string
	Dir           string
	StopRequested func() bool
	Budget        TurnBudget
	ProtocolMode  string
	Skills        []TurnSkill
}

// TurnSkill is one skill made available to the executor for this turn. It
// mirrors the roster rendered into the prompt, but is passed structurally so
// the executor's skill tool does not have to re-parse skill names, paths, and
// required-rubric flags out of the rendered prompt text.
type TurnSkill struct {
	Name        string
	Description string
	// SkillPath is the path to the skill's SKILL.md file.
	SkillPath string
	Required  bool
}

// TurnBudget carries the remaining runtime budget available to one executor
// turn. Executors should check it before each provider request and stop
// recoverably if an in-turn tool/model loop has already exhausted it.
type TurnBudget struct {
	MaxCostUSD            *float64
	RemainingCostUSD      *float64
	CostHardLimit         bool
	MaxDurationSec        int
	RemainingDurationSec  int
	AgentTimeoutSec       int
	MaxInputTokens        int
	RemainingInputTokens  int
	MaxOutputTokens       int
	RemainingOutputTokens int
	MaxToolLoops          int
}

// TurnID returns the canonical turn identifier.
func (ts *TurnState) TurnID() string {
	return ts.Role
}

// TaskPath returns the path to the task.md file.
func (ts *TurnState) TaskPath() string {
	return filepath.Join(ts.Dir, "task.md")
}

// SessionPath returns the path to the compact agent session JSONL file.
func (ts *TurnState) SessionPath() string {
	return filepath.Join(ts.Dir, "session.jsonl")
}

// -- Status extraction -------------------------------------------------------

var statusRE = regexp.MustCompile(`(?:^|\n)\s*<status>(\w+)</status>\s*$`)

// ExtractStatus parses the final status tag from agent output.
func ExtractStatus(text string) AgentStatus {
	status, ok := ParseFinalStatus(text)
	if !ok {
		return StatusContinue
	}
	return status
}

// ParseFinalStatus parses a valid final status tag from agent output.
func ParseFinalStatus(text string) (AgentStatus, bool) {
	matches := statusRE.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return StatusContinue, false
	}
	last := matches[len(matches)-1][1]
	switch strings.ToUpper(last) {
	case "CONCEDE":
		return StatusConcede, true
	case "CONTINUE":
		return StatusContinue, true
	default:
		return StatusContinue, false
	}
}

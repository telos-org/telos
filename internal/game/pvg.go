package game

import (
	"crypto/rand"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/telos-org/telos/internal/evidence"
	"github.com/telos-org/telos/internal/spec"
)

const maxRecoverableAgentFailures = 3

// PVG runs the prover-verifier loop for one compiled environment.
type PVG struct {
	Compiled *spec.CompiledEnvironment
	Executor AgentExecutor
	State    *PVGState
	Config   PVGConfig
	Result   *PVGResult
	Evidence *evidence.Evidence

	checkpoint *runCheckpoint
}

// NewPVG creates a new PVG game instance.
func NewPVG(compiled *spec.CompiledEnvironment, executor AgentExecutor, state *PVGState, config PVGConfig) *PVG {
	ev := evidence.New(compiled.Environment.Name, state.EvidencePath, state.SessionID, config.EpochID)
	result := &PVGResult{SystemName: compiled.Environment.Name}
	result.EvidencePath = ev.Path
	result.TranscriptPath = state.TranscriptPath

	return &PVG{
		Compiled: compiled,
		Executor: executor,
		State:    state,
		Config:   config,
		Result:   result,
		Evidence: ev,
	}
}

// Run executes or resumes one epoch. The caller must hold session ownership.
// Persistence errors leave the epoch open; callers must not turn them into a
// terminal game failure, which would permit a new epoch with fresh budgets.
func (p *PVG) Run() (*PVGResult, error) {
	defer p.Evidence.Close()
	if err := p.restoreCheckpoint(); err != nil {
		return nil, fmt.Errorf("restore execution checkpoint: %w", err)
	}
	alreadyTerminal := p.Result.GameResult != ""
	if !alreadyTerminal {
		if err := InitializeTranscript(p.State.TranscriptPath, p.State.SessionID,
			p.Compiled.Environment.Name, p.State.EvidencePath, p.Evidence.StartedAt); err != nil {
			return nil, err
		}
		if p.Config.Verbose {
			log.Printf("=== PVG RUN: %s, next round %d ===", p.Compiled.Environment.Name, p.Result.Rounds+1)
		}
		if err := p.runDefaultLoop(); err != nil {
			return nil, fmt.Errorf("persist execution progress: %w", err)
		}
	}
	// Aggregate snapshots can be repeated after a crash without adding usage
	// twice. A saved terminal decision must still reach evidence readers.
	p.logGameState("game_end")
	marker := fmt.Sprintf("<!-- telos result %s -->", p.checkpoint.ResultID)
	if found, err := transcriptHasMarker(p.State.TranscriptPath, p.checkpoint.TranscriptOffset, marker); err != nil {
		return nil, err
	} else if !found {
		if err := appendGameResult(p.State.TranscriptPath, string(p.Result.GameResult), p.Result.Error, marker); err != nil {
			return nil, err
		}
	}
	if p.Result.WorkspaceCheckpointPath == "" {
		p.checkpointWorkspace()
		if err := p.saveCheckpoint(); err != nil {
			return nil, fmt.Errorf("persist workspace checkpoint: %w", err)
		}
	}
	return p.Result, nil
}

func (p *PVG) runDefaultLoop() error {
	for p.Result.GameResult == "" {
		if p.checkpoint.Pending == nil {
			if p.runDurationExhausted(p.checkpoint.Deadline) {
				p.failRunDurationExhausted()
				return p.saveCheckpoint()
			}
			if p.shouldStop(p.checkpoint.Deadline) {
				p.end(GameStopped)
				return p.saveCheckpoint()
			}
			round := p.Result.Rounds + 1
			role := turnRole(round)
			workspace := p.Executor.WorkspaceState()
			promptOpts := p.promptOptions()
			var task string
			if role == "prover" {
				task = spec.RenderProverTask(p.Compiled, workspace, p.State.TranscriptPath, promptOpts)
			} else {
				task = spec.RenderVerifierTask(p.Compiled, workspace, p.State.TranscriptPath, promptOpts)
			}
			info, err := os.Stat(p.State.TranscriptPath)
			if err != nil {
				return err
			}
			turn, err := p.runAgentTurn(round, role, (round+1)/2, task, p.checkpoint.Deadline)
			if err != nil {
				return err
			}
			turn.Role = role
			if turn.Status != StatusConcede {
				turn.Status = StatusContinue
			}
			finishedAt := time.Now()
			p.checkpoint.Pending = &completedTurn{
				Turn: turn, FinishedAt: finishedAt, TranscriptOffset: info.Size(),
				Stopped: p.Config.StopRequested != nil && p.Config.StopRequested(),
				// Generated after the agent exits: its earlier output cannot be
				// mistaken for the coordinator's completed handoff marker.
				CompletionID: rand.Text(),
			}
			if err := p.saveCheckpoint(); err != nil {
				return err
			}
		}
		if stateError := p.checkpoint.Pending.Turn.StateError; stateError != "" {
			return fmt.Errorf("agent session integrity: %s", stateError)
		}
		if err := p.appendPendingTurn(); err != nil {
			return err
		}
		turn := p.checkpoint.Pending.Turn
		p.acceptPendingTurn()
		p.checkpoint.Pending = nil
		if err := p.saveCheckpoint(); err != nil {
			return err
		}
		// Activity is a best-effort projection, never the authority for resuming.
		p.Evidence.LogAgent(p.Result.Rounds, turn.Role, string(turn.Status), turn.Logs, &turn.Stats)
	}
	return nil
}

func (p *PVG) acceptPendingTurn() {
	pending := p.checkpoint.Pending
	turn := pending.Turn
	p.checkpoint.TranscriptOffset = pending.TranscriptOffset
	p.Result.Rounds++
	if turn.Role == "prover" {
		p.Result.ProverRounds++
	} else {
		p.Result.VerifierRounds++
	}
	p.Result.Accumulate(turn.Stats)
	if !p.checkpoint.Deadline.IsZero() && pending.FinishedAt.After(p.checkpoint.Deadline) {
		p.failRunDurationExhausted()
		return
	}
	if pending.Stopped {
		p.end(GameStopped)
		return
	}
	if turn.Error != "" {
		if p.turnFailureExceeded(turn, &p.checkpoint.RecoverableFailures) {
			p.end(GameFailure)
			return
		}
	} else {
		p.checkpoint.RecoverableFailures = 0
	}
	if turn.Role == "verifier" {
		if turn.Error == "" && turn.Status == StatusConcede {
			p.Result.VerifierConceded = true
			p.end(GameSuccess)
			return
		}
		if turn.Error == "" {
			p.checkpoint.ReviewCycles++
		}
		if p.reviewBudgetExhausted(p.checkpoint.ReviewCycles) {
			p.Result.Error = fmt.Sprintf("review_budget_exhausted: verifier did not concede within %d review cycles", p.Config.Until)
			p.Result.CompletionReason = "review_budget_exhausted"
			p.end(GameFailure)
			return
		}
	}
	if p.overBudget(p.Result.Rounds, turn.Role) {
		p.end(GameFailure)
	}
}

func (p *PVG) failRunDurationExhausted() *PVGResult {
	p.Result.Error = fmt.Sprintf("run_duration_exhausted: exceeded %d seconds", p.Config.UntilSeconds)
	p.Result.CompletionReason = "run_duration_exhausted"
	return p.end(GameFailure)
}

func (p *PVG) runDeadline() time.Time {
	if p.Config.UntilSeconds <= 0 {
		return time.Time{}
	}
	return time.Now().Add(time.Duration(p.Config.UntilSeconds) * time.Second)
}

func (p *PVG) runDurationExhausted(deadline time.Time) bool {
	return !deadline.IsZero() && time.Now().After(deadline)
}

func (p *PVG) turnFailureExceeded(turn TurnResult, consecutive *int) bool {
	if _, blocked := AgentFailureBlocker(turn.Error); blocked || !turn.Recoverable {
		p.Result.Error = turn.Error
		return true
	}
	*consecutive++
	p.Evidence.Log("agent_failure_recoverable", p.Result.Rounds, turn.Role, map[string]interface{}{
		"error":                turn.Error,
		"consecutive_failures": *consecutive,
		"max_failures":         maxRecoverableAgentFailures,
	})
	if *consecutive <= maxRecoverableAgentFailures {
		return false
	}
	p.Result.Error = fmt.Sprintf("agent_failure_budget_exceeded: %s", turn.Error)
	return true
}

func (p *PVG) reviewBudgetMode() bool {
	return p.Config.Until > 0
}

func (p *PVG) shouldStop(deadline time.Time) bool {
	if p.runDurationExhausted(deadline) {
		return true
	}
	return p.Config.StopRequested != nil && p.Config.StopRequested()
}

func (p *PVG) promptOptions() spec.PromptOptions {
	return spec.PromptOptions{
		Controller:      p.Config.IsController,
		PrimarySpecPath: p.Config.PrimarySpecPath,
		ReviewBudget:    p.reviewBudgetMode(),
		ReviewCycleCap:  p.Config.Until,
		LocalRuntime:    p.Config.LocalRuntime,
	}
}

func (p *PVG) runAgentTurn(roundNum int, role string, roleRound int, task string, deadline time.Time) (TurnResult, error) {
	ts := p.State.Turn(p.Config.EpochID, roundNum, role)
	ts.StopRequested = func() bool {
		return p.shouldStop(deadline)
	}
	turnID := fmt.Sprintf("%04d-%s", roundNum, role)
	ts.OnLiveEvent = func(event LiveAgentEvent) {
		if err := AppendLiveAgentEvent(p.State.TranscriptPath, role, roleRound, turnID, event); err != nil {
			return
		}
		audience := "agent"
		if event.Kind == "progress_update" {
			audience = "user"
		}
		p.Evidence.Log("agent_progress", roundNum, role, map[string]interface{}{
			"audience": audience,
			"kind":     event.Kind,
			"text":     event.Text,
			"turn_id":  turnID,
		})
	}
	if err := WriteTurnTask(ts, task); err != nil {
		return TurnResult{}, fmt.Errorf("prepare turn %d: %w", roundNum, err)
	}
	p.Evidence.Log("round_start", roundNum, role, map[string]interface{}{"audience": "user"})
	return p.Executor.ExecuteTurn(task, role, ts), nil
}

func (p *PVG) logGameState(event string) {
	p.Evidence.LogGameState(event, string(p.Result.GameResult), p.Result.Rounds,
		p.Result.ProverRounds, p.Result.VerifierRounds,
		p.Result.VerifierConceded, p.Result.TotalCostUSD,
		p.Result.TotalInputTokens, p.Result.TotalOutputTokens,
		p.Result.TotalCacheReadTokens, p.Result.TotalCacheCreateTokens,
		p.Result.Error, p.Result.CompletionReason)
}

func (p *PVG) end(result GameResult) *PVGResult {
	p.Result.GameResult = result
	p.checkpoint.ResultID = rand.Text()
	if p.Result.CompletionReason == "" {
		p.Result.CompletionReason = p.completionReason(result)
	}
	return p.Result
}

func (p *PVG) completionReason(result GameResult) string {
	switch result {
	case GameSuccess:
		if p.Result.VerifierConceded {
			return "verifier_conceded"
		}
		return "success"
	case GameStopped:
		return "stopped"
	default:
		return "failure"
	}
}

func (p *PVG) reviewBudgetExhausted(completed int) bool {
	return p.Config.Until > 0 && completed >= p.Config.Until && !p.Result.VerifierConceded
}

func (p *PVG) checkpointWorkspace() {
	dest := p.State.WorkspacePath
	if p.Executor.CheckpointWorkspace(dest) {
		p.Result.WorkspaceCheckpointPath = dest
		p.Evidence.LogWorkspaceCheckpoint(p.Result.Rounds, dest)
	}
}

func (p *PVG) overBudget(roundNum int, role string) bool {
	if p.Config.MaxCostUSD == nil {
		return false
	}
	cap := *p.Config.MaxCostUSD
	if p.Result.TotalCostUSD < cap {
		return false
	}
	p.Evidence.Log("budget_exceeded", roundNum, role, map[string]interface{}{
		"session_cost_usd": p.Result.TotalCostUSD,
		"cap_usd":          cap,
	})
	p.Result.Error = fmt.Sprintf("budget exceeded: $%.2f >= cap $%.2f",
		p.Result.TotalCostUSD, cap)
	return true
}

package game

import (
	"fmt"
	"log"
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
}

// NewPVG creates a new PVG game instance.
func NewPVG(compiled *spec.CompiledEnvironment, executor AgentExecutor, state *PVGState, config PVGConfig) *PVG {
	ev := evidence.New(compiled.Environment.Name, state.EvidencePath, state.SessionID, config.EpochID)
	result := &PVGResult{SystemName: compiled.Environment.Name}
	result.EvidencePath = ev.Path
	result.TranscriptPath = state.TranscriptPath

	InitializeTranscript(state.TranscriptPath, state.SessionID,
		compiled.Environment.Name, ev.Path, ev.StartedAt)

	return &PVG{
		Compiled: compiled,
		Executor: executor,
		State:    state,
		Config:   config,
		Result:   result,
		Evidence: ev,
	}
}

// Run executes the full PVG loop.
func (p *PVG) Run() *PVGResult {
	p.Evidence.Log("game_start", 0, "system", nil)
	defer func() {
		p.checkpointWorkspace()
		p.Evidence.Close()
	}()

	result, err := p.runLoop()
	if err != nil {
		p.Result.Error = err.Error()
		p.Evidence.Log("game_error", p.Result.Rounds, "system", map[string]interface{}{
			"error": p.Result.Error,
			"type":  "PVGError",
		})
		return p.end(GameFailure)
	}
	return result
}

func (p *PVG) runLoop() (*PVGResult, error) {
	if p.Config.Verbose {
		log.Printf("=== PVG START: %s ===", p.Compiled.Environment.Name)
	}

	return p.runDefaultLoop(), nil
}

func (p *PVG) runDefaultLoop() *PVGResult {
	promptOpts := p.promptOptions()
	workspace := ""
	recoverableFailures := 0
	reviewCyclesCompleted := 0
	deadline := p.runDeadline()

	for {
		if p.runDurationExhausted(deadline) {
			return p.failRunDurationExhausted()
		}
		if p.shouldStop(deadline) {
			return p.end(GameStopped)
		}
		turn := p.runProverTurn(workspace, promptOpts, deadline)
		if p.runDurationExhausted(deadline) {
			return p.failRunDurationExhausted()
		}
		if p.shouldStop(deadline) {
			return p.end(GameStopped)
		}
		if turn.Error != "" {
			if p.turnFailureExceeded(turn, &recoverableFailures) {
				return p.end(GameFailure)
			}
		} else {
			recoverableFailures = 0
		}
		if p.overBudget(p.Result.Rounds, "prover") {
			return p.end(GameFailure)
		}

		workspace = p.Executor.WorkspaceState()
		if p.runDurationExhausted(deadline) {
			return p.failRunDurationExhausted()
		}
		if p.shouldStop(deadline) {
			return p.end(GameStopped)
		}
		turn = p.runVerifierTurn(workspace, promptOpts, deadline)
		if p.runDurationExhausted(deadline) {
			return p.failRunDurationExhausted()
		}
		if p.shouldStop(deadline) {
			return p.end(GameStopped)
		}
		if turn.Error != "" {
			if p.turnFailureExceeded(turn, &recoverableFailures) {
				return p.end(GameFailure)
			}
		} else {
			recoverableFailures = 0
		}
		if turn.Error == "" && turn.Status == StatusConcede {
			p.Result.VerifierConceded = true
			if p.Config.Verbose {
				log.Printf("=== PVG SUCCESS: verifier conceded ===")
			}
			return p.end(GameSuccess)
		}
		if turn.Error == "" {
			reviewCyclesCompleted++
		}
		if p.reviewBudgetExhausted(reviewCyclesCompleted) {
			p.Result.Error = fmt.Sprintf("review_budget_exhausted: verifier did not concede within %d review cycles", p.Config.Until)
			p.Result.CompletionReason = "review_budget_exhausted"
			return p.end(GameFailure)
		}
		if p.overBudget(p.Result.Rounds, "verifier") {
			return p.end(GameFailure)
		}
		workspace = p.Executor.WorkspaceState()
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

func (p *PVG) runProverTurn(workspace string, promptOpts spec.PromptOptions, deadline time.Time) TurnResult {
	p.Result.Rounds++
	p.Result.ProverRounds++
	roundNum := p.Result.Rounds
	p.Evidence.Log("round_start", roundNum, "prover", nil)

	task := spec.RenderProverTask(p.Compiled, workspace, p.State.TranscriptPath, promptOpts)
	return p.runAgentTurn(roundNum, "prover", p.Result.ProverRounds, task, deadline)
}

func (p *PVG) runVerifierTurn(workspace string, promptOpts spec.PromptOptions, deadline time.Time) TurnResult {
	p.Result.Rounds++
	p.Result.VerifierRounds++
	roundNum := p.Result.Rounds
	p.Evidence.Log("round_start", roundNum, "verifier", nil)

	task := spec.RenderVerifierTask(p.Compiled, workspace, p.State.TranscriptPath, promptOpts)
	return p.runAgentTurn(roundNum, "verifier", p.Result.VerifierRounds, task, deadline)
}

func (p *PVG) turnFailureExceeded(turn TurnResult, consecutive *int) bool {
	if !turn.Recoverable {
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
	}
}

func (p *PVG) runAgentTurn(roundNum int, role string, roleRound int, task string, deadline time.Time) TurnResult {
	ts := p.State.Turn(p.Config.EpochID, roundNum, role)
	ts.StopRequested = func() bool {
		return p.shouldStop(deadline)
	}
	turnID := fmt.Sprintf("%04d-%s", roundNum, role)
	ts.OnLiveEvent = func(event LiveAgentEvent) {
		if err := AppendLiveAgentEvent(p.State.TranscriptPath, role, roleRound, turnID, event); err != nil {
			return
		}
		p.Evidence.Log("agent_progress", roundNum, role, map[string]interface{}{
			"kind":    event.Kind,
			"text":    event.Text,
			"turn_id": turnID,
		})
	}
	if err := WriteTurnTask(ts, task); err != nil {
		turn := TurnResult{
			Role:   role,
			Status: StatusContinue,
			Logs:   fmt.Sprintf("turn_prepare_failed:%v", err),
			Error:  fmt.Sprintf("turn_prepare_failed:%v", err),
		}
		p.Evidence.LogAgent(roundNum, role, string(turn.Status), turn.Logs, &turn.Stats)
		AppendTurnWithOptions(p.State.TranscriptPath, role, roleRound, string(turn.Status),
			turn.Logs, &turn.Stats, turnID, turn.Error,
			AppendTurnOptions{
				IncludeStatus: true,
				PiSessionPath: ts.PiSessionPath(),
				EvidencePath:  p.State.EvidencePath,
			})
		return turn
	}

	turn := p.Executor.ExecuteTurn(task, role, ts)
	p.Result.Accumulate(turn.Stats)
	p.Evidence.LogAgent(roundNum, role, string(turn.Status), turn.Logs, &turn.Stats)

	AppendTurnWithOptions(p.State.TranscriptPath, role, roleRound, string(turn.Status),
		turn.Logs, &turn.Stats, turnID, turn.Error,
		AppendTurnOptions{
			IncludeStatus: true,
			PiSessionPath: ts.PiSessionPath(),
			EvidencePath:  p.State.EvidencePath,
		})

	return turn
}

func (p *PVG) end(result GameResult) *PVGResult {
	p.Result.GameResult = result
	if p.Result.CompletionReason == "" {
		p.Result.CompletionReason = p.completionReason(result)
	}
	errMsg := ""
	if p.Result.Error != "" {
		errMsg = p.Result.Error
	}
	p.Evidence.LogGameEnd(string(result), p.Result.Rounds,
		p.Result.ProverRounds, p.Result.VerifierRounds,
		p.Result.VerifierConceded, p.Result.TotalCostUSD,
		p.Result.TotalInputTokens, p.Result.TotalOutputTokens,
		p.Result.TotalCacheReadTokens, p.Result.TotalCacheCreateTokens,
		errMsg, p.Result.CompletionReason)

	AppendGameResult(p.State.TranscriptPath, string(result), errMsg)
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

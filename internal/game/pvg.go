package game

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/telos-org/telos/internal/evidence"
	"github.com/telos-org/telos/internal/spec"
)

const (
	maxRecoverableAgentFailures = 3
	DefaultMaxRounds            = 100
	DefaultMaxDurationSec       = 6 * 60 * 60
)

// PVG runs the prover-verifier loop for one compiled environment.
type PVG struct {
	Compiled *spec.CompiledEnvironment
	Executor AgentExecutor
	State    *PVGState
	Config   PVGConfig
	Result   *PVGResult
	Evidence *evidence.Evidence
	started  time.Time
}

// NewPVG creates a new PVG game instance.
func NewPVG(compiled *spec.CompiledEnvironment, executor AgentExecutor, state *PVGState, config PVGConfig) *PVG {
	ev := evidence.New(compiled.Environment.Name, state.EvidencePath, state.SessionID, config.EpochID)
	result := &PVGResult{SystemName: compiled.Environment.Name}
	result.EvidencePath = ev.Path
	result.TranscriptPath = state.TranscriptPath

	InitializeTranscript(state.TranscriptPath, state.SessionID,
		compiled.Environment.Name, ev.Path, ev.StartedAt)
	_ = writeObjectiveLedger(state.LedgerPath, newObjectiveLedger(state, compiled.SpecText))

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
	p.started = time.Now()
	p.Evidence.Log("game_start", 0, "system", nil)
	defer func() {
		p.checkpointWorkspace()
		p.Evidence.Close()
	}()

	result, err := p.runLoop()
	if err != nil {
		p.Result.Error = err.Error()
		data := map[string]interface{}{
			"error": p.Result.Error,
			"type":  "PVGError",
		}
		if code := turnErrorCode(p.Result.Error); code != "" {
			data["error_code"] = code
		}
		p.Evidence.Log("game_error", p.Result.Rounds, "system", data)
		return p.end(GameFailure)
	}
	return result
}

func (p *PVG) runLoop() (*PVGResult, error) {
	if p.Config.Verbose {
		log.Printf("=== PVG START: %s ===", p.Compiled.Environment.Name)
	}

	return p.runTaskStateMachineLoop(), nil
}

func (p *PVG) runTaskStateMachineLoop() *PVGResult {
	promptOpts := p.promptOptions()
	workspace := ""
	recoverableFailures := 0
	machine := newTaskStateMachine(p.Config.Until)

	for {
		step, ok := machine.next()
		if !ok {
			p.Result.Error = "task_state_machine_blocked"
			return p.end(GameFailure)
		}
		if p.shouldStop() {
			return p.end(GameStopped)
		}
		if p.runtimeBudgetExceeded(step.Role) {
			return p.end(GameFailure)
		}

		turn := p.runStateMachineTurn(workspace, promptOpts, step)
		if p.shouldStop() {
			return p.end(GameStopped)
		}

		// The state machine owns the prover/verify/repair/finalize transitions.
		// The ledger records the state the machine just computed instead of
		// re-deriving it, so the two cannot drift (notably in review mode, where
		// a verifier turn returns to implement rather than repair).
		result, terminal := machine.advance(turn)
		p.updateObjectiveLedger(p.Result.Rounds, turn.Role, turn, machine.state)

		if turn.Error != "" {
			if p.turnFailureExceeded(turn, &recoverableFailures) {
				return p.end(GameFailure)
			}
		} else {
			recoverableFailures = 0
		}
		if terminal {
			if result == GameSuccess && !p.fixedReviewMode() && turn.Role == "verifier" && turn.Status == StatusConcede {
				p.Result.VerifierConceded = true
			}
			if result == GameSuccess && p.Config.Verbose {
				if p.fixedReviewMode() {
					log.Printf("=== PVG REVIEW CYCLES COMPLETE ===")
				} else {
					log.Printf("=== PVG SUCCESS: verifier conceded ===")
				}
			}
			return p.end(result)
		}
		if p.overBudget(p.Result.Rounds, step.Role) {
			return p.end(GameFailure)
		}
		workspace = p.Executor.WorkspaceState()
	}
}

func (p *PVG) runStateMachineTurn(workspace string, promptOpts spec.PromptOptions, step taskStateStep) TurnResult {
	switch step.Role {
	case "prover":
		return p.runProverTurn(workspace, promptOpts, step.State, step.Reason)
	case "verifier":
		return p.runVerifierTurn(workspace, promptOpts, step.Reason)
	default:
		return TurnResult{
			Role:   step.Role,
			Status: StatusContinue,
			Logs:   "task_state_machine_unknown_role",
			Error:  "task_state_machine_unknown_role",
		}
	}
}

func (p *PVG) runProverTurn(workspace string, promptOpts spec.PromptOptions, state ObjectiveState, reason string) TurnResult {
	p.Result.Rounds++
	p.Result.ProverRounds++
	roundNum := p.Result.Rounds
	p.transitionObjectiveState(state, roundNum, "prover", reason)
	p.Evidence.Log("round_start", roundNum, "prover", nil)

	task := spec.RenderProverTask(p.Compiled, workspace, p.State.TranscriptPath, promptOpts)
	return p.runAgentTurn(roundNum, "prover", p.Result.ProverRounds, task)
}

func (p *PVG) runVerifierTurn(workspace string, promptOpts spec.PromptOptions, reason string) TurnResult {
	p.Result.Rounds++
	p.Result.VerifierRounds++
	roundNum := p.Result.Rounds
	p.transitionObjectiveState(ObjectiveStateVerify, roundNum, "verifier", reason)
	p.Evidence.Log("round_start", roundNum, "verifier", nil)

	task := spec.RenderVerifierTask(p.Compiled, workspace, p.State.TranscriptPath, promptOpts)
	return p.runAgentTurn(roundNum, "verifier", p.Result.VerifierRounds, task)
}

func (p *PVG) turnFailureExceeded(turn TurnResult, consecutive *int) bool {
	if !turn.Recoverable {
		p.Result.Error = turn.Error
		return true
	}
	*consecutive++
	data := map[string]interface{}{
		"error":                turn.Error,
		"consecutive_failures": *consecutive,
		"max_failures":         maxRecoverableAgentFailures,
	}
	if code := turnErrorCode(turn.Error); code != "" {
		data["error_code"] = code
	}
	p.Evidence.Log("agent_failure_recoverable", p.Result.Rounds, turn.Role, data)
	if *consecutive <= maxRecoverableAgentFailures {
		return false
	}
	p.Result.Error = fmt.Sprintf("agent_failure_budget_exceeded: %s", turn.Error)
	return true
}

func (p *PVG) fixedReviewMode() bool {
	return p.Config.Until > 0
}

func (p *PVG) shouldStop() bool {
	return p.Config.StopRequested != nil && p.Config.StopRequested()
}

func (p *PVG) promptOptions() spec.PromptOptions {
	return spec.PromptOptions{
		Controller:      p.Config.IsController,
		PrimarySpecPath: p.Config.PrimarySpecPath,
		ReviewMode:      p.fixedReviewMode(),
		ReviewCycles:    p.Config.Until,
	}
}

func (p *PVG) runAgentTurn(roundNum int, role string, roleRound int, task string) TurnResult {
	ts := p.State.Turn(roundNum, role)
	ts.StopRequested = p.Config.StopRequested
	ts.Budget = p.turnBudget()
	ts.ProtocolMode = p.turnProtocolMode(role)
	if err := WriteTurnTask(ts, task); err != nil {
		turn := TurnResult{
			Role:   role,
			Status: StatusContinue,
			Logs:   fmt.Sprintf("turn_prepare_failed:%v", err),
			Error:  fmt.Sprintf("turn_prepare_failed:%v", err),
		}
		p.Evidence.LogAgent(roundNum, role, string(turn.Status), turn.Logs, &turn.Stats, turn.Error)
		AppendTurnWithOptions(p.State.TranscriptPath, role, roleRound, string(turn.Status),
			turn.Logs, &turn.Stats, fmt.Sprintf("%04d-%s", roundNum, role), turn.Error,
			AppendTurnOptions{
				IncludeStatus: !p.fixedReviewMode(),
				SessionPath:   ts.SessionPath(),
				EvidencePath:  p.State.EvidencePath,
			})
		return turn
	}

	turn := p.Executor.ExecuteTurn(task, role, ts)
	p.Result.Accumulate(turn.Stats)
	p.Evidence.LogAgent(roundNum, role, string(turn.Status), turn.Logs, &turn.Stats, turn.Error)

	AppendTurnWithOptions(p.State.TranscriptPath, role, roleRound, string(turn.Status),
		turn.Logs, &turn.Stats, fmt.Sprintf("%04d-%s", roundNum, role), turn.Error,
		AppendTurnOptions{
			IncludeStatus: !p.fixedReviewMode(),
			SessionPath:   ts.SessionPath(),
			EvidencePath:  p.State.EvidencePath,
		})

	return turn
}

func (p *PVG) turnProtocolMode(role string) string {
	if role == "verifier" && p.fixedReviewMode() {
		return "review"
	}
	return "pvg"
}

func (p *PVG) turnBudget() TurnBudget {
	budget := TurnBudget{
		MaxInputTokens:  p.Config.MaxInputTokens,
		MaxOutputTokens: p.Config.MaxOutputTokens,
		MaxToolLoops:    p.Config.MaxToolLoops,
		AgentTimeoutSec: p.Config.AgentTimeoutSec,
	}
	if p.Config.MaxCostUSD != nil {
		budget.MaxCostUSD = p.Config.MaxCostUSD
		remaining := *p.Config.MaxCostUSD - p.Result.TotalCostUSD
		budget.RemainingCostUSD = &remaining
	}
	maxDuration := p.Config.MaxDurationSec
	if maxDuration <= 0 {
		maxDuration = DefaultMaxDurationSec
	}
	budget.MaxDurationSec = maxDuration
	if !p.started.IsZero() {
		budget.RemainingDurationSec = p.remainingDurationSec(maxDuration)
	}
	if p.Config.MaxInputTokens > 0 {
		budget.RemainingInputTokens = p.Config.MaxInputTokens - p.Result.TotalInputTokens
	}
	if p.Config.MaxOutputTokens > 0 {
		budget.RemainingOutputTokens = p.Config.MaxOutputTokens - p.Result.TotalOutputTokens
	}
	return budget
}

func (p *PVG) remainingDurationSec(maxDuration int) int {
	if p.started.IsZero() {
		return 0
	}
	remaining := time.Until(p.started.Add(time.Duration(maxDuration) * time.Second))
	if remaining <= 0 {
		return 0
	}
	return int((remaining + time.Second - 1) / time.Second)
}

func (p *PVG) end(result GameResult) *PVGResult {
	p.Result.GameResult = result
	p.Result.CompletionReason = p.completionReason(result)
	errMsg := ""
	if p.Result.Error != "" {
		errMsg = p.Result.Error
	}
	p.Evidence.LogGameEnd(string(result), p.Result.Rounds,
		p.Result.ProverRounds, p.Result.VerifierRounds,
		p.Result.VerifierConceded, p.Result.TotalCostUSD, p.Result.CostUnavailable,
		p.Result.TotalInputTokens, p.Result.TotalOutputTokens,
		p.Result.TotalCacheReadTokens, p.Result.TotalCacheCreateTokens,
		errMsg, p.Result.CompletionReason)

	AppendGameResult(p.State.TranscriptPath, string(result), errMsg)
	return p.Result
}

func (p *PVG) completionReason(result GameResult) string {
	switch result {
	case GameSuccess:
		if p.fixedReviewMode() {
			return "review_cycles_complete"
		}
		if p.Result.VerifierConceded {
			return "verifier_conceded"
		}
		return "success"
	case GameStopped:
		return "stopped"
	default:
		if p.Result.Error == "runtime_budget_exhausted" || strings.HasPrefix(p.Result.Error, "runtime_budget_exhausted:") {
			return "runtime_budget_exhausted"
		}
		return "failure"
	}
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
		"budget":           "max_cost_usd",
		"session_cost_usd": p.Result.TotalCostUSD,
		"cap_usd":          cap,
	})
	p.Result.Error = "runtime_budget_exhausted:max_cost_usd"
	return true
}

func (p *PVG) runtimeBudgetExceeded(nextRole string) bool {
	maxRounds := p.Config.MaxRounds
	if maxRounds <= 0 {
		maxRounds = DefaultMaxRounds
	}
	if p.Result.Rounds >= maxRounds {
		p.Result.Error = "runtime_budget_exhausted:max_rounds"
		p.Evidence.Log("budget_exceeded", p.Result.Rounds, nextRole, map[string]interface{}{
			"budget":     "max_rounds",
			"rounds":     p.Result.Rounds,
			"max_rounds": maxRounds,
		})
		return true
	}
	maxDuration := p.Config.MaxDurationSec
	if maxDuration <= 0 {
		maxDuration = DefaultMaxDurationSec
	}
	if !p.started.IsZero() && time.Since(p.started) >= time.Duration(maxDuration)*time.Second {
		p.Result.Error = "runtime_budget_exhausted:max_duration"
		p.Evidence.Log("budget_exceeded", p.Result.Rounds, nextRole, map[string]interface{}{
			"budget":           "max_duration",
			"duration_sec":     int(time.Since(p.started).Seconds()),
			"max_duration_sec": maxDuration,
		})
		return true
	}
	if p.Config.AgentTimeoutSec > 0 && !p.started.IsZero() {
		remaining := time.Until(p.started.Add(time.Duration(maxDuration) * time.Second))
		required := time.Duration(p.Config.AgentTimeoutSec) * time.Second
		if remaining < required {
			p.Result.Error = "runtime_budget_exhausted:max_duration"
			p.Evidence.Log("budget_exceeded", p.Result.Rounds, nextRole, map[string]interface{}{
				"budget":                 "max_duration",
				"duration_sec":           int(time.Since(p.started).Seconds()),
				"max_duration_sec":       maxDuration,
				"remaining_duration_sec": int(remaining.Seconds()),
				"required_turn_sec":      p.Config.AgentTimeoutSec,
			})
			return true
		}
	}
	if p.Config.MaxInputTokens > 0 && p.Result.TotalInputTokens >= p.Config.MaxInputTokens {
		p.Result.Error = "runtime_budget_exhausted:max_input_tokens"
		p.Evidence.Log("budget_exceeded", p.Result.Rounds, nextRole, map[string]interface{}{
			"budget":           "max_input_tokens",
			"input_tokens":     p.Result.TotalInputTokens,
			"max_input_tokens": p.Config.MaxInputTokens,
		})
		return true
	}
	if p.Config.MaxOutputTokens > 0 && p.Result.TotalOutputTokens >= p.Config.MaxOutputTokens {
		p.Result.Error = "runtime_budget_exhausted:max_output_tokens"
		p.Evidence.Log("budget_exceeded", p.Result.Rounds, nextRole, map[string]interface{}{
			"budget":            "max_output_tokens",
			"output_tokens":     p.Result.TotalOutputTokens,
			"max_output_tokens": p.Config.MaxOutputTokens,
		})
		return true
	}
	return false
}

func turnErrorCode(errText string) string {
	for i, r := range errText {
		switch {
		case r == ':' || r == ' ' || r == '\n' || r == '\t':
			if i == 0 {
				return ""
			}
			return errText[:i]
		}
	}
	return ""
}

package game

import (
	"fmt"
	"log"

	"github.com/telos-org/telos-go/internal/evidence"
	"github.com/telos-org/telos-go/internal/spec"
)

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

	maxRounds := p.Config.MaxRounds
	if maxRounds <= 0 {
		maxRounds = 20
	}

	// Round 1: prover build
	if p.shouldStop() {
		return p.end(GameStopped), nil
	}
	p.Result.Rounds = 1
	p.Result.ProverRounds = 1
	p.Evidence.Log("round_start", 1, "prover", nil)

	task := spec.RenderProverTask(p.Compiled, 1, "", ReadTranscript(p.State.TranscriptPath))
	turn := p.runAgentTurn(1, "prover", p.Result.ProverRounds, task)
	if p.shouldStop() {
		return p.end(GameStopped), nil
	}
	if turn.Error != "" {
		p.Result.Error = turn.Error
		return p.end(GameFailure), nil
	}
	if p.overBudget(1, "prover") {
		return p.end(GameFailure), nil
	}

	// Alternating verifier/prover rounds
	for p.Result.Rounds < maxRounds {
		if p.shouldStop() {
			return p.end(GameStopped), nil
		}
		// Verifier
		workspace := p.Executor.WorkspaceState()
		p.Result.Rounds++
		p.Result.VerifierRounds++
		roundNum := p.Result.Rounds
		p.Evidence.Log("round_start", roundNum, "verifier", nil)

		task = spec.RenderVerifierTask(p.Compiled, workspace, ReadTranscript(p.State.TranscriptPath))
		turn = p.runAgentTurn(roundNum, "verifier", p.Result.VerifierRounds, task)
		if p.shouldStop() {
			return p.end(GameStopped), nil
		}
		if turn.Error != "" {
			p.Result.Error = turn.Error
			return p.end(GameFailure), nil
		}
		if turn.Status == StatusConcede {
			p.Result.VerifierConceded = true
			if p.Config.Verbose {
				log.Printf("=== PVG SUCCESS: verifier conceded ===")
			}
			return p.end(GameSuccess), nil
		}
		if p.overBudget(roundNum, "verifier") {
			return p.end(GameFailure), nil
		}

		if p.Result.Rounds >= maxRounds {
			break
		}

		if p.shouldStop() {
			return p.end(GameStopped), nil
		}
		// Prover fix
		workspace = p.Executor.WorkspaceState()
		p.Result.Rounds++
		p.Result.ProverRounds++
		roundNum = p.Result.Rounds
		p.Evidence.Log("round_start", roundNum, "prover", nil)

		task = spec.RenderProverTask(p.Compiled, p.Result.ProverRounds, workspace, ReadTranscript(p.State.TranscriptPath))
		turn = p.runAgentTurn(roundNum, "prover", p.Result.ProverRounds, task)
		if p.shouldStop() {
			return p.end(GameStopped), nil
		}
		if turn.Error != "" {
			p.Result.Error = turn.Error
			return p.end(GameFailure), nil
		}
		if p.overBudget(roundNum, "prover") {
			return p.end(GameFailure), nil
		}
	}

	if p.Config.Verbose {
		log.Printf("=== PVG TIMEOUT ===")
	}
	return p.end(GameTimeout), nil
}

func (p *PVG) shouldStop() bool {
	return p.Config.StopRequested != nil && p.Config.StopRequested()
}

func (p *PVG) runAgentTurn(roundNum int, role string, roleRound int, task string) TurnResult {
	ts := p.State.Turn(roundNum, role)
	WriteTurnTask(ts, task)

	turn := p.Executor.ExecuteTurn(task, role, ts)
	p.Result.Accumulate(turn.Stats)
	p.Evidence.LogAgent(roundNum, role, string(turn.Status), turn.Logs, &turn.Stats)

	AppendTurn(p.State.TranscriptPath, role, roleRound, string(turn.Status),
		turn.Logs, &turn.Stats, fmt.Sprintf("%04d-%s", roundNum, role),
		ts.TaskPath(), ts.RawLogPath(), turn.Error)

	return turn
}

func (p *PVG) end(result GameResult) *PVGResult {
	p.Result.GameResult = result
	errMsg := ""
	if p.Result.Error != "" {
		errMsg = p.Result.Error
	}
	p.Evidence.LogGameEnd(string(result), p.Result.Rounds,
		p.Result.ProverRounds, p.Result.VerifierRounds,
		p.Result.VerifierConceded, p.Result.TotalCostUSD,
		p.Result.TotalInputTokens, p.Result.TotalOutputTokens,
		p.Result.TotalCacheReadTokens, p.Result.TotalCacheCreateTokens,
		errMsg)

	AppendGameResult(p.State.TranscriptPath, string(result), errMsg)
	return p.Result
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

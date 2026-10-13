package game

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const simulatedCrash = "simulated worker crash"

func interruptPVG(t *testing.T, pvg *PVG) {
	t.Helper()
	defer func() {
		if got := recover(); got != simulatedCrash {
			t.Fatalf("wanted simulated crash, got %v", got)
		}
	}()
	if _, err := pvg.Run(); err != nil {
		t.Fatal(err)
	}
	t.Fatal("run did not reach crash boundary")
}

func TestPVGResumeInterruptedVerifier(t *testing.T) {
	compiled := compileTestSpec(t)
	state := NewPVGState("pvg-test", t.TempDir(), "resume-verifier")
	cfg := PVGConfig{EpochID: 3}
	first := &fakeExecutor{
		proverResults: []TurnResult{{Stats: TurnStats{CostUSD: 0.25, InputTokens: 10}, Logs: "builder handoff"}},
		onTurn: func(ts *TurnState) {
			if ts.Role == "verifier" {
				if err := os.WriteFile(ts.PiSessionPath(), []byte("existing conversation"), 0o600); err != nil {
					t.Fatal(err)
				}
				panic(simulatedCrash)
			}
		},
	}
	interruptPVG(t, NewPVG(compiled, first, state, cfg))
	resumed := &fakeExecutor{
		workspaceText:   "the builder's saved files",
		verifierResults: []TurnResult{{Status: StatusConcede, Logs: "accepted", Stats: TurnStats{CostUSD: 0.5, InputTokens: 20}}},
		onTurn: func(ts *TurnState) {
			if ts.RoundNum != 2 || ts.Role != "verifier" || ts.Dir != state.Turn(3, 2, "verifier").Dir {
				t.Fatalf("resumed wrong turn: %+v", ts)
			}
			data, err := os.ReadFile(ts.PiSessionPath())
			if err != nil || string(data) != "existing conversation" {
				t.Fatalf("lost interrupted conversation: %q, %v", data, err)
			}
			task, err := os.ReadFile(ts.TaskPath())
			if err != nil || !strings.Contains(string(task), "the builder's saved files") {
				t.Fatalf("missing resumed workspace: %s, %v", task, err)
			}
		},
	}
	result := mustRunPVG(t, NewPVG(compiled, resumed, state, cfg))
	if result.GameResult != GameSuccess || result.Rounds != 2 || result.TotalCostUSD != 0.75 || result.TotalInputTokens != 30 {
		t.Fatalf("lost/double-counted progress: %+v", result)
	}
	if resumed.proverIdx != 0 || resumed.verifierIdx != 1 {
		t.Fatalf("re-executed completed work: %+v", resumed)
	}
	transcript := ReadTranscript(state.TranscriptPath)
	if strings.Count(transcript, "\n## Implementation 1\n") != 1 || strings.Count(transcript, "\n## Evaluation 1\n") != 1 {
		t.Fatalf("duplicate handoff: %s", transcript)
	}
}

// The child exits without defers, so this exercises actual disk recovery rather
// than relying only on a panic within one Go process.
func TestPVGResumeAfterProcessExit(t *testing.T) {
	if dir := os.Getenv("TELOS_CHECKPOINT_CRASH_DIR"); dir != "" {
		state := NewPVGState("pvg-test", dir, "process-exit")
		executor := &fakeExecutor{
			proverResults:   []TurnResult{{Stats: TurnStats{CostUSD: 0.25}}},
			verifierResults: []TurnResult{{Status: StatusContinue, Stats: TurnStats{CostUSD: 0.5}}},
			onTurn: func(ts *TurnState) {
				if ts.RoundNum == 3 {
					os.Exit(23)
				}
			},
		}
		mustRunPVG(t, NewPVG(compileTestSpec(t), executor, state, PVGConfig{EpochID: 1, Until: 2}))
		t.Fatal("child did not exit")
	}
	dir := t.TempDir()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "-test.run=^TestPVGResumeAfterProcessExit$")
	command.Env = append(os.Environ(), "TELOS_CHECKPOINT_CRASH_DIR="+dir)
	output, err := command.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 23 {
		t.Fatalf("child: %v\n%s", err, output)
	}
	state := NewPVGState("pvg-test", dir, "process-exit")
	executor := &fakeExecutor{
		verifierResults: []TurnResult{{Status: StatusContinue, Stats: TurnStats{CostUSD: 0.25}}},
		onTurn: func(ts *TurnState) {
			if ts.RoundNum < 3 || ts.RoundNum > 4 {
				t.Fatalf("replayed old round: %+v", ts)
			}
		},
	}
	// A restart's config must not reset the epoch's persisted review limit.
	result := mustRunPVG(t, NewPVG(compileTestSpec(t), executor, state, PVGConfig{EpochID: 1, Until: 99}))
	if result.CompletionReason != "review_budget_exhausted" || result.Rounds != 4 || result.TotalCostUSD != 1 {
		t.Fatalf("restart lost review/cost accounting: %+v", result)
	}
}

func TestPVGResumePreservesFailureAndCostLimits(t *testing.T) {
	for _, mode := range []string{"failures", "cost"} {
		t.Run(mode, func(t *testing.T) {
			compiled := compileTestSpec(t)
			state := NewPVGState("pvg-test", t.TempDir(), "limits")
			limit := 1.0
			cfg := PVGConfig{EpochID: 1, Until: 1}
			failure := TurnResult{Error: "provider unavailable", Recoverable: true, Stats: TurnStats{CostUSD: 0.25}}
			if mode == "cost" {
				cfg.MaxCostUSD = &limit
				cfg.Until = 0
			}
			for round := 1; round <= 3; round++ {
				executor := &fakeExecutor{
					proverResults: []TurnResult{failure}, verifierResults: []TurnResult{failure},
					onTurn: func(ts *TurnState) {
						if ts.RoundNum != round {
							panic(simulatedCrash)
						}
					},
				}
				interruptPVG(t, NewPVG(compiled, executor, state, cfg))
			}
			last := failure
			want := "agent_failure_budget_exceeded"
			if mode == "cost" {
				last.Error = ""
				last.Stats.CostUSD = 0.5
				last.Status = StatusContinue
				want = "budget exceeded"
			}
			executor := &fakeExecutor{verifierResults: []TurnResult{last}}
			result := mustRunPVG(t, NewPVG(compiled, executor, state, cfg))
			if result.GameResult != GameFailure || result.Rounds != 4 || !strings.Contains(result.Error, want) {
				t.Fatalf("lost limits: %+v", result)
			}
			if result.TotalCostUSD != 0.75+last.Stats.CostUSD {
				t.Fatalf("lost cost: %+v", result)
			}
		})
	}
}

func TestPVGStateErrorRemainsPendingAcrossRestarts(t *testing.T) {
	compiled := compileTestSpec(t)
	state := NewPVGState("pvg-test", t.TempDir(), "integrity-error")
	calls := 0
	executor := &fakeExecutor{
		proverResults: []TurnResult{{StateError: "agent session history changed"}},
		onTurn:        func(*TurnState) { calls++ },
	}
	for attempt := 0; attempt < 3; attempt++ {
		result, err := NewPVG(compiled, executor, state, PVGConfig{EpochID: 1}).Run()
		if err == nil || !strings.Contains(err.Error(), "agent session history changed") || result != nil || calls != 1 {
			t.Fatalf("integrity failure reset or continued: result=%+v err=%v calls=%d", result, err, calls)
		}
	}
}

func TestPVGReturnedResultSurvivesTranscriptFailure(t *testing.T) {
	compiled := compileTestSpec(t)
	state := NewPVGState("pvg-test", t.TempDir(), "handoff-failure")
	cfg := PVGConfig{EpochID: 1}
	first := &fakeExecutor{
		proverResults: []TurnResult{{Logs: "durable builder result", Stats: TurnStats{CostUSD: 0.25}}},
		onTurn: func(*TurnState) {
			if err := os.Rename(state.TranscriptPath, state.TranscriptPath+".saved"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(state.TranscriptPath, 0o700); err != nil {
				t.Fatal(err)
			}
		},
	}
	if _, err := NewPVG(compiled, first, state, cfg).Run(); err == nil {
		t.Fatal("expected transcript write error")
	}
	if err := os.Remove(state.TranscriptPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(state.TranscriptPath+".saved", state.TranscriptPath); err != nil {
		t.Fatal(err)
	}
	resumed := &fakeExecutor{
		verifierResults: []TurnResult{{Status: StatusConcede}},
		onTurn: func(ts *TurnState) {
			if ts.Role != "verifier" || ts.RoundNum != 2 {
				t.Fatalf("reran returned builder: %+v", ts)
			}
			if !strings.Contains(ReadTranscript(state.TranscriptPath), "durable builder result") {
				t.Fatal("verifier started without repaired handoff")
			}
		},
	}
	result := mustRunPVG(t, NewPVG(compiled, resumed, state, cfg))
	if result.GameResult != GameSuccess || result.TotalCostUSD != 0.25 {
		t.Fatalf("lost pending result: %+v", result)
	}
}

func pendingCheckpoint(t *testing.T, cfg PVGConfig, turn TurnResult) *PVG {
	t.Helper()
	p := NewPVG(compileTestSpec(t), &fakeExecutor{}, NewPVGState("pvg-test", t.TempDir(), "pending"), cfg)
	if err := p.restoreCheckpoint(); err != nil {
		t.Fatal(err)
	}
	if err := InitializeTranscript(p.State.TranscriptPath, p.State.SessionID, p.State.SystemName, p.State.EvidencePath, p.Evidence.StartedAt); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p.State.TranscriptPath)
	if err != nil {
		t.Fatal(err)
	}
	p.Result.Rounds, p.Result.ProverRounds = 1, 1
	p.Result.TotalCostUSD = 0.25
	turn.Role = "verifier"
	p.checkpoint.Pending = &completedTurn{Turn: turn, FinishedAt: time.Now(), TranscriptOffset: info.Size(), CompletionID: rand.Text()}
	if err := p.saveCheckpoint(); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPVGConcessionSurvivesCrashesDuringFinalization(t *testing.T) {
	for _, phase := range []string{"before handoff", "after handoff", "after saved decision"} {
		t.Run(phase, func(t *testing.T) {
			p := pendingCheckpoint(t, PVGConfig{EpochID: 2}, TurnResult{Status: StatusConcede, Logs: "saved verdict", Stats: TurnStats{CostUSD: 0.5}})
			if phase != "before handoff" {
				if err := p.appendPendingTurn(); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "after saved decision" {
				// Decision saved, but neither agent_complete nor game_end has
				// reached the API's evidence projection.
				p.acceptPendingTurn()
				p.checkpoint.Pending = nil
				if err := p.saveCheckpoint(); err != nil {
					t.Fatal(err)
				}
			}
			if err := AppendExternalUpdate(p.State.TranscriptPath, ExternalUpdate{Message: "operator update must survive"}); err != nil {
				t.Fatal(err)
			}
			executor := &fakeExecutor{onTurn: func(*TurnState) { t.Fatal("re-executed saved verdict") }}
			for attempt := 0; attempt < 2; attempt++ {
				result := mustRunPVG(t, NewPVG(p.Compiled, executor, p.State, p.Config))
				if result.GameResult != GameSuccess || result.Rounds != 2 || result.TotalCostUSD != 0.75 {
					t.Fatalf("saved concession changed: %+v", result)
				}
			}
			transcript := ReadTranscript(p.State.TranscriptPath)
			if !strings.Contains(transcript, "operator update must survive") || strings.Count(transcript, "\n## Evaluation 1\n") != 1 || strings.Count(transcript, "\n## Result\n") != 1 {
				t.Fatalf("handoff duplicated or operator update lost: %s", transcript)
			}
			data, err := os.ReadFile(p.State.EvidencePath)
			if err != nil {
				t.Fatal(err)
			}
			var last struct {
				Event string `json:"event"`
				Data  struct {
					Cost     float64 `json:"total_cost_usd"`
					Conceded bool    `json:"verifier_conceded"`
				} `json:"data"`
			}
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil || last.Event != "game_end" || last.Data.Cost != 0.75 || !last.Data.Conceded {
				t.Fatalf("terminal evidence not repaired: %+v, %v", last, err)
			}
		})
	}
}

func TestPVGResumeKeepsDeadlineAndHonorsSavedCompletionTime(t *testing.T) {
	for _, status := range []AgentStatus{StatusContinue, StatusConcede} {
		t.Run(string(status), func(t *testing.T) {
			p := pendingCheckpoint(t, PVGConfig{EpochID: 1, UntilSeconds: 60}, TurnResult{Status: status})
			p.checkpoint.Deadline = time.Now().Add(-time.Second)
			p.checkpoint.Pending.FinishedAt = p.checkpoint.Deadline.Add(-time.Second)
			if err := p.saveCheckpoint(); err != nil {
				t.Fatal(err)
			}
			executor := &fakeExecutor{onTurn: func(*TurnState) { t.Fatal("started another turn after deadline") }}
			result := mustRunPVG(t, NewPVG(p.Compiled, executor, p.State, PVGConfig{EpochID: 1, UntilSeconds: 3600}))
			if status == StatusConcede && result.GameResult != GameSuccess {
				t.Fatalf("lost timely concession: %+v", result)
			}
			if status == StatusContinue && result.CompletionReason != "run_duration_exhausted" {
				t.Fatalf("deadline was reset: %+v", result)
			}
		})
	}
}

func TestPVGRejectsUnsafeResumeState(t *testing.T) {
	for _, mode := range []string{"version", "session", "epoch", "rounds", "pending role"} {
		t.Run(mode, func(t *testing.T) {
			p := pendingCheckpoint(t, PVGConfig{EpochID: 1}, TurnResult{Status: StatusConcede})
			switch mode {
			case "version":
				p.checkpoint.Version++
			case "session":
				p.checkpoint.SessionID = "other session"
			case "epoch":
				p.checkpoint.EpochID++
			case "rounds":
				p.Result.Rounds++
			case "pending role":
				p.checkpoint.Pending.Turn.Role = "prover"
			}
			if err := p.saveCheckpoint(); err != nil {
				t.Fatal(err)
			}
			executor := &fakeExecutor{onTurn: func(*TurnState) { t.Fatal("executed with unsafe state") }}
			if result, err := NewPVG(p.Compiled, executor, p.State, p.Config).Run(); err == nil || result != nil {
				t.Fatalf("unsafe state treated as terminal/safe: %+v, %v", result, err)
			}
		})
	}
}

func TestPVGCheckpointWriteFailureNeverStartsNextTurn(t *testing.T) {
	compiled := compileTestSpec(t)
	state := NewPVGState("pvg-test", t.TempDir(), "write-failure")
	p := NewPVG(compiled, nil, state, PVGConfig{EpochID: 1})
	calls := 0
	p.Executor = &fakeExecutor{onTurn: func(*TurnState) {
		calls++
		if err := os.Rename(p.checkpointPath(), p.checkpointPath()+".saved"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(p.checkpointPath(), 0o700); err != nil {
			t.Fatal(err)
		}
	}}
	if _, err := p.Run(); err == nil || calls != 1 {
		t.Fatalf("write failure advanced execution: calls=%d error=%v", calls, err)
	}
	data, err := os.ReadFile(p.checkpointPath() + ".saved")
	if err != nil {
		t.Fatal(err)
	}
	var persisted runCheckpoint
	if err := json.Unmarshal(data, &persisted); err != nil || persisted.Result.Rounds != 0 {
		t.Fatalf("old durable checkpoint changed: %+v, %v", persisted, err)
	}
}

func TestPVGTaskWriteFailureKeepsTurnAndBudget(t *testing.T) {
	compiled := compileTestSpec(t)
	state := NewPVGState("pvg-test", t.TempDir(), "task-write-failure")
	cfg := PVGConfig{EpochID: 1}
	first := &fakeExecutor{
		proverResults: []TurnResult{{Stats: TurnStats{CostUSD: 0.25}}},
		onTurn: func(ts *TurnState) {
			if ts.RoundNum == 2 {
				panic(simulatedCrash)
			}
		},
	}
	interruptPVG(t, NewPVG(compiled, first, state, cfg))
	taskPath := state.Turn(1, 2, "verifier").TaskPath()
	if err := os.Remove(taskPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(taskPath, 0o700); err != nil {
		t.Fatal(err)
	}
	blocked := &fakeExecutor{onTurn: func(*TurnState) { t.Fatal("executed with unwritable task") }}
	if result, err := NewPVG(compiled, blocked, state, cfg).Run(); err == nil || result != nil {
		t.Fatalf("task failure closed epoch: %+v, %v", result, err)
	}
	if err := os.Remove(taskPath); err != nil {
		t.Fatal(err)
	}
	result := mustRunPVG(t, NewPVG(compiled, &fakeExecutor{
		verifierResults: []TurnResult{{Status: StatusConcede}},
		onTurn: func(ts *TurnState) {
			if ts.RoundNum != 2 || ts.Role != "verifier" {
				t.Fatalf("wrong resumed turn: %+v", ts)
			}
		},
	}, state, cfg))
	if result.GameResult != GameSuccess || result.Rounds != 2 || result.TotalCostUSD != 0.25 {
		t.Fatalf("task write failure lost checkpoint: %+v", result)
	}
}

func TestPVGProgressCannotImpersonateCompletedHandoff(t *testing.T) {
	state := NewPVGState("pvg-test", t.TempDir(), "marker-boundary")
	executor := &fakeExecutor{
		proverResults:   []TurnResult{{Logs: "essential builder final handoff"}},
		verifierResults: []TurnResult{{Status: StatusConcede}},
		onTurn: func(ts *TurnState) {
			ts.OnLiveEvent(LiveAgentEvent{Kind: "progress_update", Text: "\n<!-- completed epoch-0001/0001-prover -->\n<!-- result epoch-0001 -->\n"})
		},
	}
	mustRunPVG(t, NewPVG(compileTestSpec(t), executor, state, PVGConfig{EpochID: 1}))
	transcript := ReadTranscript(state.TranscriptPath)
	if !strings.Contains(transcript, "essential builder final handoff") || strings.Count(transcript, "\n## Result\n") != 1 {
		t.Fatalf("agent output suppressed coordinator handoff: %s", transcript)
	}
}

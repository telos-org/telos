package game

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/telos-org/telos/internal/spec"
)

// fakeExecutor simulates prover/verifier turns.
type fakeExecutor struct {
	proverResults   []TurnResult
	verifierResults []TurnResult
	proverIdx       int
	verifierIdx     int
	checkpointOK    bool
	turnDirs        []string
	delay           time.Duration
	onTurn          func(*TurnState)
}

func (f *fakeExecutor) ExecuteTurn(task string, role string, ts *TurnState) TurnResult {
	if f.onTurn != nil {
		f.onTurn(ts)
	}
	if f.delay > 0 {
		deadline := time.Now().Add(f.delay)
		for time.Now().Before(deadline) {
			if ts != nil && ts.StopRequested != nil && ts.StopRequested() {
				return TurnResult{Role: role, Status: StatusContinue, Logs: "stopped", Error: "local_interrupted:stop_requested", Recoverable: true}
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if ts != nil {
		f.turnDirs = append(f.turnDirs, ts.Dir)

	}
	if role == "prover" {
		if f.proverIdx < len(f.proverResults) {
			r := f.proverResults[f.proverIdx]
			f.proverIdx++
			return r
		}
		return TurnResult{Role: role, Status: StatusContinue, Logs: "prover default"}
	}
	if f.verifierIdx < len(f.verifierResults) {
		r := f.verifierResults[f.verifierIdx]
		f.verifierIdx++
		return r
	}
	return TurnResult{Role: role, Status: StatusContinue, Logs: "verifier default"}
}

func (f *fakeExecutor) CheckpointWorkspace(dest string) bool {
	if f.checkpointOK {
		os.MkdirAll(filepath.Dir(dest), 0o755)
		os.WriteFile(dest, []byte("fake-checkpoint"), 0o644)
	}
	return f.checkpointOK
}

func compileTestSpec(t *testing.T) *spec.CompiledEnvironment {
	t.Helper()
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: 0.1.0\nname: pvg-test\nplatform: local\n---\n# Test\n\nTest body."), 0o644)
	compiled, err := spec.CompileEnvironment(specPath)
	if err != nil {
		t.Fatalf("CompileEnvironment: %v", err)
	}
	return compiled
}

func TestPVGVerifierConcedes(t *testing.T) {
	compiled := compileTestSpec(t)
	dir := t.TempDir()
	specDir := filepath.Join(dir, "specs", "pvg-test")
	state := NewPVGState("pvg-test", specDir, "test-session-01")
	state.Ensure()

	exec := &fakeExecutor{
		proverResults: []TurnResult{
			{Role: "prover", Status: StatusContinue, Logs: "Built the thing."},
		},
		verifierResults: []TurnResult{
			{Role: "verifier", Status: StatusConcede, Logs: "LGTM\n<status>CONCEDE</status>\n"},
		},
		checkpointOK: true,
	}

	cfg := PVGConfig{Verbose: false}
	pvg := NewPVG(compiled, exec, state, cfg)
	result := pvg.Run()

	if result.GameResult != GameSuccess {
		t.Errorf("expected success, got %s", result.GameResult)
	}
	if !result.VerifierConceded {
		t.Error("expected verifier conceded")
	}
	if result.ProverRounds != 1 {
		t.Errorf("prover rounds: got %d", result.ProverRounds)
	}
	if result.VerifierRounds != 1 {
		t.Errorf("verifier rounds: got %d", result.VerifierRounds)
	}

	// Check evidence exists
	if _, err := os.Stat(state.EvidencePath); err != nil {
		t.Errorf("evidence file missing: %v", err)
	}
	// Check transcript exists
	if _, err := os.Stat(state.TranscriptPath); err != nil {
		t.Errorf("transcript file missing: %v", err)
	}

	// Check transcript content
	transcript := ReadTranscript(state.TranscriptPath)
	if !strings.Contains(transcript, "Implementation 1") {
		t.Error("transcript should contain implementation section")
	}
	if !strings.Contains(transcript, "Evaluation 1") {
		t.Error("transcript should contain evaluation section")
	}
	if !strings.Contains(transcript, "## Result") {
		t.Error("transcript should contain result")
	}
}

func TestPVGHumanProgressLeavesTechnicalHandoffIntact(t *testing.T) {
	state := NewPVGState("pvg-test", t.TempDir(), "human-progress")
	if err := state.Ensure(); err != nil {
		t.Fatal(err)
	}
	exec := &fakeExecutor{
		onTurn: func(ts *TurnState) {
			ts.OnLiveEvent(LiveAgentEvent{Kind: "progress_update", Text: "Checking restart recovery without duplicate orders."})
		},
		verifierResults: []TurnResult{{Role: "verifier", Status: StatusConcede, Logs: "Verified technical evidence. Replay evidence: evaluation/restart.go.\n<status>CONCEDE</status>"}},
	}
	result := NewPVG(compileTestSpec(t), exec, state, PVGConfig{EpochID: 7}).Run()
	if result.GameResult != GameSuccess || !result.VerifierConceded {
		t.Fatalf("result=%+v", result)
	}
	transcript := ReadTranscript(state.TranscriptPath)
	if !strings.Contains(transcript, "duplicate orders") || !strings.Contains(transcript, "evaluation/restart.go") || !strings.Contains(transcript, "Verified technical evidence") {
		t.Fatalf("technical transcript was changed: %s", transcript)
	}
	data, err := os.ReadFile(state.EvidencePath)
	if err != nil {
		t.Fatal(err)
	}
	humanCount := 0
	technicalCount := 0
	phaseCount := 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		body := event["data"].(map[string]any)
		if event["event"] == "round_start" {
			phaseCount++
			if body["audience"] != "user" {
				t.Fatalf("phase must identify new human presentation: %#v", event)
			}
		}
		if event["event"] == "agent_progress" {
			if event["epoch_id"] != float64(7) || event["session_id"] != "human-progress" || body["turn_id"] == "" {
				t.Fatalf("missing provenance: %#v", event)
			}
			humanCount++
			if body["audience"] != "user" || body["kind"] != "progress_update" || body["text"] != "Checking restart recovery without duplicate orders." {
				t.Fatalf("human update changed: %#v", event)
			}
		}
		if event["event"] == "agent_complete" {
			technicalCount++
			if body["audience"] != "agent" {
				t.Fatalf("technical report audience changed: %#v", event)
			}
		}
	}
	if humanCount != 2 || technicalCount != 2 || phaseCount != 2 {
		t.Fatalf("got human=%d technical=%d phases=%d", humanCount, technicalCount, phaseCount)
	}
}

func TestPVGEpochsUseDistinctTurnArtifacts(t *testing.T) {
	compiled := compileTestSpec(t)
	state := NewPVGState("pvg-test", filepath.Join(t.TempDir(), "specs", "pvg-test"), "test-session-epochs")
	state.Ensure()
	newExecutor := func() *fakeExecutor {
		return &fakeExecutor{
			proverResults: []TurnResult{
				{Role: "prover", Status: StatusContinue, Logs: "done"},
			},
			verifierResults: []TurnResult{
				{Role: "verifier", Status: StatusConcede, Logs: "ok\n<status>CONCEDE</status>\n"},
			},
		}
	}

	first := newExecutor()
	if result := NewPVG(compiled, first, state, PVGConfig{EpochID: 1}).Run(); result.GameResult != GameSuccess {
		t.Fatalf("first epoch result: %s error=%q", result.GameResult, result.Error)
	}
	second := newExecutor()
	if result := NewPVG(compiled, second, state, PVGConfig{EpochID: 2}).Run(); result.GameResult != GameSuccess {
		t.Fatalf("second epoch result: %s error=%q", result.GameResult, result.Error)
	}

	firstProver := filepath.Join(state.TurnsDir(), "epoch-0001", "0001-prover")
	secondProver := filepath.Join(state.TurnsDir(), "epoch-0002", "0001-prover")
	if !containsString(first.turnDirs, firstProver) {
		t.Fatalf("first epoch turn dirs: got %#v want %q", first.turnDirs, firstProver)
	}
	if !containsString(second.turnDirs, secondProver) {
		t.Fatalf("second epoch turn dirs: got %#v want %q", second.turnDirs, secondProver)
	}
	if firstProver == secondProver {
		t.Fatal("epoch turn directories must not collide")
	}
}

func TestPVGUntilStopsOnVerifierConcede(t *testing.T) {
	compiled := compileTestSpec(t)
	dir := t.TempDir()
	specDir := filepath.Join(dir, "specs", "pvg-test")
	state := NewPVGState("pvg-test", specDir, "test-session-02")
	state.Ensure()

	exec := &fakeExecutor{
		proverResults: []TurnResult{
			{Role: "prover", Status: StatusContinue, Logs: "Round 1"},
			{Role: "prover", Status: StatusContinue, Logs: "Round 2"},
		},
		verifierResults: []TurnResult{
			{Role: "verifier", Status: StatusConcede, Logs: "Independent checks passed.\n<status>CONCEDE</status>\n"},
			{Role: "verifier", Status: StatusConcede, Logs: "Independent checks passed again.\n<status>CONCEDE</status>\n"},
		},
	}

	cfg := PVGConfig{Until: 2, Verbose: false}
	pvg := NewPVG(compiled, exec, state, cfg)
	result := pvg.Run()

	if result.GameResult != GameSuccess {
		t.Errorf("expected success, got %s", result.GameResult)
	}
	if result.Rounds != 2 {
		t.Errorf("expected 2 rounds, got %d", result.Rounds)
	}
	if result.VerifierRounds != 1 {
		t.Errorf("expected 1 verifier round, got %d", result.VerifierRounds)
	}
	if !result.VerifierConceded {
		t.Error("until should treat CONCEDE as success")
	}
	if result.CompletionReason != "verifier_conceded" {
		t.Fatalf("completion reason: got %q", result.CompletionReason)
	}

	transcript := ReadTranscript(state.TranscriptPath)
	if count := strings.Count(transcript, "## Evaluation "); count != 1 {
		t.Fatalf("expected one evaluation turn, got %d:\n%s", count, transcript)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestPVGRecoverableProverFailureContinuesToVerifier(t *testing.T) {
	compiled := compileTestSpec(t)
	dir := t.TempDir()
	specDir := filepath.Join(dir, "specs", "pvg-test")
	state := NewPVGState("pvg-test", specDir, "test-session-recover")
	state.Ensure()

	exec := &fakeExecutor{
		proverResults: []TurnResult{
			{Role: "prover", Status: StatusContinue, Logs: "pi_failed:1", Error: "pi_failed:1", Recoverable: true},
		},
		verifierResults: []TurnResult{
			{Role: "verifier", Status: StatusConcede, Logs: "Inspected failed prover turn.\n<status>CONCEDE</status>\n"},
		},
	}

	pvg := NewPVG(compiled, exec, state, PVGConfig{Verbose: false})
	result := pvg.Run()

	if result.GameResult != GameSuccess {
		t.Fatalf("expected success, got %s error=%q", result.GameResult, result.Error)
	}
	if result.Rounds != 2 {
		t.Fatalf("rounds: got %d", result.Rounds)
	}
	transcript := ReadTranscript(state.TranscriptPath)
	if !strings.Contains(transcript, "Turn failed; no completed agent report") {
		t.Fatalf("transcript should record recoverable turn error:\n%s", transcript)
	}
	if !strings.Contains(transcript, state.RunnerLogPath) {
		t.Fatalf("transcript should point to runner diagnostics:\n%s", transcript)
	}
}

func TestPVGRecoverableFailureBudget(t *testing.T) {
	compiled := compileTestSpec(t)
	dir := t.TempDir()
	specDir := filepath.Join(dir, "specs", "pvg-test")
	state := NewPVGState("pvg-test", specDir, "test-session-fail-budget")
	state.Ensure()

	recoverable := func(role string) TurnResult {
		return TurnResult{
			Role:        role,
			Status:      StatusContinue,
			Logs:        "pi_failed:1",
			Error:       "pi_failed:1",
			Recoverable: true,
		}
	}
	// The prover keeps failing while the verifier's turns succeed in between.
	var failures []TurnResult
	for range maxRecoverableAgentFailures + 1 {
		failures = append(failures, recoverable("prover"))
	}
	exec := &fakeExecutor{proverResults: failures}

	pvg := NewPVG(compiled, exec, state, PVGConfig{Verbose: false})
	result := pvg.Run()

	if result.GameResult != GameFailure {
		t.Fatalf("expected failure, got %s", result.GameResult)
	}
	if !strings.Contains(result.Error, "agent_failure_budget_exceeded") {
		t.Fatalf("error: got %q", result.Error)
	}
	if result.Rounds != 2*maxRecoverableAgentFailures+1 {
		t.Fatalf("rounds: got %d", result.Rounds)
	}
	// Each retried failure is logged as retry N of the budget; the last one
	// ends the game rather than claiming another retry.
	evidence, err := os.ReadFile(state.EvidencePath)
	if err != nil {
		t.Fatal(err)
	}
	var retries []int
	for _, line := range strings.Split(strings.TrimSpace(string(evidence)), "\n") {
		var event struct {
			Event string `json:"event"`
			Data  struct {
				Consecutive int `json:"consecutive_failures"`
				Max         int `json:"max_failures"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event.Event != "agent_failure_recoverable" {
			continue
		}
		if event.Data.Max != maxRecoverableAgentFailures {
			t.Fatalf("max_failures: got %d", event.Data.Max)
		}
		retries = append(retries, event.Data.Consecutive)
	}
	if !slices.Equal(retries, []int{1, 2, 3}) {
		t.Fatalf("logged retries: got %v", retries)
	}
}

func TestPVGRootVerifierBlock(t *testing.T) {
	blocked := "Spec contradiction.\n<blocked>The spec requires both no network and a live Stripe charge; drop one.</blocked>\n<status>BLOCKED</status>"
	for _, tc := range []struct {
		name       string
		controller bool
		logs       string
		recorded   bool
		want       GameResult
	}{
		{"root verifier with a sentence", true, blocked, true, GameBlocked},
		{"root verifier without a sentence", true, "No reason.\n<status>BLOCKED</status>", true, GameFailure},
		{"child verifier", false, blocked, true, GameFailure},
		{"block not recorded", true, blocked, false, GameFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := NewPVGState("pvg-test", filepath.Join(t.TempDir(), "specs", "pvg-test"), "test-session-block")
			state.Ensure()
			// A game that does not block ends at the next verifier turn.
			exec := &fakeExecutor{verifierResults: []TurnResult{
				{Role: "verifier", Status: ExtractStatus(tc.logs), Logs: tc.logs},
				{Role: "verifier", Error: "boom"},
			}}
			var config PVGConfig // a child session records no blocks
			if tc.controller {
				config.RecordBlock = func(string) string {
					if tc.recorded {
						return "SPEC_UNSATISFIABLE"
					}
					return ""
				}
			}
			result := NewPVG(compileTestSpec(t), exec, state, config).Run()
			if result.GameResult != tc.want {
				t.Fatalf("result = %s, want %s", result.GameResult, tc.want)
			}
			evidence, _ := os.ReadFile(state.EvidencePath)
			logged := strings.Contains(string(evidence), `"event":"goal_blocked"`)
			if tc.want == GameBlocked && (result.BlockedMessage != "The spec requires both no network and a live Stripe charge; drop one." || !logged) {
				t.Fatalf("block message %q, logged %v", result.BlockedMessage, logged)
			}
			if tc.want != GameBlocked && logged {
				t.Fatal("non-block logged goal_blocked")
			}
		})
	}
}

func TestPVGReportsWhetherAProviderErrorEndedTheGame(t *testing.T) {
	provider := TurnResult{Role: "verifier", Error: "overloaded", Recoverable: true, ProviderError: true}
	internal := TurnResult{Role: "verifier", Error: "pi crashed"}
	for _, tc := range []struct {
		name     string
		verifier []TurnResult
		want     string
	}{
		// The provider's own words, not the runtime's failure-budget wrapping.
		{"provider error", []TurnResult{provider, provider, provider, provider}, "overloaded"},
		{"provider error, then a Telos error", []TurnResult{provider, internal}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := NewPVGState("pvg-test", filepath.Join(t.TempDir(), "specs", "pvg-test"), "test-session-provider")
			state.Ensure()
			// The prover succeeds every turn; the verifier's failures still end the game.
			exec := &fakeExecutor{verifierResults: tc.verifier}
			result := NewPVG(compileTestSpec(t), exec, state, PVGConfig{}).Run()
			if result.GameResult != GameFailure || result.ProviderError != tc.want {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}

func TestPVGCallsOnTurnSucceededOnlyForTurnsWithoutErrors(t *testing.T) {
	state := NewPVGState("pvg-test", filepath.Join(t.TempDir(), "specs", "pvg-test"), "test-session-success")
	state.Ensure()
	exec := &fakeExecutor{verifierResults: []TurnResult{{Role: "verifier", Error: "boom"}}}
	successes := 0
	NewPVG(compileTestSpec(t), exec, state, PVGConfig{OnTurnSucceeded: func() { successes++ }}).Run()
	if successes != 1 {
		t.Fatalf("successes = %d, want the one prover turn", successes)
	}
}

func TestPVGUntilFailsWhenReviewBudgetExhausted(t *testing.T) {
	compiled := compileTestSpec(t)
	dir := t.TempDir()
	specDir := filepath.Join(dir, "specs", "pvg-test")
	state := NewPVGState("pvg-test", specDir, "test-session-review-recover")
	state.Ensure()

	exec := &fakeExecutor{
		proverResults: []TurnResult{
			{Role: "prover", Status: StatusContinue, Logs: "Round 1"},
			{Role: "prover", Status: StatusContinue, Logs: "Round 2"},
			{Role: "prover", Status: StatusContinue, Logs: "Round 3"},
		},
		verifierResults: []TurnResult{
			{Role: "verifier", Status: StatusContinue, Logs: "pi_failed:1", Error: "pi_failed:1", Recoverable: true},
			{Role: "verifier", Status: StatusContinue, Logs: "A required endpoint still fails validation."},
			{Role: "verifier", Status: StatusContinue, Logs: "The endpoint still returns the wrong status."},
		},
	}

	pvg := NewPVG(compiled, exec, state, PVGConfig{Until: 2, Verbose: false})
	result := pvg.Run()

	if result.GameResult != GameFailure {
		t.Fatalf("expected failure, got %s error=%q", result.GameResult, result.Error)
	}
	if result.CompletionReason != "review_budget_exhausted" {
		t.Fatalf("completion reason: got %q", result.CompletionReason)
	}
	if result.VerifierRounds != 3 {
		t.Fatalf("verifier attempts: got %d", result.VerifierRounds)
	}
	transcript := ReadTranscript(state.TranscriptPath)
	if count := strings.Count(transcript, "## Evaluation "); count != 3 {
		t.Fatalf("expected one failed and two completed evaluation turns, got %d:\n%s", count, transcript)
	}
}

func TestPVGUntilSecondsFailsWhenDurationExhausted(t *testing.T) {
	compiled := compileTestSpec(t)
	state := NewPVGState("pvg-test", filepath.Join(t.TempDir(), "specs", "pvg-test"), "test-session-duration")
	state.Ensure()

	exec := &fakeExecutor{
		delay: time.Second + 100*time.Millisecond,
		proverResults: []TurnResult{
			{Role: "prover", Status: StatusContinue, Logs: "slow turn"},
		},
	}

	start := time.Now()
	pvg := NewPVG(compiled, exec, state, PVGConfig{UntilSeconds: 1, Verbose: false})
	result := pvg.Run()
	elapsed := time.Since(start)

	if result.GameResult != GameFailure {
		t.Fatalf("expected failure, got %s error=%q", result.GameResult, result.Error)
	}
	if result.CompletionReason != "run_duration_exhausted" {
		t.Fatalf("completion reason: got %q", result.CompletionReason)
	}
	if result.VerifierRounds != 0 {
		t.Fatalf("verifier should not run after duration exhaustion, got %d", result.VerifierRounds)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("deadline did not interrupt running turn quickly enough: %s", elapsed)
	}
}

func TestPVGProverError(t *testing.T) {
	compiled := compileTestSpec(t)
	dir := t.TempDir()
	specDir := filepath.Join(dir, "specs", "pvg-test")
	state := NewPVGState("pvg-test", specDir, "test-session-03")
	state.Ensure()

	exec := &fakeExecutor{
		proverResults: []TurnResult{
			{Role: "prover", Status: StatusContinue, Logs: "error", Error: "pi_failed:1"},
		},
	}

	cfg := PVGConfig{Verbose: false}
	pvg := NewPVG(compiled, exec, state, cfg)
	result := pvg.Run()

	if result.GameResult != GameFailure {
		t.Errorf("expected failure, got %s", result.GameResult)
	}
	if result.Error != "pi_failed:1" {
		t.Errorf("error: got %q", result.Error)
	}
}

func TestPVGBudgetExceeded(t *testing.T) {
	compiled := compileTestSpec(t)
	dir := t.TempDir()
	specDir := filepath.Join(dir, "specs", "pvg-test")
	state := NewPVGState("pvg-test", specDir, "test-session-04")
	state.Ensure()

	maxCost := 1.0
	exec := &fakeExecutor{
		proverResults: []TurnResult{
			{Role: "prover", Status: StatusContinue, Logs: "expensive", Stats: TurnStats{CostUSD: 2.0}},
		},
	}

	cfg := PVGConfig{MaxCostUSD: &maxCost, Verbose: false}
	pvg := NewPVG(compiled, exec, state, cfg)
	result := pvg.Run()

	if result.GameResult != GameFailure {
		t.Errorf("expected failure, got %s", result.GameResult)
	}
	if !strings.Contains(result.Error, "budget exceeded") {
		t.Errorf("error should mention budget: got %q", result.Error)
	}
}

func TestPVGEvidenceFormat(t *testing.T) {
	compiled := compileTestSpec(t)
	dir := t.TempDir()
	specDir := filepath.Join(dir, "specs", "pvg-test")
	state := NewPVGState("pvg-test", specDir, "test-session-05")
	state.Ensure()

	exec := &fakeExecutor{
		proverResults: []TurnResult{
			{Role: "prover", Status: StatusContinue, Logs: "done"},
		},
		verifierResults: []TurnResult{
			{Role: "verifier", Status: StatusConcede, Logs: "ok\n<status>CONCEDE</status>\n"},
		},
	}

	cfg := PVGConfig{Verbose: false, EpochID: 7}
	pvg := NewPVG(compiled, exec, state, cfg)
	pvg.Run()

	// Read evidence JSONL
	data, err := os.ReadFile(state.EvidencePath)
	if err != nil {
		t.Fatalf("read evidence: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 evidence lines, got %d", len(lines))
	}

	// Verify first event is game_start
	var first map[string]interface{}
	json.Unmarshal([]byte(lines[0]), &first)
	if first["event"] != "game_start" {
		t.Errorf("first event should be game_start, got %v", first["event"])
	}
	if first["schema"] != "telos.evidence.v2" {
		t.Errorf("schema: got %v", first["schema"])
	}
	if first["session_id"] != "test-session-05" {
		t.Errorf("session_id: got %v", first["session_id"])
	}
	if first["epoch_id"] != float64(7) {
		t.Errorf("epoch_id: got %v", first["epoch_id"])
	}

	// Verify last event is game_end
	var last map[string]interface{}
	json.Unmarshal([]byte(lines[len(lines)-1]), &last)
	if last["event"] != "game_end" {
		t.Errorf("last event should be game_end, got %v", last["event"])
	}
}

func TestPVGWorkspaceCheckpoint(t *testing.T) {
	compiled := compileTestSpec(t)
	dir := t.TempDir()
	specDir := filepath.Join(dir, "specs", "pvg-test")
	state := NewPVGState("pvg-test", specDir, "test-session-06")
	state.Ensure()

	exec := &fakeExecutor{
		proverResults: []TurnResult{
			{Role: "prover", Status: StatusContinue, Logs: "done"},
		},
		verifierResults: []TurnResult{
			{Role: "verifier", Status: StatusConcede, Logs: "ok\n<status>CONCEDE</status>\n"},
		},
		checkpointOK: true,
	}

	cfg := PVGConfig{Verbose: false}
	pvg := NewPVG(compiled, exec, state, cfg)
	result := pvg.Run()

	if result.WorkspaceCheckpointPath == "" {
		t.Error("expected workspace checkpoint path")
	}
	if _, err := os.Stat(result.WorkspaceCheckpointPath); err != nil {
		t.Errorf("workspace checkpoint file missing: %v", err)
	}
}

func TestPVGLiveEventsReachTranscriptAndEvidence(t *testing.T) {
	compiled := compileTestSpec(t)
	state := NewPVGState("pvg-test", t.TempDir(), "test-session-live-events")
	liveEvents := []LiveAgentEvent{
		{Kind: "progress_update", Text: "Reading app/main.py revealed missing input validation."},
		{Kind: "progress_update", Text: "The input validation tests pass."},
		{Kind: "progress_update", Text: "Input validation is ready."},
	}
	exec := &fakeExecutor{
		onTurn: func(ts *TurnState) {
			for _, event := range liveEvents {
				ts.OnLiveEvent(event)
			}
		},
		proverResults: []TurnResult{
			{Role: "prover", Status: StatusContinue, Logs: "Added input validation.\n<progress_update>Input validation is ready.</progress_update>\n<progress_update>New final handoff.</progress_update>\nRegression evidence: tests/test_validation.py."},
		},
		verifierResults: []TurnResult{
			{Role: "verifier", Status: StatusConcede, Logs: "Accepted the validation change.\n<progress_update>Input validation is ready.</progress_update>\n<status>CONCEDE</status>"},
		},
	}
	if result := NewPVG(compiled, exec, state, PVGConfig{}).Run(); result.GameResult != GameSuccess {
		t.Fatalf("result: %s error=%q", result.GameResult, result.Error)
	}

	transcript := ReadTranscript(state.TranscriptPath)
	for _, event := range liveEvents {
		block := "<" + event.Kind + ">" + event.Text + "</" + event.Kind + ">"
		if count := strings.Count(transcript, block); count != 2 {
			t.Errorf("expected %q in both turns, got %d copies", block, count)
		}
	}
	for _, report := range []string{"Added input validation.", "Accepted the validation change.", "Regression evidence: tests/test_validation.py."} {
		if !strings.Contains(transcript, report) {
			t.Errorf("transcript lost completed report %q", report)
		}
	}
	if count := strings.Count(transcript, "<progress_update>New final handoff.</progress_update>"); count != 1 {
		t.Fatalf("unrecorded final progress must appear once, got %d copies", count)
	}

	data, err := os.ReadFile(state.EvidencePath)
	if err != nil {
		t.Fatalf("read evidence: %v", err)
	}
	progressByRole := make(map[string][]LiveAgentEvent)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var record struct {
			Event string         `json:"event"`
			Role  string         `json:"role"`
			Data  LiveAgentEvent `json:"data"`
		}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode evidence: %v", err)
		}
		if record.Event == "agent_progress" {
			progressByRole[record.Role] = append(progressByRole[record.Role], record.Data)
		}
	}
	for _, role := range []string{"prover", "verifier"} {
		got := progressByRole[role]
		if len(got) != len(liveEvents) {
			t.Fatalf("%s evidence: got %d live events, want %d", role, len(got), len(liveEvents))
		}
		for i, want := range liveEvents {
			if got[i] != want {
				t.Errorf("%s evidence event %d: got %#v, want %#v", role, i, got[i], want)
			}
		}
	}
}

func TestPVGFinalProgressAppearsOncePerTurn(t *testing.T) {
	const report = "The current spec holds."
	for _, mode := range []string{"recorded", "not emitted", "write failed"} {
		t.Run(mode, func(t *testing.T) {
			state := NewPVGState("pvg-test", t.TempDir(), "final-progress")
			body := "<progress_update>\n" + report + "\n</progress_update>"
			stats := TurnStats{Model: "test-model", CostUSD: 0.01}
			exec := &fakeExecutor{
				onTurn: func(ts *TurnState) {
					if mode == "not emitted" {
						return
					}
					if mode == "write failed" {
						if err := os.Rename(state.TranscriptPath, state.TranscriptPath+".saved"); err != nil {
							t.Fatal(err)
						}
						if err := os.Mkdir(state.TranscriptPath, 0o755); err != nil {
							t.Fatal(err)
						}
					}
					ts.OnLiveEvent(LiveAgentEvent{Kind: "progress_update", Text: "\n" + report + "\n"})
					if mode == "write failed" {
						if err := os.Remove(state.TranscriptPath); err != nil {
							t.Fatal(err)
						}
						if err := os.Rename(state.TranscriptPath+".saved", state.TranscriptPath); err != nil {
							t.Fatal(err)
						}
					}
				},
				proverResults:   []TurnResult{{Role: "prover", Status: StatusContinue, Logs: body, Stats: stats}},
				verifierResults: []TurnResult{{Role: "verifier", Status: StatusConcede, Logs: body + "\n<status>CONCEDE</status>", Stats: stats}},
			}
			if result := NewPVG(compileTestSpec(t), exec, state, PVGConfig{}).Run(); result.GameResult != GameSuccess {
				t.Fatalf("unexpected result: %+v", result)
			}
			transcript := ReadTranscript(state.TranscriptPath)
			if strings.Count(transcript, report) != 2 || strings.Count(transcript, "model `test-model`") != 2 {
				t.Fatalf("each turn must retain its report once and completion metadata:\n%s", transcript)
			}
			for _, status := range []string{"CONTINUE", "CONCEDE"} {
				if strings.Count(transcript, "<status>"+status+"</status>") != 1 {
					t.Fatalf("completion verdict missing or duplicated:\n%s", transcript)
				}
			}
			for _, fabricated := range []string{"No assistant text captured", "Turn completed"} {
				if strings.Contains(transcript, fabricated) {
					t.Fatalf("completion manufactured text despite the available report:\n%s", transcript)
				}
			}
		})
	}
}

func TestPVGLargeFailuresStayInRunnerLog(t *testing.T) {
	for _, tc := range []struct {
		name        string
		prefix      string
		recoverable bool
		wantResult  GameResult
	}{
		{"recoverable", "pi_failed:1", true, GameSuccess},
		{"fatal", "pi_failed:1", false, GameFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := NewPVGState("pvg-test", t.TempDir(), "large-failure")
			failure := tc.prefix + "\n" + strings.Repeat("PRIVATE_PROVIDER_ERROR", 65536)
			exec := &fakeExecutor{
				proverResults:   []TurnResult{{Role: "prover", Status: StatusContinue, Logs: failure, Error: failure, Recoverable: tc.recoverable}},
				verifierResults: []TurnResult{{Role: "verifier", Status: StatusConcede, Logs: "Independently checked the existing system."}},
			}
			result := NewPVG(compileTestSpec(t), exec, state, PVGConfig{EpochID: 7}).Run()
			if result.GameResult != tc.wantResult {
				t.Fatalf("result=%s want=%s", result.GameResult, tc.wantResult)
			}
			if tc.wantResult == GameFailure && result.Error != failure {
				t.Fatal("raw failure must remain available to the runtime's recovery policy")
			}
			diagnostics, err := os.ReadFile(state.RunnerLogPath)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Count(string(diagnostics), failure) != 1 || !strings.Contains(string(diagnostics), "epoch=7 turn=0001-prover") {
				t.Fatal("runner log must retain one complete diagnostic with turn provenance")
			}
			transcript := ReadTranscript(state.TranscriptPath)
			if len(transcript) > 5000 || !strings.Contains(transcript, state.RunnerLogPath) || !strings.Contains(transcript, "epoch `7`, turn `0001-prover`") {
				t.Fatalf("transcript must contain a compact diagnostic reference: %d bytes", len(transcript))
			}
			if strings.Contains(transcript, "PRIVATE_PROVIDER_ERROR") || strings.Contains(transcript, tc.prefix) {
				t.Fatal("transcript contains raw failure diagnostics")
			}
			data, err := os.ReadFile(state.EvidencePath)
			if err != nil {
				t.Fatal(err)
			}
			if len(data) > 16000 || strings.Contains(string(data), "PRIVATE_PROVIDER_ERROR") {
				t.Fatalf("evidence grew with the error payload: %d bytes", len(data))
			}
			for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				var record struct {
					Data map[string]any `json:"data"`
				}
				if err := json.Unmarshal([]byte(line), &record); err != nil {
					t.Fatal(err)
				}
				for _, field := range []string{"error", "logs_tail"} {
					if _, exists := record.Data[field]; exists {
						t.Fatalf("evidence must not copy raw diagnostics or assistant output into %s", field)
					}
				}
			}
		})
	}
}

func TestPVGDoesNotReferenceUnwrittenDiagnostics(t *testing.T) {
	state := NewPVGState("pvg-test", t.TempDir(), "unwritten-diagnostic")
	pvg := NewPVG(compileTestSpec(t), &fakeExecutor{}, state, PVGConfig{})
	if pvg.logDiagnostic("prover", "first failure", "") == "" {
		t.Fatal("expected initial diagnostic write to succeed")
	}
	state.RunnerLogPath = state.SpecDir
	if pvg.logDiagnostic("prover", "second failure", "") != "" || pvg.diagnosticLogPath != "" || pvg.diagnosticError != "" {
		t.Fatal("a failed write must not publish a stale diagnostic reference")
	}
}

func TestPVGRecordsTerminalErrorAfterRecoverableFailure(t *testing.T) {
	maxCost := 0.5
	for _, tc := range []struct {
		name   string
		config PVGConfig
		want   string
	}{
		{"cost budget", PVGConfig{MaxCostUSD: &maxCost}, "budget exceeded: $1.00 >= cap $0.50"},
		{"review budget", PVGConfig{Until: 1}, "review_budget_exhausted: verifier did not concede within 1 review cycles"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := NewPVGState("pvg-test", t.TempDir(), "terminal-error")
			exec := &fakeExecutor{
				proverResults: []TurnResult{{Role: "prover", Error: "pi_failed:1", Recoverable: true}},
				verifierResults: []TurnResult{{Role: "verifier", Status: StatusContinue,
					Logs: "Review complete", Stats: TurnStats{CostUSD: 1}}},
			}
			result := NewPVG(compileTestSpec(t), exec, state, tc.config).Run()
			if result.GameResult != GameFailure || result.Error != tc.want {
				t.Fatalf("unexpected result: %+v", result)
			}
			diagnostics, err := os.ReadFile(state.RunnerLogPath)
			if err != nil {
				t.Fatal(err)
			}
			for _, failure := range []string{"pi_failed:1", tc.want} {
				if strings.Count(string(diagnostics), failure) != 1 {
					t.Fatalf("runner log must retain each distinct failure once: %s", diagnostics)
				}
				for _, path := range []string{state.TranscriptPath, state.EvidencePath} {
					data, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					if strings.Contains(string(data), failure) {
						t.Fatalf("raw failure leaked into %s", path)
					}
				}
			}
		})
	}
}

func TestPVGFailurePreservesCapturedStderr(t *testing.T) {
	state := NewPVGState("pvg-test", t.TempDir(), "captured-stderr")
	captured := "STDERR_DETAILS\n" + strings.Repeat("provider startup detail\n", 1000)
	exec := &fakeExecutor{proverResults: []TurnResult{{
		Role: "prover", Status: StatusContinue, Error: "agent_no_output", Logs: captured,
	}}}
	result := NewPVG(compileTestSpec(t), exec, state, PVGConfig{}).Run()
	if result.GameResult != GameFailure {
		t.Fatalf("result: %s", result.GameResult)
	}
	diagnostics, err := os.ReadFile(state.RunnerLogPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(diagnostics), captured) {
		t.Fatal("captured stderr must survive in runner.log without truncation")
	}
	for _, path := range []string{state.TranscriptPath, state.EvidencePath} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "STDERR_DETAILS") {
			t.Fatalf("captured stderr leaked into %s", path)
		}
	}
}

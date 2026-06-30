package game

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/platform"
	"github.com/telos-org/telos/internal/spec"
)

// fakeExecutor simulates prover/verifier turns.
type fakeExecutor struct {
	proverResults   []TurnResult
	verifierResults []TurnResult
	proverIdx       int
	verifierIdx     int
	workspaceText   string
	checkpointOK    bool
	seenBudgets     []TurnBudget
	seenProtocols   []string
}

func (f *fakeExecutor) ExecuteTurn(task string, ts *TurnState) TurnResult {
	role := ""
	if ts != nil {
		role = ts.Role
		f.seenBudgets = append(f.seenBudgets, ts.Budget)
		f.seenProtocols = append(f.seenProtocols, ts.ProtocolMode)
	}
	if role == RoleProver {
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

func (f *fakeExecutor) WorkspaceSnapshot() platform.WorkspaceSnapshot {
	return platform.WorkspaceSnapshot{Raw: f.workspaceText}
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
	os.WriteFile(specPath, []byte("---\nversion: v0\nname: pvg-test\nplatform: local\n---\n# Test\n\nTest body."), 0o644)
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

func TestPVGWritesObjectiveLedger(t *testing.T) {
	compiled := compileTestSpec(t)
	dir := t.TempDir()
	specDir := filepath.Join(dir, "specs", "pvg-test")
	state := NewPVGState("pvg-test", specDir, "test-session-ledger")
	state.Ensure()

	exec := &fakeExecutor{
		proverResults: []TurnResult{
			{Role: "prover", Status: StatusContinue, Logs: "Changed the artifact.\n\n<progress_update>Implemented baseline.</progress_update>"},
			{Role: "prover", Status: StatusContinue, Logs: "Repaired the artifact.\n\n<progress_update>Fixed missing test coverage.</progress_update>"},
		},
		verifierResults: []TurnResult{
			{Role: "verifier", Status: StatusContinue, Logs: "Blocking finding: missing test coverage.\n\n<progress_update>Found missing test coverage.</progress_update>\n<status>CONTINUE</status>"},
			{Role: "verifier", Status: StatusConcede, Logs: "Looks good.\n\n<progress_update>All findings resolved.</progress_update>\n<status>CONCEDE</status>"},
		},
	}

	pvg := NewPVG(compiled, exec, state, PVGConfig{Verbose: false})
	result := pvg.Run()

	if result.GameResult != GameSuccess {
		t.Fatalf("expected success, got %s error=%q", result.GameResult, result.Error)
	}
	ledger, err := readObjectiveLedger(state.LedgerPath)
	if err != nil {
		t.Fatalf("readObjectiveLedger: %v", err)
	}
	if ledger.State != ObjectiveStateFinalize {
		t.Fatalf("ledger state: got %q", ledger.State)
	}
	if ledger.LastImplementation != "Fixed missing test coverage." {
		t.Fatalf("last implementation: got %q", ledger.LastImplementation)
	}
	if len(ledger.ResolvedFindings) == 0 || !strings.Contains(ledger.ResolvedFindings[0], "missing test coverage") {
		t.Fatalf("resolved findings: %#v", ledger.ResolvedFindings)
	}
	if len(ledger.Turns) != 4 {
		t.Fatalf("turn count: got %d", len(ledger.Turns))
	}
}

func TestNewPVGPreservesExistingObjectiveLedger(t *testing.T) {
	compiled := compileTestSpec(t)
	dir := t.TempDir()
	specDir := filepath.Join(dir, "specs", "pvg-test")
	state := NewPVGState("pvg-test", specDir, "test-session-preserve-ledger")
	state.Ensure()
	existing := newObjectiveLedger(state, compiled.SpecText)
	existing.State = ObjectiveStateRepair
	existing.LastImplementation = "Implemented baseline."
	existing.LastEvaluation = "Missing test coverage."
	existing.OpenFindings = []string{"missing test coverage"}
	existing.Turns = []ObjectiveTurn{{
		RoundNum:       1,
		Role:           RoleVerifier,
		Status:         StatusContinue,
		StateAfter:     ObjectiveStateRepair,
		ProgressUpdate: "Missing test coverage.",
	}}
	if err := writeObjectiveLedger(state.LedgerPath, existing); err != nil {
		t.Fatalf("writeObjectiveLedger: %v", err)
	}

	NewPVG(compiled, nil, state, PVGConfig{})

	ledger, err := readObjectiveLedger(state.LedgerPath)
	if err != nil {
		t.Fatalf("readObjectiveLedger: %v", err)
	}
	if ledger.State != ObjectiveStateRepair {
		t.Fatalf("ledger state: got %q", ledger.State)
	}
	if ledger.LastImplementation != existing.LastImplementation {
		t.Fatalf("last implementation: got %q", ledger.LastImplementation)
	}
	if len(ledger.OpenFindings) != 1 || ledger.OpenFindings[0] != "missing test coverage" {
		t.Fatalf("open findings: %#v", ledger.OpenFindings)
	}
	if len(ledger.Turns) != 1 {
		t.Fatalf("turn count: got %d", len(ledger.Turns))
	}
}

func TestPVGLedgerFallsBackWhenFindingsBlockIsEmpty(t *testing.T) {
	compiled := compileTestSpec(t)
	dir := t.TempDir()
	specDir := filepath.Join(dir, "specs", "pvg-test")
	state := NewPVGState("pvg-test", specDir, "test-session-empty-findings-ledger")
	state.Ensure()
	pvg := NewPVG(compiled, nil, state, PVGConfig{})

	pvg.updateObjectiveLedger(1, RoleVerifier, TurnResult{
		Role:   RoleVerifier,
		Status: StatusContinue,
		Logs:   "Blocking finding: missing test coverage.\n\n<findings></findings>\n<status>CONTINUE</status>",
	}, ObjectiveStateRepair)

	ledger, err := readObjectiveLedger(state.LedgerPath)
	if err != nil {
		t.Fatalf("readObjectiveLedger: %v", err)
	}
	if len(ledger.OpenFindings) != 1 || !strings.Contains(ledger.OpenFindings[0], "missing test coverage") {
		t.Fatalf("open findings: %#v", ledger.OpenFindings)
	}
}

func TestPVGReviewModeLedgerReturnsToImplement(t *testing.T) {
	compiled := compileTestSpec(t)
	dir := t.TempDir()
	specDir := filepath.Join(dir, "specs", "pvg-test")
	state := NewPVGState("pvg-test", specDir, "test-session-review-ledger")
	state.Ensure()

	exec := &fakeExecutor{
		proverResults: []TurnResult{
			{Role: "prover", Status: StatusContinue, Logs: "Round 1\n\n<progress_update>Implemented baseline.</progress_update>"},
			{Role: "prover", Status: StatusContinue, Logs: "Round 2\n\n<progress_update>Polished.</progress_update>"},
		},
		verifierResults: []TurnResult{
			{Role: "verifier", Status: StatusContinue, Logs: "<review>\ncriteria,score\nFunctional correctness,7.0/10\n</review>\n\n<summary>Keep going.</summary>\n"},
			{Role: "verifier", Status: StatusContinue, Logs: "<review>\ncriteria,score\nFunctional correctness,8.0/10\n</review>\n\n<summary>Better.</summary>\n"},
		},
	}

	pvg := NewPVG(compiled, exec, state, PVGConfig{Until: 2, Verbose: false})
	if result := pvg.Run(); result.GameResult != GameSuccess {
		t.Fatalf("expected success, got %s error=%q", result.GameResult, result.Error)
	}

	ledger, err := readObjectiveLedger(state.LedgerPath)
	if err != nil {
		t.Fatalf("readObjectiveLedger: %v", err)
	}
	// Review-mode verifier turns route back to implement, never repair. The ledger
	// must record the state the machine computed rather than its own guess.
	if len(ledger.Turns) != 4 {
		t.Fatalf("turn count: got %d (%#v)", len(ledger.Turns), ledger.Turns)
	}
	if ledger.Turns[1].Role != "verifier" || ledger.Turns[1].StateAfter != ObjectiveStateImplement {
		t.Fatalf("first review verifier turn: got role=%q state=%q, want verifier/implement", ledger.Turns[1].Role, ledger.Turns[1].StateAfter)
	}
	for i, turn := range ledger.Turns {
		if turn.StateAfter == ObjectiveStateRepair {
			t.Fatalf("review mode must not enter repair, turn %d did: %#v", i, turn)
		}
	}
	if ledger.State != ObjectiveStateFinalize {
		t.Fatalf("final ledger state: got %q", ledger.State)
	}
}

func TestPVGStopsAtMaxRounds(t *testing.T) {
	compiled := compileTestSpec(t)
	dir := t.TempDir()
	specDir := filepath.Join(dir, "specs", "pvg-test")
	state := NewPVGState("pvg-test", specDir, "test-session-max-rounds")
	state.Ensure()

	exec := &fakeExecutor{
		proverResults: []TurnResult{
			{Role: "prover", Status: StatusContinue, Logs: "still working"},
		},
		verifierResults: []TurnResult{
			{Role: "verifier", Status: StatusContinue, Logs: "not yet\n<status>CONTINUE</status>\n"},
		},
	}

	pvg := NewPVG(compiled, exec, state, PVGConfig{MaxRounds: 2})
	result := pvg.Run()

	if result.GameResult != GameFailure {
		t.Fatalf("expected failure from budget exhaustion, got %s", result.GameResult)
	}
	if result.CompletionReason != "runtime_budget_exhausted" {
		t.Fatalf("completion reason: got %q", result.CompletionReason)
	}
	if result.Error != "runtime_budget_exhausted:max_rounds" {
		t.Fatalf("error: got %q", result.Error)
	}
}

func TestPVGDoesNotStartTurnWhenRemainingDurationCannotFitAgentTimeout(t *testing.T) {
	compiled := compileTestSpec(t)
	dir := t.TempDir()
	specDir := filepath.Join(dir, "specs", "pvg-test")
	state := NewPVGState("pvg-test", specDir, "test-session-duration-reserve")
	state.Ensure()

	exec := &fakeExecutor{
		proverResults: []TurnResult{
			{Role: "prover", Status: StatusContinue, Logs: "should not run"},
		},
	}

	pvg := NewPVG(compiled, exec, state, PVGConfig{MaxDurationSec: 1, AgentTimeoutSec: 2})
	result := pvg.Run()

	if result.GameResult != GameFailure {
		t.Fatalf("expected failure from duration budget, got %s", result.GameResult)
	}
	if result.Error != "runtime_budget_exhausted:max_duration" {
		t.Fatalf("error: got %q", result.Error)
	}
	if result.Rounds != 0 || exec.proverIdx != 0 {
		t.Fatalf("turn should not start: rounds=%d proverIdx=%d", result.Rounds, exec.proverIdx)
	}

	data, err := os.ReadFile(state.EvidencePath)
	if err != nil {
		t.Fatalf("read evidence: %v", err)
	}
	if !strings.Contains(string(data), `"budget":"max_duration"`) ||
		!strings.Contains(string(data), `"required_turn_sec":2`) {
		t.Fatalf("evidence missing duration reservation details:\n%s", data)
	}
}

func TestPVGStopsAtMaxInputTokens(t *testing.T) {
	compiled := compileTestSpec(t)
	dir := t.TempDir()
	specDir := filepath.Join(dir, "specs", "pvg-test")
	state := NewPVGState("pvg-test", specDir, "test-session-max-input-tokens")
	state.Ensure()

	exec := &fakeExecutor{
		proverResults: []TurnResult{
			{Role: "prover", Status: StatusContinue, Logs: "used context", Stats: TurnStats{InputTokens: 11}},
		},
	}

	pvg := NewPVG(compiled, exec, state, PVGConfig{MaxInputTokens: 10})
	result := pvg.Run()

	if result.GameResult != GameFailure {
		t.Fatalf("expected failure from input token budget, got %s", result.GameResult)
	}
	if result.CompletionReason != "runtime_budget_exhausted" {
		t.Fatalf("completion reason: got %q", result.CompletionReason)
	}
	if result.Error != "runtime_budget_exhausted:max_input_tokens" {
		t.Fatalf("error: got %q", result.Error)
	}
	if result.VerifierRounds != 0 {
		t.Fatalf("verifier should not run after token budget exhaustion, got %d", result.VerifierRounds)
	}
}

func TestPVGStopsAtMaxOutputTokens(t *testing.T) {
	compiled := compileTestSpec(t)
	dir := t.TempDir()
	specDir := filepath.Join(dir, "specs", "pvg-test")
	state := NewPVGState("pvg-test", specDir, "test-session-max-output-tokens")
	state.Ensure()

	exec := &fakeExecutor{
		proverResults: []TurnResult{
			{Role: "prover", Status: StatusContinue, Logs: "generated output", Stats: TurnStats{OutputTokens: 12}},
		},
	}

	pvg := NewPVG(compiled, exec, state, PVGConfig{MaxOutputTokens: 10})
	result := pvg.Run()

	if result.GameResult != GameFailure {
		t.Fatalf("expected failure from output token budget, got %s", result.GameResult)
	}
	if result.Error != "runtime_budget_exhausted:max_output_tokens" {
		t.Fatalf("error: got %q", result.Error)
	}
	if result.VerifierRounds != 0 {
		t.Fatalf("verifier should not run after token budget exhaustion, got %d", result.VerifierRounds)
	}
}

func TestPVGPassesRemainingBudgetToExecutorTurns(t *testing.T) {
	compiled := compileTestSpec(t)
	dir := t.TempDir()
	specDir := filepath.Join(dir, "specs", "pvg-test")
	state := NewPVGState("pvg-test", specDir, "test-session-turn-budget")
	state.Ensure()

	maxCost := 5.0
	exec := &fakeExecutor{
		proverResults: []TurnResult{
			{Role: "prover", Status: StatusContinue, Logs: "used some budget", Stats: TurnStats{CostUSD: 1.5, InputTokens: 100, OutputTokens: 25}},
		},
		verifierResults: []TurnResult{
			{Role: "verifier", Status: StatusConcede, Logs: "ok\n<status>CONCEDE</status>\n"},
		},
	}

	pvg := NewPVG(compiled, exec, state, PVGConfig{
		MaxCostUSD:      &maxCost,
		MaxDurationSec:  30,
		MaxInputTokens:  1000,
		MaxOutputTokens: 200,
		MaxToolLoops:    7,
		AgentTimeoutSec: 5,
	})
	result := pvg.Run()

	if result.GameResult != GameSuccess {
		t.Fatalf("expected success, got %s err=%s", result.GameResult, result.Error)
	}
	if len(exec.seenBudgets) != 2 {
		t.Fatalf("seen budgets: %#v", exec.seenBudgets)
	}
	first := exec.seenBudgets[0]
	if first.RemainingCostUSD == nil || *first.RemainingCostUSD != 5.0 || first.RemainingInputTokens != 1000 || first.RemainingOutputTokens != 200 || first.MaxToolLoops != 7 {
		t.Fatalf("first budget: %#v", first)
	}
	if first.MaxDurationSec != 30 || first.AgentTimeoutSec != 5 || first.RemainingDurationSec <= 0 || first.RemainingDurationSec > 30 {
		t.Fatalf("first duration budget: %#v", first)
	}
	second := exec.seenBudgets[1]
	if second.RemainingCostUSD == nil || *second.RemainingCostUSD != 3.5 || second.RemainingInputTokens != 900 || second.RemainingOutputTokens != 175 || second.MaxToolLoops != 7 {
		t.Fatalf("second budget: %#v", second)
	}
	if second.MaxDurationSec != 30 || second.AgentTimeoutSec != 5 || second.RemainingDurationSec <= 0 || second.RemainingDurationSec > first.RemainingDurationSec {
		t.Fatalf("second duration budget: %#v first=%#v", second, first)
	}
}

func TestPVGUntilRunsExactReviewCycles(t *testing.T) {
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
			{Role: "verifier", Status: StatusConcede, Logs: "<review>\ncriteria,score\nFunctional correctness,7.0/10\n</review>\n\n<summary>Keep going.</summary>\n\n<status>CONCEDE</status>\n"},
			{Role: "verifier", Status: StatusConcede, Logs: "<review>\ncriteria,score\nFunctional correctness,8.0/10\n</review>\n\n<summary>Better.</summary>\n\n<status>CONCEDE</status>\n"},
		},
	}

	cfg := PVGConfig{Until: 2, Verbose: false}
	pvg := NewPVG(compiled, exec, state, cfg)
	result := pvg.Run()

	if result.GameResult != GameSuccess {
		t.Errorf("expected success, got %s", result.GameResult)
	}
	if result.Rounds != 4 {
		t.Errorf("expected 4 rounds, got %d", result.Rounds)
	}
	if result.VerifierRounds != 2 {
		t.Errorf("expected 2 verifier rounds, got %d", result.VerifierRounds)
	}
	if result.VerifierConceded {
		t.Error("fixed review mode should not treat CONCEDE as loop control")
	}
	if result.CompletionReason != "review_cycles_complete" {
		t.Fatalf("completion reason: got %q", result.CompletionReason)
	}
	if len(exec.seenProtocols) != 4 || exec.seenProtocols[1] != "review" || exec.seenProtocols[3] != "review" {
		t.Fatalf("review verifier turns should carry explicit review protocol: %#v", exec.seenProtocols)
	}

	transcript := ReadTranscript(state.TranscriptPath)
	if strings.Contains(transcript, "<status>") {
		t.Fatalf("fixed review transcript should not contain synthetic status tags:\n%s", transcript)
	}
	if count := strings.Count(transcript, "<review>"); count != 2 {
		t.Fatalf("expected two review blocks, got %d:\n%s", count, transcript)
	}
}

func TestPVGRecoverableProverFailureRetriesProver(t *testing.T) {
	compiled := compileTestSpec(t)
	dir := t.TempDir()
	specDir := filepath.Join(dir, "specs", "pvg-test")
	state := NewPVGState("pvg-test", specDir, "test-session-recover")
	state.Ensure()

	exec := &fakeExecutor{
		proverResults: []TurnResult{
			{Role: "prover", Status: StatusContinue, Logs: "provider_rate_limited: retry later", Error: "provider_rate_limited: retry later", Recoverable: true},
			{Role: "prover", Status: StatusContinue, Logs: "implemented after retry"},
		},
		verifierResults: []TurnResult{
			{Role: "verifier", Status: StatusConcede, Logs: "LGTM\n<status>CONCEDE</status>\n"},
		},
	}

	pvg := NewPVG(compiled, exec, state, PVGConfig{Verbose: false})
	result := pvg.Run()

	if result.GameResult != GameSuccess {
		t.Fatalf("expected success after prover retry, got %s error=%q", result.GameResult, result.Error)
	}
	if result.ProverRounds != 2 || result.VerifierRounds != 1 {
		t.Fatalf("rounds: prover=%d verifier=%d", result.ProverRounds, result.VerifierRounds)
	}
	if result.Rounds != 3 {
		t.Fatalf("rounds: got %d", result.Rounds)
	}
	transcript := ReadTranscript(state.TranscriptPath)
	if !strings.Contains(transcript, "Turn ended with runtime error") {
		t.Fatalf("transcript should record recoverable turn error:\n%s", transcript)
	}
	if !strings.Contains(transcript, filepath.Join(state.TurnsDir(), "0001-prover", "session.jsonl")) {
		t.Fatalf("transcript should point to agent session:\n%s", transcript)
	}
	evidenceData, err := os.ReadFile(state.EvidencePath)
	if err != nil {
		t.Fatalf("read evidence: %v", err)
	}
	if strings.Count(string(evidenceData), `"error_code":"provider_rate_limited"`) < 2 {
		t.Fatalf("evidence should carry typed error code on agent_complete and recoverable failure:\n%s", evidenceData)
	}
	if strings.Contains(string(evidenceData), `"error_code":"no_successful_implementation"`) {
		t.Fatalf("retrying prover failure should not emit no-successful-implementation:\n%s", evidenceData)
	}
}

func TestPVGRecoverableVerifierFailureRetriesVerifier(t *testing.T) {
	compiled := compileTestSpec(t)
	dir := t.TempDir()
	specDir := filepath.Join(dir, "specs", "pvg-test")
	state := NewPVGState("pvg-test", specDir, "test-session-verifier-retry")
	state.Ensure()

	exec := &fakeExecutor{
		proverResults: []TurnResult{
			{Role: "prover", Status: StatusContinue, Logs: "implemented"},
		},
		verifierResults: []TurnResult{
			{Role: "verifier", Status: StatusContinue, Logs: "provider_unavailable: retry", Error: "provider_unavailable: retry", Recoverable: true},
			{Role: "verifier", Status: StatusConcede, Logs: "LGTM\n<status>CONCEDE</status>\n"},
		},
	}

	pvg := NewPVG(compiled, exec, state, PVGConfig{Verbose: false})
	result := pvg.Run()

	if result.GameResult != GameSuccess {
		t.Fatalf("expected success after verifier retry, got %s error=%q", result.GameResult, result.Error)
	}
	if result.ProverRounds != 1 || result.VerifierRounds != 2 {
		t.Fatalf("rounds: prover=%d verifier=%d", result.ProverRounds, result.VerifierRounds)
	}
}

func TestPVGSuccessfulProverStillSucceedsOnVerifierConcession(t *testing.T) {
	compiled := compileTestSpec(t)
	dir := t.TempDir()
	specDir := filepath.Join(dir, "specs", "pvg-test")
	state := NewPVGState("pvg-test", specDir, "test-session-successful-prover")
	state.Ensure()

	exec := &fakeExecutor{
		proverResults: []TurnResult{
			{Role: "prover", Status: StatusContinue, Logs: "implemented"},
		},
		verifierResults: []TurnResult{
			{Role: "verifier", Status: StatusConcede, Logs: "LGTM\n<status>CONCEDE</status>\n"},
		},
	}

	pvg := NewPVG(compiled, exec, state, PVGConfig{Verbose: false})
	result := pvg.Run()

	if result.GameResult != GameSuccess {
		t.Fatalf("expected success, got %s error=%q", result.GameResult, result.Error)
	}
	if !result.VerifierConceded {
		t.Fatal("expected verifier concession to be recorded")
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
			Logs:        "provider_unavailable:temporary gateway error",
			Error:       "provider_unavailable:temporary gateway error",
			Recoverable: true,
		}
	}
	exec := &fakeExecutor{
		proverResults: []TurnResult{
			recoverable("prover"),
			recoverable("prover"),
			recoverable("prover"),
			recoverable("prover"),
		},
	}

	pvg := NewPVG(compiled, exec, state, PVGConfig{Verbose: false})
	result := pvg.Run()

	if result.GameResult != GameFailure {
		t.Fatalf("expected failure, got %s", result.GameResult)
	}
	if !strings.Contains(result.Error, "agent_failure_budget_exceeded") {
		t.Fatalf("error: got %q", result.Error)
	}
	if result.Rounds != maxRecoverableAgentFailures+1 {
		t.Fatalf("rounds: got %d", result.Rounds)
	}
	if result.VerifierRounds != 0 {
		t.Fatalf("verifier should not run before prover failure budget is exhausted, got %d", result.VerifierRounds)
	}
}

func TestPVGUntilDoesNotCountFailedVerifierReview(t *testing.T) {
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
			{Role: "verifier", Status: StatusContinue, Logs: "provider_unavailable:temporary gateway error", Error: "provider_unavailable:temporary gateway error", Recoverable: true},
			{Role: "verifier", Status: StatusContinue, Logs: "<review>\ncriteria,score\nFunctional correctness,7.0/10\n</review>\n\n<summary>Keep going.</summary>\n"},
			{Role: "verifier", Status: StatusContinue, Logs: "<review>\ncriteria,score\nFunctional correctness,8.0/10\n</review>\n\n<summary>Better.</summary>\n"},
		},
	}

	pvg := NewPVG(compiled, exec, state, PVGConfig{Until: 2, Verbose: false})
	result := pvg.Run()

	if result.GameResult != GameSuccess {
		t.Fatalf("expected success, got %s error=%q", result.GameResult, result.Error)
	}
	if result.ProverRounds != 2 {
		t.Fatalf("prover attempts: got %d", result.ProverRounds)
	}
	if result.VerifierRounds != 3 {
		t.Fatalf("verifier attempts: got %d", result.VerifierRounds)
	}
	transcript := ReadTranscript(state.TranscriptPath)
	if count := strings.Count(transcript, "<review>"); count != 2 {
		t.Fatalf("expected two successful review blocks, got %d:\n%s", count, transcript)
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
			{Role: "prover", Status: StatusContinue, Logs: "error", Error: "provider_unavailable:temporary gateway error"},
		},
	}

	cfg := PVGConfig{Verbose: false}
	pvg := NewPVG(compiled, exec, state, cfg)
	result := pvg.Run()

	if result.GameResult != GameFailure {
		t.Errorf("expected failure, got %s", result.GameResult)
	}
	if result.Error != "provider_unavailable:temporary gateway error" {
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
	if result.CompletionReason != "runtime_budget_exhausted" {
		t.Fatalf("completion reason: got %q", result.CompletionReason)
	}
	if result.Error != "runtime_budget_exhausted:max_cost_usd" {
		t.Fatalf("error: got %q", result.Error)
	}
	data, err := os.ReadFile(state.EvidencePath)
	if err != nil {
		t.Fatalf("read evidence: %v", err)
	}
	if !strings.Contains(string(data), `"budget":"max_cost_usd"`) {
		t.Fatalf("evidence missing cost budget kind:\n%s", data)
	}
}

func TestPVGCostCapUnavailableBYOLogsWarningOnly(t *testing.T) {
	compiled := compileTestSpec(t)
	dir := t.TempDir()
	specDir := filepath.Join(dir, "specs", "pvg-test")
	state := NewPVGState("pvg-test", specDir, "test-session-cost-unavailable")
	state.Ensure()

	maxCost := 1.0
	exec := &fakeExecutor{
		proverResults: []TurnResult{
			{Role: "prover", Status: StatusContinue, Logs: "cost unknown", Stats: TurnStats{CostUnavailable: true}},
		},
		verifierResults: []TurnResult{
			{Role: "verifier", Status: StatusConcede, Logs: "LGTM\n<status>CONCEDE</status>\n", Stats: TurnStats{CostUnavailable: true}},
		},
	}

	pvg := NewPVG(compiled, exec, state, PVGConfig{MaxCostUSD: &maxCost})
	result := pvg.Run()

	if result.GameResult != GameSuccess {
		t.Fatalf("BYO cost-unavailable run should not cost-fail, got %s error=%q", result.GameResult, result.Error)
	}
	data, err := os.ReadFile(state.EvidencePath)
	if err != nil {
		t.Fatalf("read evidence: %v", err)
	}
	if got := strings.Count(string(data), `"event":"cost_cap_unenforceable"`); got != 1 {
		t.Fatalf("cost cap warning events: got %d\n%s", got, data)
	}
	if strings.Contains(string(data), `"budget":"max_cost_usd_cost_unavailable"`) {
		t.Fatalf("BYO cost-unavailable run should not emit terminal budget exhaustion:\n%s", data)
	}
}

func TestPVGCostCapUnavailableManagedFailsClosed(t *testing.T) {
	compiled := compileTestSpec(t)
	dir := t.TempDir()
	specDir := filepath.Join(dir, "specs", "pvg-test")
	state := NewPVGState("pvg-test", specDir, "test-session-cost-unavailable-managed")
	state.Ensure()

	maxCost := 1.0
	exec := &fakeExecutor{
		proverResults: []TurnResult{
			{Role: "prover", Status: StatusContinue, Logs: "cost unknown", Stats: TurnStats{CostUnavailable: true}},
		},
		verifierResults: []TurnResult{
			{Role: "verifier", Status: StatusConcede, Logs: "LGTM\n<status>CONCEDE</status>\n", Stats: TurnStats{CostUnavailable: true}},
		},
	}

	pvg := NewPVG(compiled, exec, state, PVGConfig{MaxCostUSD: &maxCost, CostHardLimit: true})
	result := pvg.Run()

	if result.GameResult != GameFailure {
		t.Fatalf("cost-unavailable run should fail closed, got %s error=%q", result.GameResult, result.Error)
	}
	if result.Error != "runtime_budget_exhausted:max_cost_usd_cost_unavailable" {
		t.Fatalf("error: got %q", result.Error)
	}
	data, err := os.ReadFile(state.EvidencePath)
	if err != nil {
		t.Fatalf("read evidence: %v", err)
	}
	if got := strings.Count(string(data), `"event":"cost_cap_unenforceable"`); got != 1 {
		t.Fatalf("cost cap warning events: got %d\n%s", got, data)
	}
	if !strings.Contains(string(data), `"budget":"max_cost_usd_cost_unavailable"`) {
		t.Fatalf("cost-unavailable run should emit terminal budget exhaustion:\n%s", data)
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

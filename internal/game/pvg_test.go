package game

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
}

func (f *fakeExecutor) ExecuteTurn(task string, role string, ts *TurnState) TurnResult {
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

func (f *fakeExecutor) WorkspaceState() string {
	return f.workspaceText
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

	transcript := ReadTranscript(state.TranscriptPath)
	if strings.Contains(transcript, "<status>") {
		t.Fatalf("fixed review transcript should not contain synthetic status tags:\n%s", transcript)
	}
	if count := strings.Count(transcript, "<review>"); count != 2 {
		t.Fatalf("expected two review blocks, got %d:\n%s", count, transcript)
	}
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
	if !strings.Contains(transcript, "Turn ended with runtime error") {
		t.Fatalf("transcript should record recoverable turn error:\n%s", transcript)
	}
	if !strings.Contains(transcript, filepath.Join(state.TurnsDir(), "0001-prover", "pi-session.jsonl")) {
		t.Fatalf("transcript should point to Pi session:\n%s", transcript)
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
	exec := &fakeExecutor{
		proverResults: []TurnResult{
			recoverable("prover"),
			recoverable("prover"),
		},
		verifierResults: []TurnResult{
			recoverable("verifier"),
			recoverable("verifier"),
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
			{Role: "verifier", Status: StatusContinue, Logs: "pi_failed:1", Error: "pi_failed:1", Recoverable: true},
			{Role: "verifier", Status: StatusContinue, Logs: "<review>\ncriteria,score\nFunctional correctness,7.0/10\n</review>\n\n<summary>Keep going.</summary>\n"},
			{Role: "verifier", Status: StatusContinue, Logs: "<review>\ncriteria,score\nFunctional correctness,8.0/10\n</review>\n\n<summary>Better.</summary>\n"},
		},
	}

	pvg := NewPVG(compiled, exec, state, PVGConfig{Until: 2, Verbose: false})
	result := pvg.Run()

	if result.GameResult != GameSuccess {
		t.Fatalf("expected success, got %s error=%q", result.GameResult, result.Error)
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

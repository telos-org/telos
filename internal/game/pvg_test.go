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

	cfg := PVGConfig{MaxRounds: 10, Verbose: false}
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

func TestPVGMaxRoundsTimeout(t *testing.T) {
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
			{Role: "verifier", Status: StatusContinue, Logs: "Issues found\n<status>CONTINUE</status>\n"},
			{Role: "verifier", Status: StatusContinue, Logs: "Still issues\n<status>CONTINUE</status>\n"},
		},
	}

	cfg := PVGConfig{MaxRounds: 4, Verbose: false}
	pvg := NewPVG(compiled, exec, state, cfg)
	result := pvg.Run()

	if result.GameResult != GameTimeout {
		t.Errorf("expected timeout, got %s", result.GameResult)
	}
	if result.Rounds != 4 {
		t.Errorf("expected 4 rounds, got %d", result.Rounds)
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

	cfg := PVGConfig{MaxRounds: 10, Verbose: false}
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

	cfg := PVGConfig{MaxRounds: 10, MaxCostUSD: &maxCost, Verbose: false}
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

	cfg := PVGConfig{MaxRounds: 10, Verbose: false, EpochID: 7}
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

	cfg := PVGConfig{MaxRounds: 10, Verbose: false}
	pvg := NewPVG(compiled, exec, state, cfg)
	result := pvg.Run()

	if result.WorkspaceCheckpointPath == "" {
		t.Error("expected workspace checkpoint path")
	}
	if _, err := os.Stat(result.WorkspaceCheckpointPath); err != nil {
		t.Errorf("workspace checkpoint file missing: %v", err)
	}
}

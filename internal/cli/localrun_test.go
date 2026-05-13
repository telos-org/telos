package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/telos-org/telos-go/internal/game"
	"github.com/telos-org/telos-go/internal/sessionapi"
)

// fakeExecutor for testing local run.
type fakeExecutor struct {
	proverResult   game.TurnResult
	verifierResult game.TurnResult
	onExecute      func(role string)
}

func (f *fakeExecutor) ExecuteTurn(task string, role string, ts *game.TurnState) game.TurnResult {
	if f.onExecute != nil {
		f.onExecute(role)
	}
	if role == "prover" {
		return f.proverResult
	}
	return f.verifierResult
}

func (f *fakeExecutor) WorkspaceState() string {
	return "=== FILES ===\n(no files)"
}

func (f *fakeExecutor) CheckpointWorkspace(dest string) bool {
	os.MkdirAll(filepath.Dir(dest), 0o755)
	os.WriteFile(dest, []byte("fake"), 0o644)
	return true
}

func writeTestSpec(t *testing.T, dir string) string {
	t.Helper()
	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: v0\nname: cli-test\nplatform: local\n---\n# CLI Test\n\nTest body."), 0o644)
	return specPath
}

func TestCreateLocalSession(t *testing.T) {
	dir := t.TempDir()
	specPath := writeTestSpec(t, dir)

	// Change to temp dir so .telos goes there
	orig, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(orig)

	session, err := CreateLocalSession(specPath, LocalRunConfig{
		Model:    "test-model",
		Thinking: "medium",
	})
	if err != nil {
		t.Fatalf("CreateLocalSession: %v", err)
	}

	if session.SessionID == "" {
		t.Error("session ID should not be empty")
	}
	if session.SpecName != "cli-test" {
		t.Errorf("spec name: got %q", session.SpecName)
	}
	if !strings.HasPrefix(session.SessionID, "local_") {
		t.Errorf("session ID should start with local_: got %q", session.SessionID)
	}

	// Verify manifest exists
	manifestPath := filepath.Join(session.SessionDir, "session.json")
	if _, err := os.Stat(manifestPath); err != nil {
		t.Errorf("manifest missing: %v", err)
	}

	// Verify manifest content
	data, _ := os.ReadFile(manifestPath)
	var m map[string]interface{}
	json.Unmarshal(data, &m)
	if m["session_id"] != session.SessionID {
		t.Errorf("manifest session_id: got %v", m["session_id"])
	}
	if m["spec_name"] != "cli-test" {
		t.Errorf("manifest spec_name: got %v", m["spec_name"])
	}
	cfg, _ := m["config"].(map[string]interface{})
	if cfg["model"] != "test-model" {
		t.Errorf("manifest model: got %v", cfg["model"])
	}

	// Verify spec was copied
	specDir := filepath.Join(session.SessionDir, "specs", "cli-test")
	if _, err := os.Stat(filepath.Join(specDir, "spec.md")); err != nil {
		t.Errorf("spec not copied: %v", err)
	}
}

func TestCreateLocalSessionRejectsWorkspaceFile(t *testing.T) {
	dir := t.TempDir()
	specPath := writeTestSpec(t, dir)
	workspacePath := filepath.Join(dir, "workspace-file")
	if err := os.WriteFile(workspacePath, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := CreateLocalSession(specPath, LocalRunConfig{Workspace: workspacePath})
	if err == nil {
		t.Fatal("expected invalid workspace error")
	}
	if !strings.Contains(err.Error(), "create sessions root") && !strings.Contains(err.Error(), "create workspace") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunLocalSessionWithFakeExecutor(t *testing.T) {
	dir := t.TempDir()
	specPath := writeTestSpec(t, dir)

	orig, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(orig)

	session, err := CreateLocalSession(specPath, LocalRunConfig{
		MaxRounds: 4,
	})
	if err != nil {
		t.Fatalf("CreateLocalSession: %v", err)
	}

	exec := &fakeExecutor{
		proverResult: game.TurnResult{
			Role:   "prover",
			Status: game.StatusContinue,
			Logs:   "I built the thing.\n\n<progress_update>Built</progress_update>",
		},
		verifierResult: game.TurnResult{
			Role:   "verifier",
			Status: game.StatusConcede,
			Logs:   "Looks good.\n\n<status>CONCEDE</status>\n",
		},
	}

	result, err := RunLocalSessionWithExecutor(session.SessionDir, exec)
	if err != nil {
		t.Fatalf("RunLocalSession: %v", err)
	}

	if result.GameResult != game.GameSuccess {
		t.Errorf("game result: got %s", result.GameResult)
	}
	if result.VerifierConceded != true {
		t.Error("expected verifier conceded")
	}

	// Verify epoch was written
	manifestData, _ := os.ReadFile(filepath.Join(session.SessionDir, "session.json"))
	var m map[string]interface{}
	json.Unmarshal(manifestData, &m)
	epochs, _ := m["epochs"].([]interface{})
	if len(epochs) == 0 {
		t.Fatal("expected at least 1 epoch")
	}
	epoch0, _ := epochs[0].(map[string]interface{})
	if epoch0["result"] != "completed" {
		t.Errorf("epoch result: got %v", epoch0["result"])
	}

	// Verify session can be read by the store
	store := sessionapi.NewFileStore(filepath.Join(dir, ".telos", "sessions"))
	sessionAPI, err := store.Get(session.SessionID)
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if sessionAPI.Status != sessionapi.StatusCompleted {
		t.Errorf("status: got %s", sessionAPI.Status)
	}
}

func TestRunLocalSessionDefaultsMissingWorkspaceToSessionDir(t *testing.T) {
	dir := t.TempDir()
	specPath := writeTestSpec(t, dir)

	orig, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(orig)

	session, err := CreateLocalSession(specPath, LocalRunConfig{MaxRounds: 4})
	if err != nil {
		t.Fatalf("CreateLocalSession: %v", err)
	}
	manifestPath := filepath.Join(session.SessionDir, "session.json")
	manifestData, _ := os.ReadFile(manifestPath)
	var manifest map[string]interface{}
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}
	cfg := manifest["config"].(map[string]interface{})
	delete(cfg, "workspace")
	if err := writeManifestJSON(session.SessionDir, manifest); err != nil {
		t.Fatal(err)
	}

	exec := &fakeExecutor{
		proverResult: game.TurnResult{
			Role:   "prover",
			Status: game.StatusContinue,
			Logs:   "Built.\n\n<progress_update>done</progress_update>",
		},
		verifierResult: game.TurnResult{
			Role:   "verifier",
			Status: game.StatusConcede,
			Logs:   "LGTM\n\n<status>CONCEDE</status>\n",
		},
	}
	if _, err := RunLocalSessionWithExecutor(session.SessionDir, exec); err != nil {
		t.Fatalf("RunLocalSession: %v", err)
	}
	if _, err := os.Stat(filepath.Join(session.SessionDir, "workspace")); err != nil {
		t.Fatalf("default workspace was not created: %v", err)
	}
}

func TestRunLocalSessionRejectsMissingSessionSpecPath(t *testing.T) {
	dir := t.TempDir()
	specPath := writeTestSpec(t, dir)

	orig, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(orig)

	session, err := CreateLocalSession(specPath, LocalRunConfig{MaxRounds: 4})
	if err != nil {
		t.Fatalf("CreateLocalSession: %v", err)
	}

	manifestPath := filepath.Join(session.SessionDir, "session.json")
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]interface{}
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}
	specs := manifest["specs"].([]interface{})
	spec0 := specs[0].(map[string]interface{})
	delete(spec0, "session_spec_path")
	if err := writeManifestJSON(session.SessionDir, manifest); err != nil {
		t.Fatal(err)
	}

	_, err = RunLocalSessionWithExecutor(session.SessionDir, &fakeExecutor{})
	if err == nil {
		t.Fatal("expected missing session_spec_path error")
	}
	if !strings.Contains(err.Error(), "session_spec_path") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunLocalSessionStopsWhenManifestIsStopped(t *testing.T) {
	dir := t.TempDir()
	specPath := writeTestSpec(t, dir)

	orig, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(orig)

	session, err := CreateLocalSession(specPath, LocalRunConfig{MaxRounds: 4})
	if err != nil {
		t.Fatalf("CreateLocalSession: %v", err)
	}
	store := sessionapi.NewFileStore(filepath.Join(dir, ".telos", "sessions"))

	exec := &fakeExecutor{
		proverResult: game.TurnResult{
			Role:   "prover",
			Status: game.StatusContinue,
			Logs:   "started",
		},
		verifierResult: game.TurnResult{
			Role:   "verifier",
			Status: game.StatusContinue,
			Logs:   "should not run",
		},
		onExecute: func(role string) {
			if role != "prover" {
				return
			}
			if _, err := store.Stop(session.SessionID); err != nil {
				t.Fatalf("Stop: %v", err)
			}
		},
	}

	result, err := RunLocalSessionWithExecutor(session.SessionDir, exec)
	if err != nil {
		t.Fatalf("RunLocalSession: %v", err)
	}
	if result.GameResult != game.GameStopped {
		t.Fatalf("game result: got %s", result.GameResult)
	}

	sessionAPI, err := store.Get(session.SessionID)
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if sessionAPI.Status != sessionapi.StatusStopped {
		t.Fatalf("status: got %s", sessionAPI.Status)
	}
}

func TestEndToEndSmokeTest(t *testing.T) {
	// run -> describe -> logs -> list -> stop
	dir := t.TempDir()
	specPath := writeTestSpec(t, dir)

	orig, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(orig)

	session, err := CreateLocalSession(specPath, LocalRunConfig{
		MaxRounds: 4,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	exec := &fakeExecutor{
		proverResult: game.TurnResult{
			Role:   "prover",
			Status: game.StatusContinue,
			Logs:   "Built.\n\n<progress_update>done</progress_update>",
			Stats:  game.TurnStats{CostUSD: 0.10, InputTokens: 500, OutputTokens: 200},
		},
		verifierResult: game.TurnResult{
			Role:   "verifier",
			Status: game.StatusConcede,
			Logs:   "LGTM\n\n<status>CONCEDE</status>\n",
			Stats:  game.TurnStats{CostUSD: 0.05, InputTokens: 300, OutputTokens: 100},
		},
	}

	result, err := RunLocalSessionWithExecutor(session.SessionDir, exec)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.GameResult != game.GameSuccess {
		t.Fatalf("game: got %s", result.GameResult)
	}

	store := sessionapi.NewFileStore(filepath.Join(dir, ".telos", "sessions"))

	// describe
	sess, err := store.Get(session.SessionID)
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if sess.Status != sessionapi.StatusCompleted {
		t.Errorf("describe status: got %s", sess.Status)
	}
	if sess.SpecName == nil || *sess.SpecName != "cli-test" {
		t.Errorf("describe spec_name: got %v", sess.SpecName)
	}

	// logs
	transcript, err := store.Transcript(session.SessionID)
	if err != nil {
		t.Fatalf("logs: %v", err)
	}
	if !strings.Contains(transcript, "Prover 1") {
		t.Error("transcript should contain prover turn")
	}
	if !strings.Contains(transcript, "Verifier 1") {
		t.Error("transcript should contain verifier turn")
	}

	// events
	events, err := store.Events(session.SessionID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(events) == 0 {
		t.Error("expected evidence events")
	}
	hasGameStart := false
	hasGameEnd := false
	for _, ev := range events {
		if ev.Event == "game_start" {
			hasGameStart = true
		}
		if ev.Event == "game_end" {
			hasGameEnd = true
		}
	}
	if !hasGameStart {
		t.Error("missing game_start event")
	}
	if !hasGameEnd {
		t.Error("missing game_end event")
	}

	// list
	sessions, err := store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].SessionID != session.SessionID {
		t.Errorf("list session ID: got %q", sessions[0].SessionID)
	}

	// stop (already completed, should be idempotent)
	stopped, err := store.Stop(session.SessionID)
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if stopped.Status != sessionapi.StatusCompleted {
		t.Errorf("stop on completed: got %s", stopped.Status)
	}
}

func TestSessionArtifactShape(t *testing.T) {
	// Verify the on-disk artifact shape matches Python expectations
	dir := t.TempDir()
	specPath := writeTestSpec(t, dir)

	orig, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(orig)

	session, _ := CreateLocalSession(specPath, LocalRunConfig{})

	exec := &fakeExecutor{
		proverResult: game.TurnResult{
			Role:   "prover",
			Status: game.StatusContinue,
			Logs:   "done\n\n<progress_update>done</progress_update>",
		},
		verifierResult: game.TurnResult{
			Role:   "verifier",
			Status: game.StatusConcede,
			Logs:   "ok\n\n<status>CONCEDE</status>\n",
		},
	}

	RunLocalSessionWithExecutor(session.SessionDir, exec)

	// Check expected files exist
	expected := []string{
		"session.json",
		filepath.Join("specs", "cli-test", "evidence.jsonl"),
		filepath.Join("specs", "cli-test", "pvg-transcript-"+session.SessionID+".md"),
		filepath.Join("specs", "cli-test", "spec.md"),
		filepath.Join("specs", "cli-test", "workspace.tar.gz"),
		filepath.Join("specs", "cli-test", "turns", "0001-prover", "task.md"),
		filepath.Join("specs", "cli-test", "turns", "0002-verifier", "task.md"),
	}

	for _, rel := range expected {
		path := filepath.Join(session.SessionDir, rel)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("missing artifact: %s", rel)
		}
	}

	// Verify session.json has Python-compatible shape
	data, _ := os.ReadFile(filepath.Join(session.SessionDir, "session.json"))
	var m map[string]interface{}
	json.Unmarshal(data, &m)

	requiredKeys := []string{
		"session_id", "session_kind", "created_at", "launcher",
		"source_spec_path", "session_spec_path", "spec_name",
		"config", "provenance", "specs", "epochs",
	}
	for _, key := range requiredKeys {
		if _, ok := m[key]; !ok {
			t.Errorf("manifest missing key %q", key)
		}
	}

	specs, _ := m["specs"].([]interface{})
	if len(specs) != 1 {
		t.Fatalf("expected 1 spec, got %d", len(specs))
	}
	spec0, _ := specs[0].(map[string]interface{})
	specKeys := []string{"index", "name", "dir_name", "evidence_path", "transcript_path", "workspace_path"}
	for _, key := range specKeys {
		if _, ok := spec0[key]; !ok {
			t.Errorf("spec missing key %q", key)
		}
	}
}

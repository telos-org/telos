// Package cli implements the telos CLI commands.
package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/telos-org/telos-go/internal/executor"
	"github.com/telos-org/telos-go/internal/game"
	"github.com/telos-org/telos-go/internal/platform"
	"github.com/telos-org/telos-go/internal/spec"
)

const DefaultLocalModel = "claude-opus-4-6"

// LocalRunConfig holds configuration for local PVG runs.
type LocalRunConfig struct {
	Workspace       string
	Model           string
	Thinking        string
	MaxRounds       int
	MaxCostUSD      *float64
	AgentTimeoutSec int
}

// LocalSession holds the result of session creation.
type LocalSession struct {
	SessionID  string
	SessionDir string
	Workspace  string
	SpecName   string
}

// CreateLocalSession compiles a spec and creates a local session layout.
func CreateLocalSession(specPath string, cfg LocalRunConfig) (*LocalSession, error) {
	compiled, err := spec.CompileEnvironment(specPath)
	if err != nil {
		return nil, err
	}

	absSpec, _ := filepath.Abs(specPath)
	var workspace string
	if cfg.Workspace != "" {
		workspace, _ = filepath.Abs(cfg.Workspace)
	}

	sessionsRoot := filepath.Join(".telos", "sessions")
	if workspace != "" {
		sessionsRoot = filepath.Join(workspace, ".telos", "sessions")
	}
	sessionsRoot, _ = filepath.Abs(sessionsRoot)

	sessionDir, err := newSessionDir(sessionsRoot)
	if err != nil {
		return nil, err
	}

	if workspace == "" {
		workspace = filepath.Join(sessionDir, "workspace")
	}
	os.MkdirAll(workspace, 0o755)

	specDir := filepath.Join(sessionDir, "specs", compiled.Environment.Name)
	state := game.NewPVGState(compiled.Environment.Name, specDir, filepath.Base(sessionDir))
	state.Ensure()

	// Copy spec
	data, _ := os.ReadFile(absSpec)
	os.WriteFile(state.SpecPath(), data, 0o644)

	writeLocalManifest(sessionDir, compiled, absSpec, state, cfg, workspace)

	return &LocalSession{
		SessionID:  filepath.Base(sessionDir),
		SessionDir: sessionDir,
		Workspace:  workspace,
		SpecName:   compiled.Environment.Name,
	}, nil
}

// RunLocalSession executes a persisted local session.
func RunLocalSession(sessionDir string) (*game.PVGResult, error) {
	return RunLocalSessionWithExecutor(sessionDir, nil)
}

// RunLocalSessionWithExecutor runs a session with an optional custom executor.
func RunLocalSessionWithExecutor(sessionDir string, exec game.AgentExecutor) (*game.PVGResult, error) {
	data, err := os.ReadFile(filepath.Join(sessionDir, "session.json"))
	if err != nil {
		return nil, fmt.Errorf("read session manifest: %w", err)
	}
	var manifest map[string]interface{}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}

	cfg := manifestToConfig(manifest)
	specs, _ := manifest["specs"].([]interface{})
	if len(specs) == 0 {
		return nil, fmt.Errorf("no specs in manifest")
	}
	spec0, _ := specs[0].(map[string]interface{})
	sessionSpecPath, _ := spec0["session_spec_path"].(string)

	compiled, err := spec.CompileEnvironment(sessionSpecPath)
	if err != nil {
		return nil, err
	}

	specDir := filepath.Dir(sessionSpecPath)
	sessionID, _ := manifest["session_id"].(string)
	state := game.NewPVGState(compiled.Environment.Name, specDir, sessionID)

	workspace := cfg.Workspace

	// Open epoch
	startEpoch(sessionDir, manifest)

	var agentExec game.AgentExecutor
	if exec != nil {
		agentExec = exec
	} else {
		agentExec, err = createPiExecutor(workspace, cfg)
		if err != nil {
			return nil, err
		}
	}

	pvgCfg := game.PVGConfig{
		MaxRounds:  cfg.MaxRounds,
		MaxCostUSD: cfg.MaxCostUSD,
		Verbose:    true,
	}

	pvg := game.NewPVG(compiled, agentExec, state, pvgCfg)
	result := pvg.Run()

	// Close epoch
	finishEpoch(sessionDir, manifest, result)

	return result, nil
}

func createPiExecutor(workspace string, cfg LocalRunConfig) (*executor.PiExecutor, error) {
	// Check pi is available
	if _, err := exec.LookPath("pi"); err != nil {
		return nil, fmt.Errorf("pi executable not found on PATH. Install pi (https://github.com/mariozechner/pi-coding-agent) to run local sessions")
	}
	p := platform.NewLocalPlatform(workspace)
	model := cfg.Model
	if model == "" {
		model = DefaultLocalModel
	}
	return executor.NewPiExecutor(p, model, cfg.Thinking, cfg.AgentTimeoutSec), nil
}

func newSessionDir(root string) (string, error) {
	os.MkdirAll(root, 0o755)
	stamp := time.Now().UTC().Format("20060102_150405")
	for i := 0; i < 100; i++ {
		dir := filepath.Join(root, fmt.Sprintf("local_%s_%02d", stamp, i))
		if err := os.Mkdir(dir, 0o755); err == nil {
			return dir, nil
		}
	}
	return "", fmt.Errorf("could not allocate local session under %s", root)
}

func writeLocalManifest(sessionDir string, compiled *spec.CompiledEnvironment, specPath string, state *game.PVGState, cfg LocalRunConfig, workspace string) {
	model := cfg.Model
	if model == "" {
		model = DefaultLocalModel
	}
	thinking := cfg.Thinking
	if thinking == "" {
		thinking = "medium"
	}
	maxRounds := cfg.MaxRounds
	if maxRounds <= 0 {
		maxRounds = 20
	}
	agentTimeout := cfg.AgentTimeoutSec
	if agentTimeout <= 0 {
		agentTimeout = 1800
	}

	manifest := map[string]interface{}{
		"session_id":        filepath.Base(sessionDir),
		"session_kind":      "task",
		"created_at":        time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		"launcher":          "local",
		"parent_session_id": nil,
		"source_spec_path":  specPath,
		"session_spec_path": state.SpecPath(),
		"spec_name":         compiled.Environment.Name,
		"config": map[string]interface{}{
			"model":             model,
			"max_rounds":        maxRounds,
			"max_cost_usd":      cfg.MaxCostUSD,
			"agent_timeout_sec": agentTimeout,
			"thinking":          thinking,
			"workspace":         workspace,
		},
		"provenance": map[string]interface{}{"mode": "local"},
		"specs": []map[string]interface{}{{
			"index":             0,
			"name":              compiled.Environment.Name,
			"dir_name":          compiled.Environment.Name,
			"environment_path":  specPath,
			"session_spec_path": state.SpecPath(),
			"content_hash":      compiled.ContentHash,
			"evidence_path":     state.EvidencePath,
			"transcript_path":   state.TranscriptPath,
			"workspace_path":    state.WorkspacePath,
			"interval_seconds":  compiled.Environment.IntervalSeconds,
		}},
		"epochs": []interface{}{},
	}

	data, _ := json.MarshalIndent(manifest, "", "  ")
	os.WriteFile(filepath.Join(sessionDir, "session.json"), append(data, '\n'), 0o644)
}

func manifestToConfig(manifest map[string]interface{}) LocalRunConfig {
	cfg, _ := manifest["config"].(map[string]interface{})
	if cfg == nil {
		cfg = map[string]interface{}{}
	}
	lrc := LocalRunConfig{
		Thinking: "medium",
	}
	if w, ok := cfg["workspace"].(string); ok {
		lrc.Workspace = w
	}
	if m, ok := cfg["model"].(string); ok {
		lrc.Model = m
	}
	if t, ok := cfg["thinking"].(string); ok && t != "" {
		lrc.Thinking = t
	}
	if mr, ok := cfg["max_rounds"].(float64); ok {
		lrc.MaxRounds = int(mr)
	}
	if lrc.MaxRounds <= 0 {
		lrc.MaxRounds = 20
	}
	if mc, ok := cfg["max_cost_usd"].(float64); ok {
		lrc.MaxCostUSD = &mc
	}
	if at, ok := cfg["agent_timeout_sec"].(float64); ok {
		lrc.AgentTimeoutSec = int(at)
	}
	if lrc.AgentTimeoutSec <= 0 {
		lrc.AgentTimeoutSec = 1800
	}
	return lrc
}

func startEpoch(sessionDir string, manifest map[string]interface{}) {
	epochs, _ := manifest["epochs"].([]interface{})
	epochID := len(epochs) + 1
	epoch := map[string]interface{}{
		"id":          epochID,
		"started_at":  time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		"finished_at": nil,
		"result":      nil,
		"error":       nil,
		"runner": map[string]interface{}{
			"kind": "local-subprocess",
			"pid":  os.Getpid(),
		},
	}
	manifest["epochs"] = append(epochs, epoch)
	writeManifestJSON(sessionDir, manifest)
}

func finishEpoch(sessionDir string, manifest map[string]interface{}, result *game.PVGResult) {
	epochs, _ := manifest["epochs"].([]interface{})
	if len(epochs) == 0 {
		return
	}
	last, _ := epochs[len(epochs)-1].(map[string]interface{})
	if last == nil {
		return
	}
	last["finished_at"] = time.Now().UTC().Format("2006-01-02T15:04:05.000Z")

	switch result.GameResult {
	case game.GameSuccess:
		last["result"] = "completed"
	case game.GameFailure:
		last["result"] = "failed"
		if result.Error != "" {
			last["error"] = result.Error
		}
	case game.GameTimeout:
		last["result"] = "failed"
		last["error"] = "max_rounds_exceeded"
	}

	writeManifestJSON(sessionDir, manifest)
}

func writeManifestJSON(sessionDir string, manifest map[string]interface{}) {
	data, _ := json.MarshalIndent(manifest, "", "  ")
	tmp := filepath.Join(sessionDir, "session.json.tmp")
	os.WriteFile(tmp, append(data, '\n'), 0o644)
	os.Rename(tmp, filepath.Join(sessionDir, "session.json"))
}

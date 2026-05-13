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

	absSpec, err := filepath.Abs(specPath)
	if err != nil {
		return nil, fmt.Errorf("resolve spec path: %w", err)
	}
	var workspace string
	if cfg.Workspace != "" {
		workspace, err = filepath.Abs(cfg.Workspace)
		if err != nil {
			return nil, fmt.Errorf("resolve workspace path: %w", err)
		}
	}

	sessionsRoot := filepath.Join(".telos", "sessions")
	if workspace != "" {
		sessionsRoot = filepath.Join(workspace, ".telos", "sessions")
	}
	sessionsRoot, err = filepath.Abs(sessionsRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve sessions root: %w", err)
	}

	sessionDir, err := newSessionDir(sessionsRoot)
	if err != nil {
		return nil, err
	}

	if workspace == "" {
		workspace = filepath.Join(sessionDir, "workspace")
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return nil, fmt.Errorf("create workspace: %w", err)
	}

	specDir := filepath.Join(sessionDir, "specs", compiled.Environment.Name)
	state := game.NewPVGState(compiled.Environment.Name, specDir, filepath.Base(sessionDir))
	if err := state.Ensure(); err != nil {
		return nil, fmt.Errorf("create session state: %w", err)
	}

	data, err := os.ReadFile(absSpec)
	if err != nil {
		return nil, fmt.Errorf("read spec: %w", err)
	}
	if err := os.WriteFile(state.SpecPath(), data, 0o644); err != nil {
		return nil, fmt.Errorf("write session spec: %w", err)
	}

	if err := writeLocalManifest(sessionDir, compiled, absSpec, state, cfg, workspace); err != nil {
		return nil, err
	}

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
	if manifestStopped(manifest) {
		return &game.PVGResult{GameResult: game.GameStopped, Error: "stopped by operator"}, nil
	}

	cfg := manifestToConfig(manifest)
	specs, _ := manifest["specs"].([]interface{})
	if len(specs) == 0 {
		return nil, fmt.Errorf("no specs in manifest")
	}
	spec0, _ := specs[0].(map[string]interface{})
	sessionSpecPath, _ := spec0["session_spec_path"].(string)
	if sessionSpecPath == "" {
		return nil, fmt.Errorf("manifest spec missing session_spec_path")
	}

	compiled, err := spec.CompileEnvironment(sessionSpecPath)
	if err != nil {
		return nil, err
	}

	specDir := filepath.Dir(sessionSpecPath)
	sessionID, _ := manifest["session_id"].(string)
	state := game.NewPVGState(compiled.Environment.Name, specDir, sessionID)

	workspace := cfg.Workspace
	if workspace == "" {
		workspace = filepath.Join(sessionDir, "workspace")
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return nil, err
	}

	// Open epoch
	if err := startEpoch(sessionDir, manifest); err != nil {
		return nil, err
	}

	var agentExec game.AgentExecutor
	if exec != nil {
		agentExec = exec
	} else {
		agentExec, err = createPiExecutor(workspace, cfg)
		if err != nil {
			fail := &game.PVGResult{GameResult: game.GameFailure, Error: err.Error()}
			if finishErr := finishEpoch(sessionDir, manifest, fail); finishErr != nil {
				return nil, fmt.Errorf("%w; also failed to finish epoch: %v", err, finishErr)
			}
			return nil, err
		}
	}

	pvgCfg := game.PVGConfig{
		MaxRounds:     cfg.MaxRounds,
		MaxCostUSD:    cfg.MaxCostUSD,
		Verbose:       true,
		StopRequested: func() bool { return sessionStopped(sessionDir) },
	}

	pvg := game.NewPVG(compiled, agentExec, state, pvgCfg)
	result := pvg.Run()

	// Close epoch
	if err := finishEpoch(sessionDir, manifest, result); err != nil {
		return result, err
	}

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
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("create sessions root: %w", err)
	}
	stamp := time.Now().UTC().Format("20060102_150405")
	for i := 0; i < 100; i++ {
		dir := filepath.Join(root, fmt.Sprintf("local_%s_%02d", stamp, i))
		if err := os.Mkdir(dir, 0o755); err == nil {
			return dir, nil
		} else if !os.IsExist(err) {
			return "", fmt.Errorf("create session dir: %w", err)
		}
	}
	return "", fmt.Errorf("could not allocate local session under %s", root)
}

func writeLocalManifest(sessionDir string, compiled *spec.CompiledEnvironment, specPath string, state *game.PVGState, cfg LocalRunConfig, workspace string) error {
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

	if err := writeManifestJSON(sessionDir, manifest); err != nil {
		return fmt.Errorf("write session manifest: %w", err)
	}
	return nil
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

func startEpoch(sessionDir string, manifest map[string]interface{}) error {
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
	if err := writeManifestJSON(sessionDir, manifest); err != nil {
		return fmt.Errorf("start epoch: %w", err)
	}
	return nil
}

func finishEpoch(sessionDir string, manifest map[string]interface{}, result *game.PVGResult) error {
	manifest = currentManifest(sessionDir, manifest)
	if manifestStopped(manifest) && result.GameResult != game.GameStopped {
		return nil
	}
	epochs, _ := manifest["epochs"].([]interface{})
	if len(epochs) == 0 {
		return nil
	}
	last, _ := epochs[len(epochs)-1].(map[string]interface{})
	if last == nil {
		return nil
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
	case game.GameStopped:
		last["result"] = "stopped"
		if result.Error != "" {
			last["error"] = result.Error
		} else {
			last["error"] = "stopped by operator"
		}
	}

	if err := writeManifestJSON(sessionDir, manifest); err != nil {
		return fmt.Errorf("finish epoch: %w", err)
	}
	return nil
}

func sessionStopped(sessionDir string) bool {
	data, err := os.ReadFile(filepath.Join(sessionDir, "session.json"))
	if err != nil {
		return false
	}
	var manifest map[string]interface{}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return false
	}
	return manifestStopped(manifest)
}

func manifestStopped(manifest map[string]interface{}) bool {
	epochs, _ := manifest["epochs"].([]interface{})
	if len(epochs) == 0 {
		return false
	}
	last, _ := epochs[len(epochs)-1].(map[string]interface{})
	if last == nil {
		return false
	}
	result, _ := last["result"].(string)
	return result == "stopped"
}

func currentManifest(sessionDir string, fallback map[string]interface{}) map[string]interface{} {
	data, err := os.ReadFile(filepath.Join(sessionDir, "session.json"))
	if err != nil {
		return fallback
	}
	var manifest map[string]interface{}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fallback
	}
	return manifest
}

func writeManifestJSON(sessionDir string, manifest map[string]interface{}) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(sessionDir, "session.json.tmp")
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(sessionDir, "session.json")); err != nil {
		return err
	}
	return nil
}

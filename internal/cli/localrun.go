// Package cli implements the telos CLI commands.
package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/telos-org/telos-go/internal/executor"
	"github.com/telos-org/telos-go/internal/game"
	"github.com/telos-org/telos-go/internal/platform"
	"github.com/telos-org/telos-go/internal/sessionapi"
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

// SubmitLocalSession creates a session and starts its worker in the background.
func SubmitLocalSession(specPath string, cfg LocalRunConfig) (*LocalSession, error) {
	session, err := CreateLocalSession(specPath, cfg)
	if err != nil {
		return nil, err
	}
	if err := startLocalWorker(session.SessionDir); err != nil {
		return nil, err
	}
	return session, nil
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

func startLocalWorker(sessionDir string) error {
	telosd, err := resolveTelosd()
	if err != nil {
		return err
	}
	logPath := filepath.Join(sessionDir, "runner.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open runner log: %w", err)
	}
	defer logFile.Close()

	cmd := exec.Command(telosd, "--session-dir", sessionDir)
	cmd.Env = append(os.Environ(), "TELOS_SESSION_DIR="+filepath.Dir(sessionDir))
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start worker: %w", err)
	}
	if err := markWorkerStarted(sessionDir, cmd.Process.Pid, logPath); err != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		_ = cmd.Process.Kill()
		return err
	}
	return nil
}

func resolveTelosd() (string, error) {
	if configured := os.Getenv("TELOSD_PATH"); configured != "" {
		return configured, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve telos executable: %w", err)
	}
	sibling := filepath.Join(filepath.Dir(exe), "telosd")
	if _, err := os.Stat(sibling); err == nil {
		return sibling, nil
	}
	if path, err := exec.LookPath("telosd"); err == nil {
		return path, nil
	}
	return "", fmt.Errorf("telosd not found; install telosd next to telos or set TELOSD_PATH")
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

	manifestPath := filepath.Join(sessionDir, "session.json")
	err := sessionapi.WriteInitialManifest(manifestPath, sessionapi.InitialManifest{
		SessionID:       filepath.Base(sessionDir),
		SessionKind:     sessionapi.KindTask,
		Runtime:         sessionapi.RuntimeLocal,
		CreatedAt:       time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		Launcher:        "local",
		SourceSpecPath:  &specPath,
		SessionSpecPath: strPtr(state.SpecPath()),
		SpecName:        compiled.Environment.Name,
		Config: map[string]interface{}{
			"model":             model,
			"max_rounds":        maxRounds,
			"max_cost_usd":      cfg.MaxCostUSD,
			"agent_timeout_sec": agentTimeout,
			"thinking":          thinking,
			"workspace":         workspace,
		},
		Provenance: map[string]interface{}{"mode": "local"},
		Specs: []sessionapi.InitialManifestSpec{{
			Index:           0,
			Name:            compiled.Environment.Name,
			DirName:         compiled.Environment.Name,
			EnvironmentPath: &specPath,
			SessionSpecPath: strPtr(state.SpecPath()),
			ContentHash:     strPtr(compiled.ContentHash),
			EvidencePath:    strPtr(state.EvidencePath),
			TranscriptPath:  strPtr(state.TranscriptPath),
			WorkspacePath:   strPtr(state.WorkspacePath),
			IntervalSeconds: compiled.Environment.IntervalSeconds,
		}},
	})
	if err != nil {
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
	return startEpochWithRunner(sessionDir, manifest, os.Getpid(), "")
}

func markWorkerStarted(sessionDir string, pid int, logPath string) error {
	data, err := os.ReadFile(filepath.Join(sessionDir, "session.json"))
	if err != nil {
		return fmt.Errorf("read session manifest: %w", err)
	}
	var manifest map[string]interface{}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("decode session manifest: %w", err)
	}
	return startEpochWithRunner(sessionDir, manifest, pid, logPath)
}

func startEpochWithRunner(sessionDir string, manifest map[string]interface{}, pid int, logPath string) error {
	epochs, _ := manifest["epochs"].([]interface{})
	if len(epochs) > 0 {
		if last, _ := epochs[len(epochs)-1].(map[string]interface{}); last != nil && last["finished_at"] == nil {
			return nil
		}
	}
	epochID := len(epochs) + 1
	runner := map[string]interface{}{
		"kind": "local-subprocess",
		"pid":  pid,
		"pgid": pid,
	}
	if logPath != "" {
		runner["log_path"] = logPath
	}
	epoch := map[string]interface{}{
		"id":          epochID,
		"started_at":  time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		"finished_at": nil,
		"result":      nil,
		"error":       nil,
		"runner":      runner,
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

func strPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

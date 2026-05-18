// Package cli implements the telos CLI commands.
package cli

import (
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
	manifest, err := sessionapi.ReadManifest(manifestPath(sessionDir))
	if err != nil {
		return nil, fmt.Errorf("read session manifest: %w", err)
	}
	if manifest.IsStopped() {
		return &game.PVGResult{GameResult: game.GameStopped, Error: "stopped by operator"}, nil
	}

	cfg := manifestToConfig(manifest)
	if len(manifest.Specs) == 0 {
		return nil, fmt.Errorf("no specs in manifest")
	}
	sessionSpecPath := manifest.Specs[0].SessionSpecPath
	if sessionSpecPath == nil || *sessionSpecPath == "" {
		return nil, fmt.Errorf("manifest spec missing session_spec_path")
	}

	compiled, err := spec.CompileEnvironment(*sessionSpecPath)
	if err != nil {
		return nil, err
	}

	specDir := filepath.Dir(*sessionSpecPath)
	state := game.NewPVGState(compiled.Environment.Name, specDir, manifest.SessionID)

	workspace := cfg.Workspace
	if workspace == "" {
		workspace = filepath.Join(sessionDir, "workspace")
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return nil, err
	}

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
		Config: sessionapi.SessionConfig{
			Model:           model,
			MaxRounds:       maxRounds,
			MaxCostUSD:      cfg.MaxCostUSD,
			AgentTimeoutSec: agentTimeout,
			Thinking:        thinking,
			Workspace:       workspace,
		},
		Provenance: map[string]any{"mode": "local"},
		Specs: []sessionapi.InitialManifestSpec{{
			Index:           0,
			Name:            compiled.Environment.Name,
			DirName:         compiled.Environment.Name,
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

func manifestToConfig(manifest *sessionapi.Manifest) LocalRunConfig {
	cfg := manifest.Config
	lrc := LocalRunConfig{
		Workspace:       cfg.Workspace,
		Model:           cfg.Model,
		Thinking:        cfg.Thinking,
		MaxRounds:       cfg.MaxRounds,
		MaxCostUSD:      cfg.MaxCostUSD,
		AgentTimeoutSec: cfg.AgentTimeoutSec,
	}
	if lrc.Thinking == "" {
		lrc.Thinking = "medium"
	}
	if lrc.MaxRounds <= 0 {
		lrc.MaxRounds = 20
	}
	if lrc.AgentTimeoutSec <= 0 {
		lrc.AgentTimeoutSec = 1800
	}
	return lrc
}

func startEpoch(sessionDir string, manifest *sessionapi.Manifest) error {
	return startEpochWithRunner(sessionDir, manifest, os.Getpid(), "")
}

func markWorkerStarted(sessionDir string, pid int, logPath string) error {
	manifest, err := sessionapi.ReadManifest(manifestPath(sessionDir))
	if err != nil {
		return fmt.Errorf("read session manifest: %w", err)
	}
	return startEpochWithRunner(sessionDir, manifest, pid, logPath)
}

func startEpochWithRunner(sessionDir string, manifest *sessionapi.Manifest, pid int, logPath string) error {
	if manifest.OpenEpoch() != nil {
		return nil
	}
	epochID := len(manifest.Epochs) + 1
	runner := runnerIdentity(pid)
	if logPath != "" {
		runner.LogPath = logPath
	}
	manifest.Epochs = append(manifest.Epochs, sessionapi.Epoch{
		ID:        epochID,
		StartedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		Runner:    &runner,
	})
	if err := sessionapi.WriteManifest(manifestPath(sessionDir), manifest); err != nil {
		return fmt.Errorf("start epoch: %w", err)
	}
	return nil
}

func runnerIdentity(pid int) sessionapi.Runner {
	runner := sessionapi.Runner{
		Kind:      "local-subprocess",
		PID:       pid,
		PGID:      pid,
		StartedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
	}
	if os.Getenv("KUBERNETES_SERVICE_HOST") == "" {
		return runner
	}

	runner.Kind = "kubernetes-pod"
	runner.InCluster = true
	if hostname := firstEnv("HOSTNAME"); hostname != "" {
		runner.Hostname = hostname
	}
	if podName := firstEnv("TELOS_RUNNER_POD_NAME", "POD_NAME"); podName != "" {
		runner.PodName = podName
	} else if hostname := firstEnv("HOSTNAME"); hostname != "" {
		runner.PodName = hostname
	}
	if namespace := firstEnv("TELOS_RUNNER_POD_NAMESPACE", "POD_NAMESPACE"); namespace != "" {
		runner.PodNamespace = namespace
	}
	return runner
}

func firstEnv(names ...string) string {
	for _, name := range names {
		if value := os.Getenv(name); value != "" {
			return value
		}
	}
	return ""
}

func finishEpoch(sessionDir string, manifest *sessionapi.Manifest, result *game.PVGResult) error {
	manifest = currentManifest(sessionDir, manifest)
	if manifest.IsStopped() && result.GameResult != game.GameStopped {
		return nil
	}
	last := manifest.LastEpoch()
	if last == nil {
		return nil
	}
	finishedAt := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	last.FinishedAt = &finishedAt

	switch result.GameResult {
	case game.GameSuccess:
		completed := "completed"
		last.Result = &completed
	case game.GameFailure:
		failed := "failed"
		last.Result = &failed
		if result.Error != "" {
			last.Error = &result.Error
		}
	case game.GameTimeout:
		failed := "failed"
		err := "max_rounds_exceeded"
		last.Result = &failed
		last.Error = &err
	case game.GameStopped:
		stopped := "stopped"
		last.Result = &stopped
		if result.Error != "" {
			last.Error = &result.Error
		} else {
			err := "stopped by operator"
			last.Error = &err
		}
	}

	if err := sessionapi.WriteManifest(manifestPath(sessionDir), manifest); err != nil {
		return fmt.Errorf("finish epoch: %w", err)
	}
	return nil
}

func sessionStopped(sessionDir string) bool {
	manifest, err := sessionapi.ReadManifest(manifestPath(sessionDir))
	if err != nil {
		return false
	}
	return manifest.IsStopped()
}

func currentManifest(sessionDir string, fallback *sessionapi.Manifest) *sessionapi.Manifest {
	manifest, err := sessionapi.ReadManifest(manifestPath(sessionDir))
	if err != nil {
		return fallback
	}
	return manifest
}

func manifestPath(sessionDir string) string {
	return filepath.Join(sessionDir, "session.json")
}

func strPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

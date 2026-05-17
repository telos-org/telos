// Package cli implements the telos CLI commands.
package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

// RunLocalSession executes a persisted local session.
func RunLocalSession(sessionDir string) (*game.PVGResult, error) {
	return RunLocalSessionWithExecutor(sessionDir, nil)
}

// RunLocalSessionWithExecutor runs a session with an optional custom executor.
func RunLocalSessionWithExecutor(sessionDir string, exec game.AgentExecutor) (*game.PVGResult, error) {
	m, err := sessionapi.ReadManifest(manifestPath(sessionDir))
	if err != nil {
		return nil, fmt.Errorf("read session manifest: %w", err)
	}
	if m.IsStopped() {
		return &game.PVGResult{GameResult: game.GameStopped, Error: "stopped by operator"}, nil
	}

	if len(m.Specs) == 0 {
		return nil, fmt.Errorf("no specs in manifest")
	}
	sessionSpec := m.Specs[0]
	if sessionSpec.SessionSpecPath == nil || *sessionSpec.SessionSpecPath == "" {
		return nil, fmt.Errorf("manifest spec missing session_spec_path")
	}
	sessionSpecPath := *sessionSpec.SessionSpecPath

	cfg := configFromManifest(m)

	compiled, err := spec.CompileEnvironment(sessionSpecPath)
	if err != nil {
		return nil, err
	}

	state := game.NewPVGState(compiled.Environment.Name, filepath.Dir(sessionSpecPath), m.SessionID)

	workspace := cfg.Workspace
	if workspace == "" {
		workspace = filepath.Join(sessionDir, "workspace")
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return nil, err
	}

	// Open epoch.
	if err := startEpoch(sessionDir, m); err != nil {
		return nil, err
	}

	var agentExec game.AgentExecutor
	if exec != nil {
		agentExec = exec
	} else {
		agentExec, err = createPiExecutor(workspace, cfg)
		if err != nil {
			fail := &game.PVGResult{GameResult: game.GameFailure, Error: err.Error()}
			if finishErr := finishEpoch(sessionDir, m, fail); finishErr != nil {
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

	// Close epoch.
	if err := finishEpoch(sessionDir, m, result); err != nil {
		return result, err
	}

	return result, nil
}

func createPiExecutor(workspace string, cfg LocalRunConfig) (*executor.PiExecutor, error) {
	// Check pi is available.
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

	index := 0
	specName := compiled.Environment.Name
	sessionSpecPath := state.SpecPath()
	m := &sessionapi.Manifest{
		SessionID:       filepath.Base(sessionDir),
		SessionKind:     sessionapi.KindTask,
		CreatedAt:       tsNow(),
		Launcher:        "local",
		SourceSpecPath:  &specPath,
		SessionSpecPath: &sessionSpecPath,
		SpecName:        specName,
		Config: sessionapi.SessionConfig{
			Model:           model,
			MaxRounds:       maxRounds,
			MaxCostUSD:      cfg.MaxCostUSD,
			AgentTimeoutSec: agentTimeout,
			Thinking:        thinking,
			Workspace:       workspace,
		},
		Provenance: map[string]any{"mode": "local"},
		Specs: []sessionapi.ManifestSpec{{
			Index:           &index,
			Name:            specName,
			DirName:         specName,
			EnvironmentPath: &specPath,
			SessionSpecPath: &sessionSpecPath,
			ContentHash:     &compiled.ContentHash,
			EvidencePath:    &state.EvidencePath,
			TranscriptPath:  &state.TranscriptPath,
			WorkspacePath:   &state.WorkspacePath,
			IntervalSeconds: compiled.Environment.IntervalSeconds,
		}},
		Epochs: []sessionapi.Epoch{},
	}

	if err := sessionapi.WriteManifest(manifestPath(sessionDir), m); err != nil {
		return fmt.Errorf("write session manifest: %w", err)
	}
	return nil
}

func configFromManifest(m *sessionapi.Manifest) LocalRunConfig {
	cfg := LocalRunConfig{
		Workspace:       m.Config.Workspace,
		Model:           m.Config.Model,
		Thinking:        m.Config.Thinking,
		MaxRounds:       m.Config.MaxRounds,
		MaxCostUSD:      m.Config.MaxCostUSD,
		AgentTimeoutSec: m.Config.AgentTimeoutSec,
	}
	if cfg.Thinking == "" {
		cfg.Thinking = "medium"
	}
	if cfg.MaxRounds <= 0 {
		cfg.MaxRounds = 20
	}
	if cfg.AgentTimeoutSec <= 0 {
		cfg.AgentTimeoutSec = 1800
	}
	return cfg
}

func startEpoch(sessionDir string, m *sessionapi.Manifest) error {
	m.Epochs = append(m.Epochs, sessionapi.Epoch{
		ID:        len(m.Epochs) + 1,
		StartedAt: tsNow(),
		Runner: &sessionapi.Runner{
			Kind: "local-subprocess",
			PID:  os.Getpid(),
		},
	})
	if err := sessionapi.WriteManifest(manifestPath(sessionDir), m); err != nil {
		return fmt.Errorf("start epoch: %w", err)
	}
	return nil
}

func finishEpoch(sessionDir string, fallback *sessionapi.Manifest, result *game.PVGResult) error {
	m := currentManifest(sessionDir, fallback)
	if m.IsStopped() && result.GameResult != game.GameStopped {
		return nil
	}
	last := m.LastEpoch()
	if last == nil {
		return nil
	}
	now := tsNow()
	last.FinishedAt = &now

	switch result.GameResult {
	case game.GameSuccess:
		last.Result = strRef("completed")
	case game.GameFailure:
		last.Result = strRef("failed")
		if result.Error != "" {
			last.Error = strRef(result.Error)
		}
	case game.GameTimeout:
		last.Result = strRef("failed")
		last.Error = strRef("max_rounds_exceeded")
	case game.GameStopped:
		last.Result = strRef("stopped")
		if result.Error != "" {
			last.Error = strRef(result.Error)
		} else {
			last.Error = strRef("stopped by operator")
		}
	}

	if err := sessionapi.WriteManifest(manifestPath(sessionDir), m); err != nil {
		return fmt.Errorf("finish epoch: %w", err)
	}
	return nil
}

func sessionStopped(sessionDir string) bool {
	m, err := sessionapi.ReadManifest(manifestPath(sessionDir))
	if err != nil {
		return false
	}
	return m.IsStopped()
}

// currentManifest re-reads the manifest from disk so concurrent stop requests
// are observed; it falls back to the in-memory copy on read failure.
func currentManifest(sessionDir string, fallback *sessionapi.Manifest) *sessionapi.Manifest {
	m, err := sessionapi.ReadManifest(manifestPath(sessionDir))
	if err != nil {
		return fallback
	}
	return m
}

func manifestPath(sessionDir string) string {
	return filepath.Join(sessionDir, "session.json")
}

func tsNow() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

func strRef(s string) *string {
	return &s
}

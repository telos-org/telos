// Package cli implements the telos CLI commands.
package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/telos-org/telos/internal/executor"
	"github.com/telos-org/telos/internal/game"
	"github.com/telos-org/telos/internal/platform"
	"github.com/telos-org/telos/internal/sessionapi"
	"github.com/telos-org/telos/internal/sessionworker"
	"github.com/telos-org/telos/internal/spec"
)

const DefaultLocalModel = "sail-research/zai-org/GLM-5.2-FP8"

// LocalRunConfig holds configuration for local PVG runs.
type LocalRunConfig struct {
	SessionKind     sessionapi.SessionKind
	ParentSessionID *string
	Workspace       string
	Model           string
	Thinking        string
	Until           int
	MaxCostUSD      *float64
	AgentTimeoutSec int
}

// LocalSession holds the result of session creation.
type LocalSession struct {
	SessionID       string
	SessionDir      string
	WorkspaceScope  string
	ActiveWorkspace string
	SpecName        string
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
	sourceWorkspace, scopePath, err := workspaceScope(cfg.Workspace)
	if err != nil {
		return nil, err
	}

	sessionsRoot, err := DefaultLocalSessionRoot(scopePath)
	if err != nil {
		return nil, err
	}

	sessionDir, err := newSessionDir(sessionsRoot)
	if err != nil {
		return nil, err
	}

	workspace, err := prepareSessionWorkspace(sessionDir, sourceWorkspace, compiled, sessionsRoot)
	if err != nil {
		return nil, err
	}
	if err := ensureScopeMarker(scopePath, sessionsRoot); err != nil {
		return nil, err
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
		SessionID:       filepath.Base(sessionDir),
		SessionDir:      sessionDir,
		WorkspaceScope:  scopePath,
		ActiveWorkspace: activeWorkspacePath(sessionDir),
		SpecName:        compiled.Environment.Name,
	}, nil
}

// SubmitLocalSession creates a session and starts its worker in the background.
func SubmitLocalSession(specPath string, cfg LocalRunConfig) (*LocalSession, error) {
	session, err := CreateLocalSession(specPath, cfg)
	if err != nil {
		return nil, err
	}
	if err := sessionworker.Start(session.SessionDir, sessionapi.RuntimeLocal); err != nil {
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

	// Resolve the session's copied spec against the original spec's directory
	// so relative `extends` and `skills` paths point at real files on disk
	// rather than the session's `specs/<name>/` copy.
	specBaseDir := ""
	if manifest.SourceSpecPath != nil && *manifest.SourceSpecPath != "" {
		specBaseDir = filepath.Dir(*manifest.SourceSpecPath)
	}
	compiled, err := spec.CompileEnvironmentWithBase(*sessionSpecPath, specBaseDir)
	if err != nil {
		return nil, err
	}

	specDir := filepath.Dir(*sessionSpecPath)
	state := game.NewPVGState(compiled.Environment.Name, specDir, manifest.SessionID)

	workspace := activeWorkspacePath(sessionDir)
	if err := ensureSessionWorkspace(sessionDir, manifest); err != nil {
		return nil, err
	}

	epochID, err := sessionworker.StartEpoch(sessionDir, manifest)
	if err != nil {
		return nil, err
	}

	var agentExec game.AgentExecutor
	if exec != nil {
		agentExec = exec
	} else {
		agentExec, err = createAgentExecutor(workspace, cfg)
		if err != nil {
			fail := &game.PVGResult{GameResult: game.GameFailure, Error: err.Error()}
			if finishErr := finishEpoch(sessionDir, manifest, fail); finishErr != nil {
				return nil, fmt.Errorf("%w; also failed to finish epoch: %v", err, finishErr)
			}
			return nil, err
		}
	}

	pvgCfg := game.PVGConfig{
		Until:           cfg.Until,
		MaxCostUSD:      cfg.MaxCostUSD,
		Verbose:         true,
		EpochID:         epochID,
		IsController:    controllerPromptEnabled(manifest),
		PrimarySpecPath: primarySpecPath(manifest, sessionSpecPath),
		StopRequested:   func() bool { return sessionStopped(sessionDir) },
	}

	pvg := game.NewPVG(compiled, agentExec, state, pvgCfg)
	result := pvg.Run()

	// Close epoch
	if err := finishEpoch(sessionDir, manifest, result); err != nil {
		return result, err
	}
	if err := cleanupSessionWorkspace(sessionDir, result.WorkspaceCheckpointPath); err != nil {
		fmt.Fprintf(os.Stderr, "warning: cleanup session workspace: %v\n", err)
	}

	return result, nil
}

func controllerPromptEnabled(manifest *sessionapi.Manifest) bool {
	if manifest.SessionKind != sessionapi.KindController {
		return false
	}
	return strings.TrimSpace(os.Getenv("TELOS_CONTROLLER_PROMPT_ENABLED")) == "1"
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

func primarySpecPath(manifest *sessionapi.Manifest, fallback *string) string {
	if manifest != nil && manifest.SessionSpecPath != nil && *manifest.SessionSpecPath != "" {
		return *manifest.SessionSpecPath
	}
	if fallback != nil {
		return *fallback
	}
	return ""
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

func writeLocalManifest(sessionDir string, compiled *spec.CompiledEnvironment, specPath string, state *game.PVGState, cfg LocalRunConfig, workspace *sessionapi.Workspace) error {
	model := cfg.Model
	if model == "" {
		model = DefaultLocalModel
	}
	thinking := cfg.Thinking
	if thinking == "" {
		thinking = "medium"
	}
	sessionKind := cfg.SessionKind
	if sessionKind == "" {
		sessionKind = sessionapi.KindTask
	}

	manifestPath := filepath.Join(sessionDir, "session.json")
	err := sessionapi.WriteInitialManifest(manifestPath, sessionapi.InitialManifest{
		SessionID:       filepath.Base(sessionDir),
		SessionKind:     sessionKind,
		Runtime:         sessionapi.RuntimeLocal,
		CreatedAt:       time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		Launcher:        "local",
		ParentSessionID: cfg.ParentSessionID,
		SourceSpecPath:  &specPath,
		SessionSpecPath: strPtr(state.SpecPath()),
		SpecName:        compiled.Environment.Name,
		Config: sessionapi.SessionConfig{
			Model:           model,
			Until:           cfg.Until,
			MaxCostUSD:      cfg.MaxCostUSD,
			AgentTimeoutSec: cfg.AgentTimeoutSec,
			Thinking:        thinking,
		},
		Workspace:  workspace,
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
		Model:           cfg.Model,
		Thinking:        cfg.Thinking,
		Until:           cfg.Until,
		MaxCostUSD:      cfg.MaxCostUSD,
		AgentTimeoutSec: cfg.AgentTimeoutSec,
	}
	if lrc.Thinking == "" {
		lrc.Thinking = "medium"
	}
	return lrc
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

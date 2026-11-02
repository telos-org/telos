// Package cli implements the telos CLI commands.
package cli

import (
	"crypto/sha256"
	"errors"
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

const (
	DefaultLocalModel    = "openai-codex/gpt-5.5"
	DefaultLocalThinking = "high"
)

// LocalRunConfig holds configuration for local PVG runs.
type LocalRunConfig struct {
	Workspace       string
	Model           string
	Thinking        string
	Until           int
	UntilSeconds    int
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

	workspace, err := prepareSessionWorkspace(sessionDir, sourceWorkspace)
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
	version := 1
	specVersions := []map[string]any{{
		"version":     version,
		"revision":    compiled.Environment.Version,
		"spec_path":   state.SpecPath(),
		"spec_sha256": specDataSHA256(data),
		"created_at":  time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
	}}
	if err := writeLocalManifest(sessionDir, compiled, absSpec, state.SpecPath(), state, cfg, workspace, strPtr(compiled.Environment.Version), &version, specVersions); err != nil {
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
	if _, err := sessionworker.ResolveTelosd(); err != nil {
		return nil, err
	}
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
	// so relative skill paths point at real files on disk rather than the
	// session's `specs/<name>/` copy.
	specBaseDir := ""
	if manifest.SourceSpecPath != nil && *manifest.SourceSpecPath != "" {
		specBaseDir = filepath.Dir(*manifest.SourceSpecPath)
	}
	compileSpecPath, specBaseDir := boundSpecPaths(
		manifest,
		*sessionSpecPath,
		specBaseDir,
	)
	compiled, err := spec.CompileEnvironmentWithBase(compileSpecPath, specBaseDir)
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
		if errors.Is(err, sessionworker.ErrSessionStopped) {
			return &game.PVGResult{
				GameResult: game.GameStopped,
				Error:      "stopped by operator",
			}, nil
		}
		return nil, err
	}

	var agentExec game.AgentExecutor
	if exec != nil {
		agentExec = exec
	} else {
		agentExec, err = createAgentExecutor(workspace, cfg)
		if err != nil {
			fail := &game.PVGResult{GameResult: game.GameFailure, Error: err.Error()}
			if finishErr := finishEpoch(sessionDir, epochID, fail); finishErr != nil {
				return nil, fmt.Errorf("%w; also failed to finish epoch: %v", err, finishErr)
			}
			return nil, err
		}
	}

	if pi, ok := agentExec.(*executor.PiExecutor); ok {
		agentExec = &sessionInferenceExecutor{PiExecutor: pi, sessionDir: sessionDir}
	}

	pvgCfg := game.PVGConfig{
		Until:           cfg.Until,
		UntilSeconds:    cfg.UntilSeconds,
		MaxCostUSD:      cfg.MaxCostUSD,
		Verbose:         true,
		EpochID:         epochID,
		IsController:    controllerPromptEnabled(manifest),
		PrimarySpecPath: compileSpecPath,
		LocalRuntime:    manifest.ResolvedRuntime(sessionapi.SessionRuntime(os.Getenv("TELOS_RUNTIME"))) == sessionapi.RuntimeLocal,
		StopRequested:   func() bool { return agentsShouldStop(sessionDir) },
		RecordBlock:     func(message string) string { return recordBlock(sessionDir, manifest, message) },
	}

	if pvgCfg.IsController {
		// Only the root's own turns show that what it retries works again.
		pvgCfg.OnTurnSucceeded = func() { ClearRetryingIssue(sessionDir) }
	}
	pvg := game.NewPVG(compiled, agentExec, state, pvgCfg)
	result := pvg.Run()

	// Close epoch
	if err := finishEpoch(sessionDir, epochID, result); err != nil {
		return result, err
	}
	if manifest.SessionKind != sessionapi.KindController {
		if err := cleanupSessionWorkspace(sessionDir, result.WorkspaceCheckpointPath); err != nil {
			fmt.Fprintf(os.Stderr, "warning: cleanup session workspace: %v\n", err)
		}
	}

	return result, nil
}

func boundSpecPaths(manifest *sessionapi.Manifest, fallbackPath, fallbackBase string) (string, string) {
	versionNumber := boundSpecVersion(manifest)
	if versionNumber == nil {
		return fallbackPath, fallbackBase
	}
	for _, version := range manifest.SpecVersions {
		if numericMapValue(version, "version") != *versionNumber {
			continue
		}
		if path, _ := version["spec_path"].(string); path != "" {
			fallbackPath = path
		}
		if packageSpec, _ := version["package_spec_path"].(string); packageSpec != "" {
			fallbackBase = filepath.Dir(packageSpec)
		}
		return fallbackPath, fallbackBase
	}
	return fallbackPath, fallbackBase
}

func boundSpecVersion(manifest *sessionapi.Manifest) *int {
	if manifest == nil {
		return nil
	}
	if open := manifest.OpenEpoch(); open != nil && open.SpecVersion != nil {
		return open.SpecVersion
	}
	return manifest.CurrentSpecVersion
}

func numericMapValue(values map[string]any, key string) int {
	switch value := values[key].(type) {
	case int:
		return value
	case float64:
		return int(value)
	default:
		return 0
	}
}

func controllerPromptEnabled(manifest *sessionapi.Manifest) bool {
	return manifest.SessionKind == sessionapi.KindController
}

func createPiExecutor(workspace string, cfg LocalRunConfig) (*executor.PiExecutor, error) {
	if _, err := exec.LookPath("pi"); err != nil {
		return nil, fmt.Errorf("local runs use the pi coding agent, but `pi` is not on your PATH; install it with `npm install -g @earendil-works/pi-coding-agent`, then run `pi` and use `/login` to configure model credentials")
	}
	p := platform.NewLocalPlatform(workspace)
	model := cfg.Model
	if model == "" {
		model = DefaultLocalModel
	}
	if err := validatePiModel(model); err != nil {
		return nil, err
	}
	thinking := cfg.Thinking
	if thinking == "" {
		thinking = DefaultLocalThinking
	}
	return executor.NewPiExecutor(p, model, thinking, cfg.AgentTimeoutSec), nil
}

func validatePiModel(model string) error {
	providerName, modelID, ok := strings.Cut(model, "/")
	if !ok || providerName == "" || modelID == "" {
		return fmt.Errorf("pi model %q must use <provider>/<model-id>; choose one with `telos run SPEC.md --model <provider>/<model-id>` or set `TELOS_MODEL=<provider>/<model-id>`", model)
	}

	// models.json supplements Pi's built-in catalog and Telos model
	// definitions, so Pi itself reports unknown models.
	return nil
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

func specDataSHA256(data []byte) string {
	hash := sha256.Sum256(data)
	return fmt.Sprintf("%x", hash)
}

func writeLocalManifest(sessionDir string, compiled *spec.CompiledEnvironment, sourceSpecPath string, sessionSpecPath string, state *game.PVGState, cfg LocalRunConfig, workspace *sessionapi.Workspace, currentRevision *string, currentSpecVersion *int, specVersions []map[string]any) error {
	model := cfg.Model
	if model == "" {
		model = DefaultLocalModel
	}
	thinking := cfg.Thinking
	if thinking == "" {
		thinking = DefaultLocalThinking
	}
	manifestPath := filepath.Join(sessionDir, "session.json")
	err := sessionapi.WriteInitialManifest(manifestPath, sessionapi.InitialManifest{
		SessionID:          filepath.Base(sessionDir),
		SessionKind:        sessionapi.KindTask,
		Runtime:            sessionapi.RuntimeLocal,
		CreatedAt:          time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		SourceSpecPath:     &sourceSpecPath,
		SessionSpecPath:    &sessionSpecPath,
		SpecName:           compiled.Environment.Name,
		CurrentRevision:    currentRevision,
		CurrentSpecVersion: currentSpecVersion,
		SpecVersions:       specVersions,
		Config: sessionapi.SessionConfig{
			Model:           model,
			Until:           cfg.Until,
			UntilSeconds:    cfg.UntilSeconds,
			MaxCostUSD:      cfg.MaxCostUSD,
			AgentTimeoutSec: cfg.AgentTimeoutSec,
			Thinking:        thinking,
		},
		Workspace: workspace,
		Specs: []sessionapi.InitialManifestSpec{{
			Index:           0,
			Name:            compiled.Environment.Name,
			DirName:         compiled.Environment.Name,
			SessionSpecPath: &sessionSpecPath,
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
		UntilSeconds:    cfg.UntilSeconds,
		MaxCostUSD:      cfg.MaxCostUSD,
		AgentTimeoutSec: cfg.AgentTimeoutSec,
	}
	if lrc.Thinking == "" {
		lrc.Thinking = DefaultLocalThinking
	}
	return lrc
}

func finishEpoch(sessionDir string, epochID int, result *game.PVGResult) error {
	_, err := sessionapi.MutateManifest(manifestPath(sessionDir), func(manifest *sessionapi.Manifest) error {
		var epoch *sessionapi.Epoch
		for i := range manifest.Epochs {
			if manifest.Epochs[i].ID == epochID {
				epoch = &manifest.Epochs[i]
				break
			}
		}
		if epoch == nil {
			return fmt.Errorf("epoch %d not found", epochID)
		}
		externallyStopped := epoch.Result != nil && *epoch.Result == "stopped"
		finishedAt := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
		epoch.FinishedAt = &finishedAt
		if !externallyStopped {
			epoch.CompletionReason = stringPtr(result.CompletionReason)
			epoch.VerifierConceded = boolPtr(result.VerifierConceded)
		}
		epoch.RoundCount = intPtr(result.Rounds)
		checkpointSaved := result.WorkspaceCheckpointPath != ""
		epoch.CheckpointSaved = &checkpointSaved
		epoch.CheckpointPath = stringPtr(result.WorkspaceCheckpointPath)
		epoch.CheckpointBytes = fileSize(result.WorkspaceCheckpointPath)
		if externallyStopped {
			return nil
		}

		switch result.GameResult {
		case game.GameSuccess:
			completed := "completed"
			epoch.Result = &completed
		case game.GameFailure:
			failed := "failed"
			epoch.Result = &failed
			if result.Error != "" {
				epoch.Error = &result.Error
			}
		case game.GameBlocked:
			blocked := "blocked"
			epoch.Result = &blocked
			epoch.Error = &result.BlockedMessage
		case game.GameStopped:
			stopped, reason := "stopped", "stopped by operator"
			if !manifest.IsStopped() {
				stopped, reason = sessionapi.EpochHeld, "agents held"
			}
			epoch.Result = &stopped
			if result.Error != "" {
				reason = result.Error
			}
			epoch.Error = &reason
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("finish epoch: %w", err)
	}
	return nil
}

func stringPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func boolPtr(value bool) *bool {
	return &value
}

func intPtr(value int) *int {
	return &value
}

func fileSize(path string) *int64 {
	if path == "" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	size := info.Size()
	return &size
}

func sessionStopped(sessionDir string) bool {
	manifest, err := sessionapi.ReadManifest(manifestPath(sessionDir))
	if err != nil {
		return false
	}
	return manifest.IsStopped()
}

// ClearRetryingIssue ends the root's retrying issue: an agent turn succeeded,
// or agents stopped for a hold.
func ClearRetryingIssue(sessionDir string) {
	root, rootDir := sessionapi.ReadRootManifest(sessionDir)
	if root == nil || root.Issue == nil || root.Issue.Status != sessionapi.IssueRetrying {
		return
	}
	_, _ = sessionapi.MutateManifest(manifestPath(rootDir), func(m *sessionapi.Manifest) error {
		if m.Issue != nil && m.Issue.Status == sessionapi.IssueRetrying {
			m.Issue = nil
		}
		return nil
	})
}

// recordBlock sets a blocked issue on the root and returns its reason, unless
// the spec changed since started was read: a block of a replaced spec would
// outlive the new one.
func recordBlock(sessionDir string, started *sessionapi.Manifest, message string) string {
	recorded := false
	_, err := sessionapi.MutateManifest(manifestPath(sessionDir), func(m *sessionapi.Manifest) error {
		if equalPtr(m.CurrentSpecVersion, started.CurrentSpecVersion) && equalPtr(m.PackageDigest, started.PackageDigest) {
			m.Issue = sessionapi.NewIssue(sessionapi.IssueBlocked, sessionapi.ReasonSpecUnsatisfiable, message, time.Now())
			recorded = true
		}
		return nil
	})
	if err != nil || !recorded {
		return ""
	}
	return sessionapi.ReasonSpecUnsatisfiable
}

func equalPtr[T comparable](a, b *T) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

// agentsShouldStop reports an operator stop of this session, or a hold on
// its root that stops every agent in the goal.
func agentsShouldStop(sessionDir string) bool {
	root, _ := sessionapi.ReadRootManifest(sessionDir)
	return sessionStopped(sessionDir) || root.OnHold()
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

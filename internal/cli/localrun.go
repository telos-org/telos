// Package cli implements the telos CLI commands.
package cli

import (
	"crypto/sha256"
	"encoding/json"
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
	SessionKind     sessionapi.SessionKind
	ParentSessionID *string
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
	sessionKind := localSessionKind(cfg)
	if sessionKind == sessionapi.KindController && (cfg.Until > 0 || cfg.UntilSeconds > 0) {
		return nil, fmt.Errorf("controller sessions do not support per-run duration bounds")
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
	sourceSpecPath := absSpec
	sessionSpecPath := state.SpecPath()
	var currentRevision *string
	var currentSpecVersion *int
	var specVersions []map[string]any
	var packageDigest *string
	var applyPackageLock *spec.ApplyPackageManifest
	if sessionKind == sessionapi.KindController && cfg.ParentSessionID == nil {
		revision, err := materializeLocalControllerRevision(sessionDir, compiled, state.SpecPath(), data)
		if err != nil {
			return nil, err
		}
		sourceSpecPath = revision.PackageSpecPath
		sessionSpecPath = revision.ActiveSpecPath
		currentRevision = strPtr(revision.Version)
		version := 1
		currentSpecVersion = &version
		packageDigest = strPtr(revision.PackageDigest)
		applyPackageLock = revision.ApplyPackageLock
		specVersions = []map[string]any{{
			"version":           version,
			"revision":          revision.Version,
			"spec_path":         revision.SpecPath,
			"spec_sha256":       specDataSHA256(data),
			"package_digest":    revision.PackageDigest,
			"package_path":      revision.PackagePath,
			"package_spec_path": revision.PackageSpecPath,
			"active_spec_path":  revision.ActiveSpecPath,
			"provenance":        map[string]any{"type": "inline"},
			"created_at":        time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		}}
	} else {
		if err := os.WriteFile(state.SpecPath(), data, 0o644); err != nil {
			return nil, fmt.Errorf("write session spec: %w", err)
		}
	}

	if err := writeLocalManifest(sessionDir, compiled, sourceSpecPath, sessionSpecPath, state, cfg, workspace, currentRevision, currentSpecVersion, specVersions, packageDigest, applyPackageLock); err != nil {
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
		UntilSeconds:    cfg.UntilSeconds,
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
	if manifest.SessionKind != sessionapi.KindController {
		if err := cleanupSessionWorkspace(sessionDir, result.WorkspaceCheckpointPath); err != nil {
			fmt.Fprintf(os.Stderr, "warning: cleanup session workspace: %v\n", err)
		}
	}

	return result, nil
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

type piModelsConfig struct {
	Providers map[string]piProvider `json:"providers"`
}

type piProvider struct {
	Models []piModel `json:"models"`
}

type piModel struct {
	ID string `json:"id"`
}

func validatePiModel(model string) error {
	providerName, modelID, ok := strings.Cut(model, "/")
	if !ok || providerName == "" || modelID == "" {
		return fmt.Errorf("pi model %q must use <provider>/<model-id>; choose one with `telos run SPEC.md --model <provider>/<model-id>` or set `TELOS_MODEL=<provider>/<model-id>`", model)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(home, ".pi", "agent", "models.json"))
	if err != nil {
		return nil
	}

	var config piModelsConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil
	}
	provider, configured := config.Providers[providerName]
	if !configured {
		return nil
	}
	for _, configuredModel := range provider.Models {
		if configuredModel.ID == modelID {
			return nil
		}
	}
	return fmt.Errorf("pi model %q is not configured: provider %q exists in ~/.pi/agent/models.json, but model id %q was not found; choose one with `telos run SPEC.md --model <provider>/<model-id>` or set `TELOS_MODEL=<provider>/<model-id>`", model, providerName, modelID)
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

type localControllerRevision struct {
	Version          string
	SpecPath         string
	PackagePath      string
	PackageSpecPath  string
	ActiveSpecPath   string
	PackageDigest    string
	ApplyPackageLock *spec.ApplyPackageManifest
}

type localRevisionMetadata struct {
	Version         string `json:"version"`
	Sequence        int    `json:"sequence"`
	SpecSHA256      string `json:"spec_sha256"`
	PackageDigest   string `json:"package_digest,omitempty"`
	SpecPath        string `json:"spec_path"`
	PackagePath     string `json:"package_path,omitempty"`
	PackageSpecPath string `json:"package_spec_path,omitempty"`
	ActiveSpecPath  string `json:"active_spec_path"`
	CreatedAt       string `json:"created_at"`
}

func materializeLocalControllerRevision(sessionDir string, compiled *spec.CompiledEnvironment, activeSpecPath string, specData []byte) (localControllerRevision, error) {
	pkg, err := spec.BuildApplyPackage(compiled)
	if err != nil {
		return localControllerRevision{}, err
	}
	version := compiled.Environment.Version
	revisionDir := filepath.Join(sessionDir, "revisions", version)
	packagePath := filepath.Join(revisionDir, "package")
	manifest := pkg.Manifest
	revision := localControllerRevision{
		Version:          version,
		SpecPath:         filepath.Join(revisionDir, "SPEC.md"),
		PackagePath:      packagePath,
		PackageSpecPath:  filepath.Join(packagePath, "SPEC.md"),
		ActiveSpecPath:   activeSpecPath,
		PackageDigest:    pkg.Digest,
		ApplyPackageLock: &manifest,
	}
	if err := os.MkdirAll(revisionDir, 0o755); err != nil {
		return localControllerRevision{}, err
	}
	if err := os.WriteFile(revision.SpecPath, specData, 0o644); err != nil {
		return localControllerRevision{}, fmt.Errorf("write revision spec: %w", err)
	}
	if _, err := spec.ExtractApplyPackage(pkg.Bytes, packagePath); err != nil {
		return localControllerRevision{}, fmt.Errorf("extract revision package: %w", err)
	}
	if err := writeLocalRevisionMetadata(filepath.Join(revisionDir, "revision.json"), localRevisionMetadata{
		Version:         version,
		Sequence:        1,
		SpecSHA256:      specDataSHA256(specData),
		PackageDigest:   pkg.Digest,
		SpecPath:        revision.SpecPath,
		PackagePath:     revision.PackagePath,
		PackageSpecPath: revision.PackageSpecPath,
		ActiveSpecPath:  revision.ActiveSpecPath,
		CreatedAt:       time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
	}); err != nil {
		return localControllerRevision{}, fmt.Errorf("write revision metadata: %w", err)
	}
	if err := replaceLocalSymlink(filepath.Join(sessionDir, "revisions", "current"), version); err != nil {
		return localControllerRevision{}, fmt.Errorf("update current revision link: %w", err)
	}
	specTarget, err := filepath.Rel(filepath.Dir(activeSpecPath), filepath.Join(sessionDir, "revisions", "current", "SPEC.md"))
	if err != nil {
		return localControllerRevision{}, err
	}
	if err := replaceLocalSymlink(activeSpecPath, specTarget); err != nil {
		return localControllerRevision{}, fmt.Errorf("update active spec link: %w", err)
	}
	if err := replaceLocalSymlink(filepath.Join(sessionDir, "package"), filepath.Join("revisions", "current", "package")); err != nil {
		return localControllerRevision{}, fmt.Errorf("update active package link: %w", err)
	}
	return revision, nil
}

func writeLocalRevisionMetadata(path string, metadata localRevisionMetadata) error {
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

func replaceLocalSymlink(path string, target string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp")
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	if err := os.RemoveAll(path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func specDataSHA256(data []byte) string {
	hash := sha256.Sum256(data)
	return fmt.Sprintf("%x", hash)
}

func localSessionKind(cfg LocalRunConfig) sessionapi.SessionKind {
	if cfg.SessionKind != "" {
		return cfg.SessionKind
	}
	return sessionapi.KindTask
}

func writeLocalManifest(sessionDir string, compiled *spec.CompiledEnvironment, sourceSpecPath string, sessionSpecPath string, state *game.PVGState, cfg LocalRunConfig, workspace *sessionapi.Workspace, currentRevision *string, currentSpecVersion *int, specVersions []map[string]any, packageDigest *string, applyPackageLock *spec.ApplyPackageManifest) error {
	model := cfg.Model
	if model == "" {
		model = DefaultLocalModel
	}
	thinking := cfg.Thinking
	if thinking == "" {
		thinking = DefaultLocalThinking
	}
	sessionKind := localSessionKind(cfg)

	manifestPath := filepath.Join(sessionDir, "session.json")
	err := sessionapi.WriteInitialManifest(manifestPath, sessionapi.InitialManifest{
		SessionID:          filepath.Base(sessionDir),
		SessionKind:        sessionKind,
		Runtime:            sessionapi.RuntimeLocal,
		CreatedAt:          time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		Launcher:           "local",
		ParentSessionID:    cfg.ParentSessionID,
		SourceSpecPath:     &sourceSpecPath,
		SessionSpecPath:    &sessionSpecPath,
		SpecName:           compiled.Environment.Name,
		CurrentRevision:    currentRevision,
		CurrentSpecVersion: currentSpecVersion,
		SpecVersions:       specVersions,
		PackageDigest:      packageDigest,
		ApplyPackageLock:   applyPackageLock,
		Config: sessionapi.SessionConfig{
			Model:           model,
			Until:           cfg.Until,
			UntilSeconds:    cfg.UntilSeconds,
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

func finishEpoch(sessionDir string, manifest *sessionapi.Manifest, result *game.PVGResult) error {
	_, err := sessionapi.MutateManifest(manifestPath(sessionDir), func(manifest *sessionapi.Manifest) error {
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
		return nil
	})
	if err != nil {
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

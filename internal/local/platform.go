package local

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/telos-org/telos-go/internal/cli"
	"github.com/telos-org/telos-go/internal/sessionapi"
)

// LocalPlatform is a sessionapi.Store backed by detached subprocess workers.
// Creating a session spawns a `telos worker` process that runs the PVG loop
// independently, so a session outlives the daemon that launched it. Read
// operations are served by the embedded FileStore.
type LocalPlatform struct {
	*sessionapi.FileStore
	sessionsRoot string
	spawn        func(sessionDir string) error
}

// NewLocalPlatform returns a LocalPlatform rooted at a sessions directory.
func NewLocalPlatform(sessionsRoot string) *LocalPlatform {
	return &LocalPlatform{
		FileStore:    sessionapi.NewFileStore(sessionsRoot),
		sessionsRoot: sessionsRoot,
		spawn:        spawnWorker,
	}
}

// Create compiles the requested spec, persists a session, and launches a
// detached worker to run it.
func (p *LocalPlatform) Create(req sessionapi.SessionCreateRequest) (*sessionapi.Session, error) {
	if req.SpecPath == nil || *req.SpecPath == "" {
		return nil, fmt.Errorf("missing field: spec_path")
	}
	return p.Submit(*req.SpecPath, configFromRequest(req))
}

// Submit creates a local session from a spec file and spawns its worker.
func (p *LocalPlatform) Submit(specPath string, cfg cli.LocalRunConfig) (*sessionapi.Session, error) {
	cfg.SessionsRoot = p.sessionsRoot
	session, err := cli.CreateLocalSession(specPath, cfg)
	if err != nil {
		return nil, err
	}
	if err := p.spawn(session.SessionDir); err != nil {
		return nil, err
	}
	return p.FileStore.Get(session.SessionID)
}

// Stop terminates a session's worker process tree, then marks it stopped.
func (p *LocalPlatform) Stop(id string) (*sessionapi.Session, error) {
	manifestPath := filepath.Join(p.sessionsRoot, id, "session.json")
	if m, err := sessionapi.ReadManifest(manifestPath); err == nil {
		if open := m.OpenEpoch(); open != nil && open.Runner != nil {
			terminateProcessTree(open.Runner.PID)
		}
	}
	return p.FileStore.Stop(id)
}

func configFromRequest(req sessionapi.SessionCreateRequest) cli.LocalRunConfig {
	cfg := cli.LocalRunConfig{
		Model:      req.Model,
		Thinking:   req.Thinking,
		MaxCostUSD: req.MaxCostUSD,
	}
	if req.Workspace != nil {
		cfg.Workspace = *req.Workspace
	}
	if req.MaxRounds != nil {
		cfg.MaxRounds = *req.MaxRounds
	}
	if req.AgentTimeoutSec != nil {
		cfg.AgentTimeoutSec = *req.AgentTimeoutSec
	}
	return cfg
}

// spawnWorker launches a detached `telos worker` process for a session. The
// worker is started in its own session (setsid) so it survives the daemon,
// with stdio routed to the session's runner.log.
func spawnWorker(sessionDir string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate telos binary: %w", err)
	}
	logFile, err := os.OpenFile(
		filepath.Join(sessionDir, "runner.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open runner log: %w", err)
	}
	defer logFile.Close()
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}
	defer devNull.Close()

	cmd := exec.Command(exe, "worker", sessionDir)
	cmd.Stdin = devNull
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawn worker: %w", err)
	}
	return cmd.Process.Release()
}

package sessionworker

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/telos-org/telos/internal/sessionapi"
)

type StartOptions struct {
	Runtime    sessionapi.SessionRuntime
	WakeReason string
}

func Start(sessionDir string, runtime sessionapi.SessionRuntime) error {
	return StartWithOptions(sessionDir, StartOptions{Runtime: runtime})
}

func StartWithOptions(sessionDir string, opts StartOptions) error {
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
	cmd.Env = Env(sessionDir, opts)
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start worker: %w", err)
	}
	if err := MarkStarted(sessionDir, cmd.Process.Pid, logPath); err != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		_ = cmd.Process.Kill()
		return err
	}
	return nil
}

func Env(sessionDir string, opts StartOptions) []string {
	runtime := opts.Runtime
	if runtime == "" {
		runtime = sessionapi.RuntimeLocal
	}
	env := append(os.Environ(),
		"TELOS_RUNTIME="+string(runtime),
		"TELOS_SESSION_DIR="+filepath.Dir(sessionDir),
		"TELOS_SESSION_ID="+filepath.Base(sessionDir),
	)
	manifest, err := sessionapi.ReadManifest(manifestPath(sessionDir))
	if err == nil {
		if manifest.ParentSessionID != nil {
			env = append(env, "TELOS_PARENT_SESSION_ID="+*manifest.ParentSessionID)
		}
		if manifest.Access != nil && manifest.Access.APIToken != "" {
			env = append(env, "TELOS_API_TOKEN="+manifest.Access.APIToken)
		}
	}
	if opts.WakeReason != "" {
		env = append(env,
			"TELOS_WAKE_REASON="+opts.WakeReason,
			"TELOS_WAKE_ID="+opts.WakeReason+":"+time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		)
	}
	return env
}

func Stop(sessionDir string) error {
	manifest, err := sessionapi.ReadManifest(manifestPath(sessionDir))
	if err != nil {
		return fmt.Errorf("read session manifest: %w", err)
	}
	open := manifest.OpenEpoch()
	if open == nil || open.Runner == nil {
		return nil
	}
	pid, ok := open.Runner.ProcessID()
	if !ok || pid == os.Getpid() {
		return nil
	}
	group := pid
	if pgid, ok := open.Runner.ProcessGroupID(); ok {
		group = pgid
	}
	if err := syscall.Kill(-group, syscall.SIGTERM); err == nil || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("stop worker process %d: %w", pid, err)
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

func StartEpoch(sessionDir string, manifest *sessionapi.Manifest) (int, error) {
	return StartEpochWithRunner(sessionDir, manifest, os.Getpid(), "")
}

func MarkStarted(sessionDir string, pid int, logPath string) error {
	manifest, err := sessionapi.ReadManifest(manifestPath(sessionDir))
	if err != nil {
		return fmt.Errorf("read session manifest: %w", err)
	}
	_, err = StartEpochWithRunner(sessionDir, manifest, pid, logPath)
	return err
}

func StartEpochWithRunner(sessionDir string, manifest *sessionapi.Manifest, pid int, logPath string) (int, error) {
	if open := manifest.OpenEpoch(); open != nil {
		return open.ID, nil
	}
	epochID := len(manifest.Epochs) + 1
	runner := RunnerIdentity(pid)
	if logPath != "" {
		runner.LogPath = logPath
	}
	manifest.Epochs = append(manifest.Epochs, sessionapi.Epoch{
		ID:        epochID,
		StartedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		Runner:    &runner,
	})
	if err := sessionapi.WriteManifest(manifestPath(sessionDir), manifest); err != nil {
		return 0, fmt.Errorf("start epoch: %w", err)
	}
	return epochID, nil
}

func RunnerIdentity(pid int) sessionapi.Runner {
	return sessionapi.Runner{
		Kind:      "local-subprocess",
		PID:       pid,
		PGID:      pid,
		StartedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
	}
}

func manifestPath(sessionDir string) string {
	return filepath.Join(sessionDir, "session.json")
}

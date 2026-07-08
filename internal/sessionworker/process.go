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

var ErrWorkerNotRunning = errors.New("worker is not running")
var ErrWorkerAlreadyRunning = errors.New("worker is already running")

type Ownership struct {
	file *os.File
}

func (o *Ownership) Release() error {
	if o == nil || o.file == nil {
		return nil
	}
	err := syscall.Flock(int(o.file.Fd()), syscall.LOCK_UN)
	closeErr := o.file.Close()
	o.file = nil
	if err != nil {
		return err
	}
	return closeErr
}

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
	alive, err := workerAlive(sessionDir)
	if err != nil {
		return err
	}
	if !alive {
		return nil
	}
	manifest, err := sessionapi.ReadManifest(manifestPath(sessionDir))
	if err != nil {
		return fmt.Errorf("read session manifest: %w", err)
	}
	if manifest.Runner == nil {
		return fmt.Errorf("worker is alive but runner is not recorded")
	}
	pid, ok := manifest.Runner.ProcessID()
	if !ok || pid == os.Getpid() {
		return nil
	}
	group := pid
	if pgid, ok := manifest.Runner.ProcessGroupID(); ok {
		group = pgid
	}
	if err := syscall.Kill(-group, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	if err := waitWorkerExit(sessionDir, 10*time.Second); err == nil {
		return clearRunner(sessionDir, pid)
	}
	if err := syscall.Kill(-group, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	if err := waitWorkerExit(sessionDir, 5*time.Second); err != nil {
		return fmt.Errorf("stop worker process %d: %w", pid, err)
	}
	return clearRunner(sessionDir, pid)
}

func Wake(sessionDir string) error {
	alive, err := workerAlive(sessionDir)
	if err != nil {
		return err
	}
	if !alive {
		return ErrWorkerNotRunning
	}
	manifest, err := sessionapi.ReadManifest(manifestPath(sessionDir))
	if err != nil {
		return fmt.Errorf("read session manifest: %w", err)
	}
	if manifest.Runner == nil {
		return nil
	}
	pid, ok := manifest.Runner.ProcessID()
	if !ok || pid == os.Getpid() {
		return nil
	}
	if err := syscall.Kill(pid, syscall.SIGUSR1); err == nil {
		return nil
	} else if !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("wake worker process %d: %w", pid, err)
	}
	return ErrWorkerNotRunning
}

func AcquireOwnership(sessionDir string, logPath string) (*Ownership, error) {
	lock, err := openRunnerLock(sessionDir)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		if isWouldBlock(err) {
			return nil, ErrWorkerAlreadyRunning
		}
		return nil, fmt.Errorf("acquire runner lock: %w", err)
	}
	runner := RunnerIdentity(os.Getpid())
	if logPath != "" {
		runner.LogPath = logPath
	}
	if _, err := sessionapi.MutateManifest(manifestPath(sessionDir), func(m *sessionapi.Manifest) error {
		m.Runner = &runner
		return nil
	}); err != nil {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		lock.Close()
		return nil, fmt.Errorf("record runner: %w", err)
	}
	return &Ownership{file: lock}, nil
}

func workerAlive(sessionDir string) (bool, error) {
	lock, err := openRunnerLock(sessionDir)
	if err != nil {
		return false, err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		if isWouldBlock(err) {
			return true, nil
		}
		return false, fmt.Errorf("probe runner lock: %w", err)
	}
	_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return false, nil
}

func openRunnerLock(sessionDir string) (*os.File, error) {
	if sessionDir == "" {
		return nil, fmt.Errorf("session dir is required")
	}
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return nil, err
	}
	return os.OpenFile(filepath.Join(sessionDir, "runner.lock"), os.O_CREATE|os.O_RDWR, 0o600)
}

func isWouldBlock(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
}

func clearRunner(sessionDir string, pid int) error {
	_, err := sessionapi.MutateManifest(manifestPath(sessionDir), func(m *sessionapi.Manifest) error {
		if runnerPID, ok := m.Runner.ProcessID(); ok && runnerPID == pid {
			m.Runner = nil
		}
		return nil
	})
	return err
}

func waitWorkerExit(sessionDir string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		alive, err := workerAlive(sessionDir)
		if err != nil {
			return err
		}
		if !alive {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for worker exit")
		}
		time.Sleep(100 * time.Millisecond)
	}
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
	logPath := ""
	if manifest != nil && manifest.Runner != nil {
		logPath = manifest.Runner.LogPath
	}
	return StartEpochWithRunner(sessionDir, manifest, os.Getpid(), logPath)
}

func StartEpochWithRunner(sessionDir string, manifest *sessionapi.Manifest, pid int, logPath string) (int, error) {
	var epochID int
	_, err := sessionapi.MutateManifest(manifestPath(sessionDir), func(m *sessionapi.Manifest) error {
		if open := m.OpenEpoch(); open != nil {
			epochID = open.ID
			return nil
		}
		epochID = len(m.Epochs) + 1
		runner := RunnerIdentity(pid)
		if logPath != "" {
			runner.LogPath = logPath
		}
		m.Epochs = append(m.Epochs, sessionapi.Epoch{
			ID:        epochID,
			StartedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
			Runner:    &runner,
		})
		return nil
	})
	if err != nil {
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

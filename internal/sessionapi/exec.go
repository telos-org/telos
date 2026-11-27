package sessionapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

const (
	DefaultExecTimeoutMS = 30_000
	MaxExecTimeoutMS     = 300_000
	MaxExecOutputBytes   = 1 << 20
)

type SessionExecRequest struct {
	Argv      []string `json:"argv"`
	TimeoutMS int      `json:"timeout_ms,omitempty"`
}

func (r *SessionExecRequest) Validate() error {
	if len(r.Argv) == 0 || len(r.Argv) > 256 || r.Argv[0] == "" {
		return fmt.Errorf("provide a command and at most 255 arguments: %w", ErrInvalidSession)
	}
	size := 0
	for _, arg := range r.Argv {
		size += len(arg)
		if !utf8.ValidString(arg) {
			return fmt.Errorf("command arguments must be valid UTF-8: %w", ErrInvalidSession)
		}
		if strings.ContainsRune(arg, 0) {
			return fmt.Errorf("command arguments cannot contain NUL bytes: %w", ErrInvalidSession)
		}
	}
	if size > 128<<10 {
		return fmt.Errorf("command arguments exceed 128 KiB: %w", ErrInvalidSession)
	}
	if r.TimeoutMS == 0 {
		r.TimeoutMS = DefaultExecTimeoutMS
	}
	if r.TimeoutMS < 1 || r.TimeoutMS > MaxExecTimeoutMS {
		return fmt.Errorf("command timeout must be between 1 ms and 5 minutes: %w", ErrInvalidSession)
	}
	return nil
}

type SessionExecResponse struct {
	Stdout          []byte `json:"stdout_base64"`
	Stderr          []byte `json:"stderr_base64"`
	ExitCode        int    `json:"exit_code"`
	TimedOut        bool   `json:"timed_out"`
	StdoutTruncated bool   `json:"stdout_truncated"`
	StderrTruncated bool   `json:"stderr_truncated"`
}

// LockWorkspace excludes other agent turns and operator commands until the
// returned file is closed. Each session has its own workspace and lock.
func LockWorkspace(sessionDir string) (*os.File, error) {
	return lockWorkspace(sessionDir, syscall.LOCK_EX)
}

func lockWorkspace(sessionDir string, flags int) (*os.File, error) {
	file, err := os.OpenFile(filepath.Join(sessionDir, "workspace.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), flags); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func (fs *FileStore) Exec(ctx context.Context, id string, request SessionExecRequest) (*SessionExecResponse, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if !filepath.IsLocal(id) || filepath.Base(id) != id {
		return nil, fmt.Errorf("invalid Goal ID: %w", ErrInvalidSession)
	}
	dir, err := filepath.Abs(fs.sessionDir(id))
	if err != nil {
		return nil, err
	}
	if _, err := pausedExecManifest(dir); err != nil {
		return nil, err
	}
	lock, err := lockWorkspace(dir, syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return nil, fmt.Errorf("the Goal's workspace is busy; retry after the current work finishes: %w", ErrConflict)
	}
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	manifest, err := pausedExecManifest(dir)
	if err != nil {
		return nil, err
	}
	workspace := filepath.Join(dir, "workspace")
	info, err := os.Stat(workspace)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("the Goal's workspace is unavailable: %w", ErrConflict)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(request.TimeoutMS)*time.Millisecond)
	defer cancel()
	command := exec.CommandContext(ctx, request.Argv[0], request.Argv[1:]...)
	command.Dir = workspace
	command.Env = execEnvironment(dir, manifest, fs.runtime)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	command.WaitDelay = 100 * time.Millisecond
	var stdout, stderr execOutput
	command.Stdout, command.Stderr = &stdout, &stderr
	err = command.Run()
	if command.Process != nil {
		// Clean up the process group before releasing the workspace.
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
	result := &SessionExecResponse{
		Stdout: append([]byte{}, stdout.buffer.Bytes()...), Stderr: append([]byte{}, stderr.buffer.Bytes()...),
		StdoutTruncated: stdout.truncated, StderrTruncated: stderr.truncated,
	}
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		result.TimedOut, result.ExitCode = true, 124
	case errors.Is(ctx.Err(), context.Canceled):
		result.ExitCode = 130
	case command.ProcessState != nil:
		result.ExitCode = command.ProcessState.ExitCode()
		if status, ok := command.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			result.ExitCode = 128 + int(status.Signal())
		}
	case errors.Is(err, exec.ErrNotFound), errors.Is(err, os.ErrNotExist):
		result.ExitCode, result.Stderr = 127, []byte(fmt.Sprintf("command not found: %s\n", request.Argv[0]))
	case errors.Is(err, os.ErrPermission), errors.Is(err, syscall.ENOEXEC):
		result.ExitCode, result.Stderr = 126, []byte(fmt.Sprintf("cannot execute: %s\n", request.Argv[0]))
	default:
		return nil, fmt.Errorf("run command: %w", err)
	}
	return result, nil
}

func pausedExecManifest(dir string) (*Manifest, error) {
	manifest, err := ReadManifest(filepath.Join(dir, "session.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if !isTopLevelManifest(manifest) || manifest.SessionKind != KindController {
		return nil, fmt.Errorf("exec requires a root Goal: %w", ErrInvalidSession)
	}
	if manifest.IsStopped() {
		return nil, fmt.Errorf("the Goal is stopped: %w", ErrConflict)
	}
	if !manifest.Paused {
		return nil, fmt.Errorf("pause the Goal before running a command: %w", ErrConflict)
	}
	return manifest, nil
}

func execEnvironment(dir string, manifest *Manifest, runtime SessionRuntime) []string {
	var env []string
	for _, item := range os.Environ() {
		name, _, _ := strings.Cut(item, "=")
		if strings.HasPrefix(name, "TELOS_") || name == "VIRTUAL_ENV" || name == "PYTHONHOME" || name == "PYTHONPATH" {
			continue
		}
		env = append(env, item)
	}
	env = append(env,
		"TELOS_SESSION_DIR="+filepath.Dir(dir), "TELOS_SESSION_ID="+filepath.Base(dir),
		"TELOS_RUNTIME="+string(runtime), "TELOS_MODEL="+manifest.Config.Model,
		"TELOS_THINKING="+manifest.Config.Thinking,
	)
	if endpoint := os.Getenv("TELOS_API_ENDPOINT"); endpoint != "" {
		env = append(env, "TELOS_API_ENDPOINT="+endpoint)
	}
	if manifest.Access != nil {
		env = append(env, "TELOS_API_TOKEN="+manifest.Access.APIToken)
	}
	return env
}

type execOutput struct {
	buffer    bytes.Buffer
	truncated bool
}

func (output *execOutput) Write(data []byte) (int, error) {
	keep := min(len(data), MaxExecOutputBytes-output.buffer.Len())
	_, _ = output.buffer.Write(data[:keep])
	output.truncated = output.truncated || keep != len(data)
	return len(data), nil
}

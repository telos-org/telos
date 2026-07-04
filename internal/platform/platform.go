// Package platform defines the platform abstraction for running commands.
package platform

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	TaskEnvVar            = "TELOS_TASK"
	FileListLimit         = 200
	WorkspaceStateExclude = ".git .telos __pycache__"
)

// CommandResult is the outcome of one platform run.
type CommandResult struct {
	RawLines    []string
	Stderr      string
	ReturnCode  int
	DurationMS  int
	InfraError  string
	TimedOut    bool
	Interrupted bool
}

// OnStdoutLine is called for each stdout line as it arrives.
type OnStdoutLine func(line string)

// InterruptRequested returns true when an active command should be stopped.
type InterruptRequested func() bool

// LocalPlatform runs commands as subprocesses in a workspace directory.
type LocalPlatform struct {
	Workspace string
	Env       map[string]string
}

// NewLocalPlatform creates a new local platform.
func NewLocalPlatform(workspace string) *LocalPlatform {
	abs, _ := filepath.Abs(workspace)
	return &LocalPlatform{Workspace: abs}
}

// Run executes a command in the workspace.
func (p *LocalPlatform) Run(argv []string, task string, env map[string]string, timeout int, interrupt InterruptRequested, onLine OnStdoutLine) *CommandResult {
	result := &CommandResult{}
	started := time.Now()

	mergedEnv := workspaceProcessEnv()
	if p.Env != nil {
		for k, v := range p.Env {
			mergedEnv = append(mergedEnv, k+"="+v)
		}
	}
	if env != nil {
		for k, v := range env {
			mergedEnv = append(mergedEnv, k+"="+v)
		}
	}
	if task != "" {
		mergedEnv = append(mergedEnv, TaskEnvVar+"="+task)
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = p.Workspace
	cmd.Env = mergedEnv
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		result.InfraError = fmt.Sprintf("stdout_pipe: %v", err)
		return result
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		result.InfraError = fmt.Sprintf("stderr_pipe: %v", err)
		return result
	}

	if err := cmd.Start(); err != nil {
		result.InfraError = fmt.Sprintf("local_spawn_failed:%v", err)
		return result
	}

	doneCh := make(chan struct{})
	var stderrBuf strings.Builder

	go func() {
		buf := make([]byte, 65536)
		for {
			n, err := stderr.Read(buf)
			if n > 0 {
				stderrBuf.Write(buf[:n])
			}
			if err != nil {
				break
			}
		}
	}()

	go func() {
		defer close(doneCh)
		buf := make([]byte, 65536)
		lineBuf := ""
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				lineBuf += string(buf[:n])
				for {
					idx := strings.IndexByte(lineBuf, '\n')
					if idx < 0 {
						break
					}
					line := strings.TrimRight(lineBuf[:idx], "\r")
					lineBuf = lineBuf[idx+1:]
					if line != "" {
						result.RawLines = append(result.RawLines, line)
						if onLine != nil {
							onLine(line)
						}
					}
				}
			}
			if err != nil {
				if lineBuf != "" {
					result.RawLines = append(result.RawLines, lineBuf)
					if onLine != nil {
						onLine(lineBuf)
					}
				}
				break
			}
		}
	}()

	var timedOut atomic.Bool
	var interrupted atomic.Bool
	var timer *time.Timer
	if timeout > 0 {
		timer = time.AfterFunc(time.Duration(timeout)*time.Second, func() {
			timedOut.Store(true)
			killProcessGroup(cmd)
		})
	}
	var interruptDone chan struct{}
	if interrupt != nil {
		interruptDone = make(chan struct{})
		go func() {
			ticker := time.NewTicker(200 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					if interrupt() {
						interrupted.Store(true)
						killProcessGroup(cmd)
						return
					}
				case <-interruptDone:
					return
				}
			}
		}()
	}

	<-doneCh
	err = cmd.Wait()
	if timer != nil {
		timer.Stop()
	}
	if interruptDone != nil {
		close(interruptDone)
	}

	result.Stderr = stderrBuf.String()
	if cmd.ProcessState != nil {
		result.ReturnCode = cmd.ProcessState.ExitCode()
	} else {
		result.ReturnCode = -1
	}
	result.DurationMS = int(time.Since(started).Milliseconds())
	result.TimedOut = timedOut.Load()
	result.Interrupted = interrupted.Load()

	if err != nil && result.TimedOut {
		result.InfraError = fmt.Sprintf("local_timeout:%d", timeout)
	} else if err != nil && result.Interrupted {
		result.InfraError = "local_interrupted:stop_requested"
	}

	return result
}

// WorkspaceState returns a text snapshot of the workspace for prompts.
func (p *LocalPlatform) WorkspaceState() string {
	files := workspaceFileListing(p.Workspace)
	parts := []string{"=== FILES ===", files}
	if gitStatus := gitText([]string{"status", "--short"}, p.Workspace); gitStatus != "" {
		parts = append(parts, "=== GIT STATUS ===", gitStatus)
	}
	if gitDiff := gitText([]string{"diff", "--stat"}, p.Workspace); gitDiff != "" {
		parts = append(parts, "=== GIT DIFF STAT ===", gitDiff)
	}
	return strings.Join(parts, "\n")
}

// CheckpointWorkspace creates a tar.gz of the workspace.
func (p *LocalPlatform) CheckpointWorkspace(dest string) bool {
	abs, _ := filepath.Abs(dest)
	os.MkdirAll(filepath.Dir(abs), 0o755)

	tmp := abs + ".partial"
	absTmp, _ := filepath.Abs(tmp)
	f, err := os.Create(tmp)
	if err != nil {
		return false
	}

	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)

	walkErr := filepath.Walk(p.Workspace, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		rel, _ := filepath.Rel(p.Workspace, path)
		if rel == "." {
			return nil
		}
		// Skip the checkpoint itself
		absPath, _ := filepath.Abs(path)
		if absPath == abs || absPath == absTmp {
			return nil
		}
		linkname := ""
		if info.Mode()&os.ModeSymlink != 0 {
			linkname, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}
		header, err := tar.FileInfoHeader(info, linkname)
		if err != nil {
			return err
		}
		header.Name = "./" + filepath.ToSlash(rel)
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if _, err := tw.Write(data); err != nil {
				return err
			}
		}
		return nil
	})
	closeErr := tw.Close()
	if closeErr == nil {
		closeErr = gw.Close()
	}
	if closeErr == nil {
		closeErr = f.Close()
	} else {
		_ = f.Close()
	}
	if walkErr != nil || closeErr != nil {
		_ = os.Remove(tmp)
		return false
	}
	if err := os.Rename(tmp, abs); err != nil {
		_ = os.Remove(tmp)
		return false
	}
	return true
}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err == nil {
		syscall.Kill(-pgid, syscall.SIGTERM)
	} else {
		cmd.Process.Signal(syscall.SIGTERM)
	}
	time.AfterFunc(5*time.Second, func() {
		if cmd.Process != nil {
			if pgid, err := syscall.Getpgid(cmd.Process.Pid); err == nil {
				syscall.Kill(-pgid, syscall.SIGKILL)
			}
		}
	})
}

func workspaceProcessEnv() []string {
	var env []string
	for _, e := range os.Environ() {
		key := strings.SplitN(e, "=", 2)[0]
		switch key {
		case "VIRTUAL_ENV", "PYTHONHOME", "PYTHONPATH":
			continue
		}
		env = append(env, e)
	}
	return env
}

func workspaceFileListing(workspace string) string {
	excludes := map[string]bool{".git": true, ".telos": true, "__pycache__": true}
	var files []string
	filepath.Walk(workspace, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() && excludes[info.Name()] {
			return filepath.SkipDir
		}
		if !info.IsDir() {
			rel, _ := filepath.Rel(workspace, path)
			files = append(files, "./"+filepath.ToSlash(rel))
			if len(files) >= FileListLimit {
				files = append(files, "...")
				return io.EOF
			}
		}
		return nil
	})
	if len(files) == 0 {
		return "(no files)"
	}
	return strings.Join(files, "\n")
}

func gitText(args []string, cwd string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

package platform

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestLocalPlatformRun(t *testing.T) {
	dir := t.TempDir()
	p := NewLocalPlatform(dir)

	var lines []string
	result := p.Run(
		[]string{"sh", "-c", "echo hello; echo world"},
		"", nil, 10, nil,
		func(line string) { lines = append(lines, line) },
	)

	if result.InfraError != "" {
		t.Fatalf("infra error: %s", result.InfraError)
	}
	if result.ReturnCode != 0 {
		t.Errorf("return code: got %d", result.ReturnCode)
	}
	if len(lines) != 2 {
		t.Errorf("expected 2 lines, got %d: %v", len(lines), lines)
	}
	if len(result.RawLines) != 2 {
		t.Errorf("expected 2 raw lines, got %d", len(result.RawLines))
	}
}

func TestLocalPlatformRunWithTask(t *testing.T) {
	dir := t.TempDir()
	p := NewLocalPlatform(dir)

	result := p.Run(
		[]string{"sh", "-c", "echo $TELOS_TASK"},
		"test-task-body", nil, 10, nil, nil,
	)

	if result.InfraError != "" {
		t.Fatalf("infra error: %s", result.InfraError)
	}
	found := false
	for _, line := range result.RawLines {
		if strings.Contains(line, "test-task-body") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected task in output, got %v", result.RawLines)
	}
}

func TestLocalPlatformRunWithEnv(t *testing.T) {
	dir := t.TempDir()
	p := NewLocalPlatform(dir)

	result := p.Run(
		[]string{"sh", "-c", "echo $TELOS_ROLE"},
		"", map[string]string{"TELOS_ROLE": "prover"}, 10, nil, nil,
	)

	if result.InfraError != "" {
		t.Fatalf("infra error: %s", result.InfraError)
	}
	found := false
	for _, line := range result.RawLines {
		if strings.Contains(line, "prover") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected role in output, got %v", result.RawLines)
	}
}

func TestLocalPlatformRunFailure(t *testing.T) {
	dir := t.TempDir()
	p := NewLocalPlatform(dir)

	result := p.Run(
		[]string{"sh", "-c", "exit 42"},
		"", nil, 10, nil, nil,
	)

	if result.ReturnCode != 42 {
		t.Errorf("expected exit code 42, got %d", result.ReturnCode)
	}
}

func TestLocalPlatformRunWithoutTimeout(t *testing.T) {
	dir := t.TempDir()
	p := NewLocalPlatform(dir)

	result := p.Run(
		[]string{"sh", "-c", "echo no-timeout"},
		"", nil, 0, nil, nil,
	)

	if result.InfraError != "" {
		t.Fatalf("infra error: %s", result.InfraError)
	}
	if result.ReturnCode != 0 {
		t.Fatalf("return code: got %d", result.ReturnCode)
	}
}

func TestLocalPlatformRunTimeout(t *testing.T) {
	dir := t.TempDir()
	p := NewLocalPlatform(dir)

	result := p.Run(
		[]string{"sh", "-c", "sleep 60"},
		"", nil, 1, nil, nil,
	)

	if result.InfraError == "" {
		t.Error("expected timeout error")
	}
	if !strings.Contains(result.InfraError, "timeout") {
		t.Errorf("expected timeout in error: got %q", result.InfraError)
	}
}

func TestLocalPlatformRunInterrupt(t *testing.T) {
	dir := t.TempDir()
	p := NewLocalPlatform(dir)
	var stop atomic.Bool
	go func() {
		time.Sleep(100 * time.Millisecond)
		stop.Store(true)
	}()

	start := time.Now()
	result := p.Run(
		[]string{"sh", "-c", "sleep 60"},
		"", nil, 0,
		func() bool { return stop.Load() },
		nil,
	)

	if result.InfraError != "local_interrupted:stop_requested" {
		t.Fatalf("infra error: got %q", result.InfraError)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("interrupt should stop the subprocess promptly")
	}
}

func TestLocalPlatformRunInterruptReapsDescendants(t *testing.T) {
	dir := t.TempDir()
	p := NewLocalPlatform(dir)
	pidPath := filepath.Join(dir, "child.pid")
	var stop atomic.Bool
	go func() {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(pidPath); err == nil {
				stop.Store(true)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	result := p.Run(
		[]string{"sh", "-c", "sleep 60 & echo $! > child.pid; wait"},
		"", nil, 0,
		func() bool { return stop.Load() },
		nil,
	)

	if result.InfraError != "local_interrupted:stop_requested" {
		t.Fatalf("infra error: got %q", result.InfraError)
	}
	data, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("read child pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parse child pid: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		err = syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("descendant process %d survived interrupted turn", pid)
}

func TestLocalPlatformRunInvalidCommand(t *testing.T) {
	dir := t.TempDir()
	p := NewLocalPlatform(dir)

	result := p.Run(
		[]string{"/nonexistent/binary"},
		"", nil, 10, nil, nil,
	)

	if result.InfraError == "" {
		t.Error("expected spawn error")
	}
}

func TestLocalPlatformWorkspaceState(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0o644)
	os.MkdirAll(filepath.Join(dir, "pkg"), 0o755)
	os.WriteFile(filepath.Join(dir, "pkg", "lib.go"), []byte("package pkg"), 0o644)

	p := NewLocalPlatform(dir)
	state := p.WorkspaceState()

	if !strings.Contains(state, "=== FILES ===") {
		t.Error("should contain FILES header")
	}
	if !strings.Contains(state, "main.go") {
		t.Error("should contain main.go")
	}
	if !strings.Contains(state, "pkg/lib.go") {
		t.Error("should contain pkg/lib.go")
	}
}

func TestLocalPlatformCheckpointWorkspace(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte("content"), 0o644)

	p := NewLocalPlatform(dir)
	dest := filepath.Join(t.TempDir(), "workspace.tar.gz")
	ok := p.CheckpointWorkspace(dest)
	if !ok {
		t.Fatal("checkpoint failed")
	}

	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("checkpoint file missing: %v", err)
	}
	if info.Size() == 0 {
		t.Error("checkpoint file is empty")
	}
}

func TestWorkspaceStateExcludesGit(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("gitconfig"), 0o644)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0o644)

	p := NewLocalPlatform(dir)
	state := p.WorkspaceState()

	if strings.Contains(state, ".git/config") {
		t.Error("should exclude .git files")
	}
	if !strings.Contains(state, "main.go") {
		t.Error("should include main.go")
	}
}

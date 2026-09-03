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

	"github.com/telos-org/telos/internal/runtimeenv"
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

func TestWorkspaceProcessEnvReloadsRuntimeCredentialEnvironment(t *testing.T) {
	t.Setenv("MODAL_TOKEN_ID", "stale-one")
	t.Setenv("MODAL_TOKEN_SECRET", "stale-two")
	path := runtimeenv.Path(t.TempDir())
	t.Setenv(runtimeenv.PathEnvironmentVariable, path)
	store := runtimeenv.NewStore(path)
	if _, err := store.Put(1, map[string]string{
		"MODAL_TOKEN_ID":     "opaque-one",
		"MODAL_TOKEN_SECRET": "opaque-two",
	}); err != nil {
		t.Fatal(err)
	}

	first, err := runtimeCredentialProcessEnv(workspaceProcessEnv())
	if err != nil {
		t.Fatal(err)
	}
	firstValues := testEnvironmentMap(first)
	if firstValues["MODAL_TOKEN_ID"] != "opaque-one" || firstValues["MODAL_TOKEN_SECRET"] != "opaque-two" {
		t.Fatalf("initial credential environment missing: %v", firstValues)
	}

	if _, err := store.Put(2, map[string]string{"NEW_TOKEN": "opaque-three"}); err != nil {
		t.Fatal(err)
	}
	second, err := runtimeCredentialProcessEnv(workspaceProcessEnv())
	if err != nil {
		t.Fatal(err)
	}
	secondValues := testEnvironmentMap(second)
	if _, ok := secondValues["MODAL_TOKEN_ID"]; ok {
		t.Fatal("detached MODAL_TOKEN_ID remained in the next process environment")
	}
	if _, ok := secondValues["MODAL_TOKEN_SECRET"]; ok {
		t.Fatal("detached MODAL_TOKEN_SECRET remained in the next process environment")
	}
	if secondValues["NEW_TOKEN"] != "opaque-three" {
		t.Fatalf("attached NEW_TOKEN missing: %v", secondValues)
	}

	if _, err := store.Put(3, map[string]string{}); err != nil {
		t.Fatal(err)
	}
	third, err := runtimeCredentialProcessEnv(workspaceProcessEnv())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := testEnvironmentMap(third)["NEW_TOKEN"]; ok {
		t.Fatal("detached NEW_TOKEN remained in the next process environment")
	}
}

func TestWorkspaceProcessEnvPreservesLegacyEnvironmentWhenStateIsAbsent(t *testing.T) {
	t.Setenv("LEGACY_INTEGRATION_TOKEN", "legacy-placeholder")
	t.Setenv(runtimeenv.PathEnvironmentVariable, "")

	environment, err := runtimeCredentialProcessEnv(workspaceProcessEnv())
	if err != nil {
		t.Fatal(err)
	}
	if got := testEnvironmentMap(environment)["LEGACY_INTEGRATION_TOKEN"]; got != "legacy-placeholder" {
		t.Fatalf("legacy environment: got %q want legacy-placeholder", got)
	}
}

func TestRuntimeCredentialEnvironmentIsFinalOverlayAuthority(t *testing.T) {
	dir := t.TempDir()
	path := runtimeenv.Path(dir)
	t.Setenv(runtimeenv.PathEnvironmentVariable, path)
	t.Setenv("TOKEN", "inherited")
	store := runtimeenv.NewStore(path)
	if _, err := store.Put(1, map[string]string{"TOKEN": "credential-placeholder"}); err != nil {
		t.Fatal(err)
	}
	platform := NewLocalPlatform(dir)
	platform.Env = map[string]string{"TOKEN": "platform-overlay"}

	result := platform.Run(
		[]string{"sh", "-c", "printf '%s\\n' \"$TOKEN\""},
		"",
		map[string]string{"TOKEN": "per-call-overlay"},
		10,
		nil,
		nil,
	)
	if result.InfraError != "" || result.ReturnCode != 0 {
		t.Fatalf("run failed: %#v", result)
	}
	if len(result.RawLines) != 1 || result.RawLines[0] != "credential-placeholder" {
		t.Fatalf("managed credential did not win final overlay: %v", result.RawLines)
	}

	if _, err := store.Put(2, map[string]string{}); err != nil {
		t.Fatal(err)
	}
	result = platform.Run(
		[]string{"sh", "-c", `if [ "${TOKEN+x}" = x ]; then printf '%s\n' "$TOKEN"; else printf 'missing\n'; fi`},
		"",
		map[string]string{"TOKEN": "per-call-overlay"},
		10,
		nil,
		nil,
	)
	if result.InfraError != "" || result.ReturnCode != 0 {
		t.Fatalf("run after detach failed: %#v", result)
	}
	if len(result.RawLines) != 1 || result.RawLines[0] != "missing" {
		t.Fatalf("later overlay reintroduced detached credential: %v", result.RawLines)
	}
}

func TestLocalPlatformDoesNotSpawnWhenConfiguredRuntimeCredentialEnvironmentIsMissing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(runtimeenv.PathEnvironmentVariable, filepath.Join(dir, "missing.json"))
	marker := filepath.Join(dir, "spawned")

	result := NewLocalPlatform(dir).Run(
		[]string{"sh", "-c", "touch \"$1\"", "test", marker},
		"", nil, 10, nil, nil,
	)
	if result.InfraError != "runtime_credential_environment_invalid" {
		t.Fatalf("infra error: got %q", result.InfraError)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("command unexpectedly spawned: %v", err)
	}
}

func TestLocalPlatformDoesNotSpawnWithCorruptRuntimeCredentialEnvironment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credential-environment.json")
	if err := os.WriteFile(path, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(runtimeenv.PathEnvironmentVariable, path)
	marker := filepath.Join(dir, "spawned")

	result := NewLocalPlatform(dir).Run(
		[]string{"sh", "-c", "touch \"$1\"", "test", marker},
		"", nil, 10, nil, nil,
	)
	if result.InfraError != "runtime_credential_environment_invalid" {
		t.Fatalf("infra error: got %q", result.InfraError)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("command unexpectedly spawned: %v", err)
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

func testEnvironmentMap(environment []string) map[string]string {
	values := make(map[string]string, len(environment))
	for _, entry := range environment {
		name, value, ok := strings.Cut(entry, "=")
		if ok {
			values[name] = value
		}
	}
	return values
}

func TestLocalPlatformRunCapturesCompleteStderr(t *testing.T) {
	dir := t.TempDir()
	p := NewLocalPlatform(dir)

	result := p.Run(
		[]string{"sh", "-c", `exec 1>&2; i=0; while [ "$i" -lt 8192 ]; do printf '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n'; i=$((i + 1)); done; printf 'stderr-tail\n'; exit 23`},
		"", nil, 10, nil, nil,
	)

	if result.ReturnCode != 23 {
		t.Fatalf("expected exit code 23, got %d", result.ReturnCode)
	}
	if got, want := len(result.Stderr), 8192*65+len("stderr-tail\n"); got != want {
		t.Fatalf("stderr length: got %d, want %d", got, want)
	}
	if !strings.HasSuffix(result.Stderr, "stderr-tail\n") {
		t.Fatalf("stderr is incomplete: got %d bytes", len(result.Stderr))
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

package platform

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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
	if result.StartedAt.IsZero() || result.EndedAt.IsZero() || result.DurationMS < 0 {
		t.Fatalf("missing timing metadata: started=%v ended=%v duration=%d", result.StartedAt, result.EndedAt, result.DurationMS)
	}
	if result.Signal != "" || result.TimedOut || result.Interrupted {
		t.Fatalf("unexpected terminal metadata: signal=%q timeout=%t interrupted=%t", result.Signal, result.TimedOut, result.Interrupted)
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

func TestLocalPlatformRunCapsStdoutAndStderr(t *testing.T) {
	dir := t.TempDir()
	p := NewLocalPlatform(dir)

	result := p.Run(
		[]string{"sh", "-c", "python3 - <<'PY'\nimport sys\nprint('x' * (300 * 1024))\nprint('y' * (300 * 1024), file=sys.stderr)\nPY"},
		"", nil, 10, nil, nil,
	)

	if result.InfraError != "" {
		t.Fatalf("infra error: %s", result.InfraError)
	}
	if !result.StdoutTruncated {
		t.Fatal("expected stdout to be truncated")
	}
	if !result.StderrTruncated {
		t.Fatal("expected stderr to be truncated")
	}
	if result.StdoutBytes > DefaultRunOutputLimit {
		t.Fatalf("stdout bytes exceed cap: %d", result.StdoutBytes)
	}
	if result.StderrBytes > DefaultRunOutputLimit {
		t.Fatalf("stderr bytes exceed cap: %d", result.StderrBytes)
	}
	if result.StdoutOriginalBytes <= result.StdoutBytes {
		t.Fatalf("stdout original bytes should exceed captured bytes: original=%d captured=%d", result.StdoutOriginalBytes, result.StdoutBytes)
	}
	if result.StderrOriginalBytes <= result.StderrBytes {
		t.Fatalf("stderr original bytes should exceed captured bytes: original=%d captured=%d", result.StderrOriginalBytes, result.StderrBytes)
	}
	if result.StdoutOriginalLines != 1 {
		t.Fatalf("stdout original lines: got %d", result.StdoutOriginalLines)
	}
	if result.StderrOriginalLines != 1 {
		t.Fatalf("stderr original lines: got %d", result.StderrOriginalLines)
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
	if !result.TimedOut || result.Interrupted {
		t.Fatalf("timeout flags: timed_out=%t interrupted=%t", result.TimedOut, result.Interrupted)
	}
	if result.Signal == "" {
		t.Fatalf("timeout should record terminating signal: %+v", result)
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
	if !result.Interrupted || result.TimedOut {
		t.Fatalf("interrupt flags: timed_out=%t interrupted=%t", result.TimedOut, result.Interrupted)
	}
	if result.Signal == "" {
		t.Fatalf("interrupt should record terminating signal: %+v", result)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("interrupt should stop the subprocess promptly")
	}
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

func TestCheckpointWorkspaceExcludesRuntimeDirsAndWritesManifest(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("secret"), 0o644)
	os.MkdirAll(filepath.Join(dir, "node_modules", "pkg"), 0o755)
	os.WriteFile(filepath.Join(dir, "node_modules", "pkg", "index.js"), []byte("ignored"), 0o644)
	os.MkdirAll(filepath.Join(dir, ".pytest_cache"), 0o755)
	os.WriteFile(filepath.Join(dir, ".pytest_cache", "state"), []byte("ignored"), 0o644)
	os.MkdirAll(filepath.Join(dir, "target"), 0o755)
	os.WriteFile(filepath.Join(dir, "target", "artifact"), []byte("ignored"), 0o644)
	os.WriteFile(filepath.Join(dir, ".env.local"), []byte("TOKEN=secret"), 0o644)
	os.WriteFile(filepath.Join(dir, "id_rsa"), []byte("secret"), 0o600)
	os.WriteFile(filepath.Join(dir, "credentials.json"), []byte("{}"), 0o600)
	os.WriteFile(filepath.Join(dir, "cert.p12"), []byte("secret"), 0o600)
	os.WriteFile(filepath.Join(dir, "app.go"), []byte("package app"), 0o644)

	p := NewLocalPlatform(dir)
	dest := filepath.Join(t.TempDir(), "workspace.tar.gz")
	if !p.CheckpointWorkspace(dest) {
		t.Fatal("checkpoint failed")
	}

	names := tarNames(t, dest)
	if !contains(names, "./app.go") {
		t.Fatalf("checkpoint missing app.go: %v", names)
	}
	for _, banned := range []string{
		"./.git/config",
		"./node_modules/pkg/index.js",
		"./.pytest_cache/state",
		"./target/artifact",
		"./.env.local",
		"./id_rsa",
		"./credentials.json",
		"./cert.p12",
	} {
		if contains(names, banned) {
			t.Fatalf("checkpoint included excluded path %s: %v", banned, names)
		}
	}

	data, err := os.ReadFile(dest + ".manifest.json")
	if err != nil {
		t.Fatalf("manifest missing: %v", err)
	}
	var manifest checkpointManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("manifest invalid: %v", err)
	}
	if !manifest.Complete {
		t.Fatal("manifest should mark completed checkpoint")
	}
	if len(manifest.Excluded) == 0 {
		t.Fatalf("manifest should record exclusions: %+v", manifest)
	}
	for _, want := range []string{".env.local", "id_rsa", "credentials.json", "cert.p12"} {
		if !manifestHasExcluded(manifest, want, "excluded_file") {
			t.Fatalf("manifest missing secret exclusion %s: %+v", want, manifest)
		}
	}
}

func TestCheckpointWorkspaceIncludeAllOverride(t *testing.T) {
	t.Setenv("TELOS_CHECKPOINT_INCLUDE_ALL", "1")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("config"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "debug.log"), []byte("log"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secret.pem"), []byte("pem"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := NewLocalPlatform(dir)
	dest := filepath.Join(t.TempDir(), "workspace.tar.gz")
	if !p.CheckpointWorkspace(dest) {
		t.Fatal("checkpoint failed")
	}

	names := tarNames(t, dest)
	for _, want := range []string{"./.git/config", "./debug.log", "./secret.pem"} {
		if !contains(names, want) {
			t.Fatalf("checkpoint include-all missing %s: %v", want, names)
		}
	}
	manifest := readCheckpointManifest(t, dest)
	if !manifest.IncludeAll {
		t.Fatalf("manifest should record include-all mode: %+v", manifest)
	}
}

func TestCheckpointWorkspaceRespectsRootGitignore(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.log\ncache/\n/anchored.txt\nignored-*.tmp\n!ignored-keep.tmp\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "app.go"), []byte("package app"), 0o644)
	os.WriteFile(filepath.Join(dir, "debug.log"), []byte("ignored"), 0o644)
	os.WriteFile(filepath.Join(dir, "anchored.txt"), []byte("ignored"), 0o644)
	os.WriteFile(filepath.Join(dir, "ignored-drop.tmp"), []byte("ignored"), 0o644)
	os.WriteFile(filepath.Join(dir, "ignored-keep.tmp"), []byte("kept"), 0o644)
	os.MkdirAll(filepath.Join(dir, "cache"), 0o755)
	os.WriteFile(filepath.Join(dir, "cache", "blob"), []byte("ignored"), 0o644)

	p := NewLocalPlatform(dir)
	dest := filepath.Join(t.TempDir(), "workspace.tar.gz")
	if !p.CheckpointWorkspace(dest) {
		t.Fatal("checkpoint failed")
	}

	names := tarNames(t, dest)
	for _, want := range []string{"./app.go", "./ignored-keep.tmp"} {
		if !contains(names, want) {
			t.Fatalf("checkpoint missing %s: %v", want, names)
		}
	}
	for _, banned := range []string{"./debug.log", "./anchored.txt", "./ignored-drop.tmp", "./cache/blob"} {
		if contains(names, banned) {
			t.Fatalf("checkpoint included gitignored path %s: %v", banned, names)
		}
	}

	manifest := readCheckpointManifest(t, dest)
	if !manifestHasExcluded(manifest, "debug.log", "gitignore") ||
		!manifestHasExcluded(manifest, "cache", "gitignore") {
		t.Fatalf("manifest missing gitignore exclusions: %+v", manifest)
	}
}

func TestCheckpointWorkspaceMaxSizeWritesFailedManifest(t *testing.T) {
	t.Setenv("TELOS_CHECKPOINT_MAX_BYTES", "8")
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "small.txt"), []byte("1234"), 0o644)
	os.WriteFile(filepath.Join(dir, "large.txt"), []byte("123456789"), 0o644)

	p := NewLocalPlatform(dir)
	dest := filepath.Join(t.TempDir(), "workspace.tar.gz")
	if p.CheckpointWorkspace(dest) {
		t.Fatal("checkpoint should fail after max archive size")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("failed checkpoint archive should not remain: %v", err)
	}
	manifest := readCheckpointManifest(t, dest)
	if manifest.Complete {
		t.Fatal("manifest should mark failed checkpoint incomplete")
	}
	if !manifestHasExcluded(manifest, "large.txt", "max_archive_size") {
		t.Fatalf("manifest missing max size exclusion: %+v", manifest)
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

func readCheckpointManifest(t *testing.T, checkpointPath string) checkpointManifest {
	t.Helper()
	data, err := os.ReadFile(checkpointPath + ".manifest.json")
	if err != nil {
		t.Fatalf("manifest missing: %v", err)
	}
	var manifest checkpointManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("manifest invalid: %v", err)
	}
	return manifest
}

func manifestHasExcluded(manifest checkpointManifest, path string, reason string) bool {
	for _, item := range manifest.Excluded {
		if item.Path == path && item.Reason == reason {
			return true
		}
	}
	return false
}

func tarNames(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	var names []string
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, header.Name)
	}
	return names
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

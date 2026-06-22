// Package platform defines the platform abstraction for running commands.
package platform

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	TaskEnvVar                = "TELOS_TASK"
	FileListLimit             = 200
	WorkspaceStateExclude     = ".git .telos __pycache__"
	DefaultRunOutputLimit     = 256 << 10
	DefaultCheckpointMaxBytes = 512 << 20
)

// CommandResult is the outcome of one platform run.
type CommandResult struct {
	RawLines            []string
	Stderr              string
	ReturnCode          int
	Signal              string
	DurationMS          int
	InfraError          string
	StartedAt           time.Time
	EndedAt             time.Time
	StdoutBytes         int
	StderrBytes         int
	StdoutOriginalBytes int
	StderrOriginalBytes int
	StdoutOriginalLines int
	StderrOriginalLines int
	StdoutTruncated     bool
	StderrTruncated     bool
	TimedOut            bool
	Interrupted         bool
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
func (p *LocalPlatform) Run(argv []string, task string, env map[string]string, timeout int, interrupt InterruptRequested, onLine OnStdoutLine, cwd ...string) *CommandResult {
	result := &CommandResult{StartedAt: time.Now()}
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
	if len(cwd) > 0 && strings.TrimSpace(cwd[0]) != "" {
		cmd.Dir = cwd[0]
	}
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

	var readers sync.WaitGroup
	processDone := make(chan struct{})
	stdoutCollector := newCappedLineCollector(DefaultRunOutputLimit, onLine)
	stderrCollector := newCappedBuffer(DefaultRunOutputLimit)

	readers.Add(1)
	go func() {
		defer readers.Done()
		buf := make([]byte, 65536)
		for {
			n, err := stderr.Read(buf)
			if n > 0 {
				stderrCollector.Write(buf[:n])
			}
			if err != nil {
				break
			}
		}
	}()

	readers.Add(1)
	go func() {
		defer readers.Done()
		buf := make([]byte, 65536)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				stdoutCollector.Write(buf[:n])
			}
			if err != nil {
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
			terminateProcessGroup(cmd, processDone)
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
						terminateProcessGroup(cmd, processDone)
						return
					}
				case <-interruptDone:
					return
				}
			}
		}()
	}

	readers.Wait()
	err = cmd.Wait()
	close(processDone)
	if timer != nil {
		timer.Stop()
	}
	if interruptDone != nil {
		close(interruptDone)
	}

	stdoutCollector.Finish()
	result.RawLines = stdoutCollector.Lines()
	result.StdoutBytes = stdoutCollector.Bytes()
	result.StdoutOriginalBytes = stdoutCollector.OriginalBytes()
	result.StdoutOriginalLines = stdoutCollector.OriginalLines()
	result.StdoutTruncated = stdoutCollector.Truncated()
	result.Stderr = stderrCollector.String()
	result.StderrBytes = stderrCollector.Bytes()
	result.StderrOriginalBytes = stderrCollector.OriginalBytes()
	result.StderrOriginalLines = stderrCollector.OriginalLines()
	result.StderrTruncated = stderrCollector.Truncated()
	if cmd.ProcessState != nil {
		result.ReturnCode = cmd.ProcessState.ExitCode()
		if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			result.Signal = status.Signal().String()
		}
	} else {
		result.ReturnCode = -1
	}
	result.EndedAt = time.Now()
	result.DurationMS = int(result.EndedAt.Sub(started).Milliseconds())
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

	includeAll := checkpointIncludeAll()
	manifest := checkpointManifest{MaxBytes: checkpointMaxBytes(), IncludeAll: includeAll}
	var ignoreRules []checkpointIgnoreRule
	if !includeAll {
		ignoreRules = loadCheckpointIgnoreRules(p.Workspace)
	}
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
		if !includeAll && info.IsDir() && shouldExcludeCheckpointPath(rel, info.Name()) {
			manifest.Excluded = append(manifest.Excluded, checkpointManifestEntry{Path: filepath.ToSlash(rel), Reason: "excluded_dir"})
			return filepath.SkipDir
		}
		if !includeAll && !info.IsDir() && shouldExcludeCheckpointPath(rel, info.Name()) {
			manifest.Excluded = append(manifest.Excluded, checkpointManifestEntry{Path: filepath.ToSlash(rel), Reason: "excluded_file"})
			return nil
		}
		// Skip the checkpoint itself
		absPath, _ := filepath.Abs(path)
		if absPath == abs || absPath == absTmp {
			manifest.Excluded = append(manifest.Excluded, checkpointManifestEntry{Path: filepath.ToSlash(rel), Reason: "checkpoint_output"})
			return nil
		}
		if !includeAll && ignoredByCheckpointRules(ignoreRules, rel, info.IsDir()) {
			manifest.Excluded = append(manifest.Excluded, checkpointManifestEntry{Path: filepath.ToSlash(rel), Reason: "gitignore"})
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.Mode().IsRegular() {
			if manifest.TotalBytes+info.Size() > manifest.MaxBytes {
				manifest.Excluded = append(manifest.Excluded, checkpointManifestEntry{Path: filepath.ToSlash(rel), Reason: "max_archive_size"})
				return fmt.Errorf("checkpoint max size exceeded: %d > %d", manifest.TotalBytes+info.Size(), manifest.MaxBytes)
			}
			manifest.TotalBytes += info.Size()
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
		manifest.Included = append(manifest.Included, filepath.ToSlash(rel))
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
		_ = writeCheckpointManifest(abs, manifest, false)
		return false
	}
	if err := os.Rename(tmp, abs); err != nil {
		_ = os.Remove(tmp)
		_ = writeCheckpointManifest(abs, manifest, false)
		return false
	}
	_ = writeCheckpointManifest(abs, manifest, true)
	return true
}

type checkpointManifest struct {
	Included   []string                  `json:"included"`
	Excluded   []checkpointManifestEntry `json:"excluded"`
	TotalBytes int64                     `json:"total_bytes"`
	MaxBytes   int64                     `json:"max_bytes"`
	IncludeAll bool                      `json:"include_all,omitempty"`
	Complete   bool                      `json:"complete"`
}

type checkpointManifestEntry struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

func writeCheckpointManifest(checkpointPath string, manifest checkpointManifest, complete bool) error {
	manifest.Complete = complete
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(checkpointPath+".manifest.json", data, 0o644)
}

func shouldExcludeCheckpointPath(rel, name string) bool {
	switch name {
	case ".git", ".telos", "__pycache__", "node_modules", ".venv", "venv", "dist", "build", "target", "out", ".cache", ".pytest_cache", ".mypy_cache", ".ruff_cache", ".gradle":
		return true
	}
	base := filepath.Base(rel)
	lower := strings.ToLower(base)
	if lower == ".env" || strings.HasPrefix(lower, ".env.") || strings.HasSuffix(lower, ".env") {
		return true
	}
	switch lower {
	case ".npmrc", ".pypirc", ".netrc", "id_rsa", "id_dsa", "id_ecdsa", "id_ed25519", "credentials", "credentials.json", "service-account.json", "service_account.json":
		return true
	}
	return strings.HasSuffix(lower, ".pem") ||
		strings.HasSuffix(lower, ".key") ||
		strings.HasSuffix(lower, ".p12") ||
		strings.HasSuffix(lower, ".pfx")
}

type checkpointIgnoreRule struct {
	Pattern  string
	Negate   bool
	DirOnly  bool
	Rooted   bool
	HasSlash bool
}

// loadCheckpointIgnoreRules parses the workspace .gitignore for checkpoint
// hygiene. It implements an intentional *subset* of gitignore semantics
// (top-level file only, no `**`, no nested .gitignore, simple last-match-wins
// negation) — enough to keep build/cache output out of checkpoints. It is not a
// security boundary: the hardcoded secret-name exclusions in
// shouldExcludeCheckpointPath are the real safety net for sensitive files.
func loadCheckpointIgnoreRules(root string) []checkpointIgnoreRule {
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		return nil
	}
	var rules []checkpointIgnoreRule
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rule := checkpointIgnoreRule{}
		if strings.HasPrefix(line, "!") {
			rule.Negate = true
			line = strings.TrimPrefix(line, "!")
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasSuffix(line, "/") {
			rule.DirOnly = true
			line = strings.TrimSuffix(line, "/")
		}
		if strings.HasPrefix(line, "/") {
			rule.Rooted = true
			line = strings.TrimPrefix(line, "/")
		}
		line = filepath.ToSlash(filepath.Clean(line))
		if line == "." || line == "" {
			continue
		}
		rule.Pattern = line
		rule.HasSlash = strings.Contains(line, "/")
		rules = append(rules, rule)
	}
	return rules
}

func ignoredByCheckpointRules(rules []checkpointIgnoreRule, rel string, isDir bool) bool {
	rel = filepath.ToSlash(filepath.Clean(rel))
	if rel == "." || rel == "" {
		return false
	}
	ignored := false
	for _, rule := range rules {
		if checkpointIgnoreMatch(rule, rel, isDir) {
			ignored = !rule.Negate
		}
	}
	return ignored
}

func checkpointIgnoreMatch(rule checkpointIgnoreRule, rel string, isDir bool) bool {
	if rule.DirOnly && !isDir && rel != rule.Pattern && !strings.HasPrefix(rel, rule.Pattern+"/") {
		return false
	}
	if rule.HasSlash || rule.Rooted {
		return matchCheckpointPattern(rule.Pattern, rel) || strings.HasPrefix(rel, rule.Pattern+"/")
	}
	for _, part := range strings.Split(rel, "/") {
		if matchCheckpointPattern(rule.Pattern, part) {
			return true
		}
	}
	return false
}

func matchCheckpointPattern(pattern, value string) bool {
	ok, err := filepath.Match(pattern, value)
	return err == nil && ok
}

func checkpointMaxBytes() int64 {
	raw := strings.TrimSpace(os.Getenv("TELOS_CHECKPOINT_MAX_BYTES"))
	if raw == "" {
		return DefaultCheckpointMaxBytes
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return DefaultCheckpointMaxBytes
	}
	return n
}

func checkpointIncludeAll() bool {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv("TELOS_CHECKPOINT_INCLUDE_ALL")))
	switch raw {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func terminateProcessGroup(cmd *exec.Cmd, processDone <-chan struct{}) {
	if cmd.Process == nil {
		return
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err == nil {
		syscall.Kill(-pgid, syscall.SIGTERM)
	} else {
		cmd.Process.Signal(syscall.SIGTERM)
	}
	timer := time.NewTimer(5 * time.Second)
	go func() {
		defer timer.Stop()
		select {
		case <-processDone:
			return
		case <-timer.C:
		}
		if cmd.Process != nil {
			if pgid, err := syscall.Getpgid(cmd.Process.Pid); err == nil {
				syscall.Kill(-pgid, syscall.SIGKILL)
			}
		}
	}()
}

type cappedLineCollector struct {
	limit           int
	bytes           int
	originalBytes   int
	originalLines   int
	seenInput       bool
	lastByteNewline bool
	truncated       bool
	lineBuf         string
	lines           []string
	onLine          OnStdoutLine
}

func newCappedLineCollector(limit int, onLine OnStdoutLine) *cappedLineCollector {
	return &cappedLineCollector{limit: limit, onLine: onLine}
}

func (c *cappedLineCollector) Write(data []byte) {
	c.recordOriginal(data)
	if c.limit > 0 && c.bytes >= c.limit {
		c.truncated = true
		return
	}
	if c.limit > 0 && c.bytes+len(data) > c.limit {
		data = data[:c.limit-c.bytes]
		c.truncated = true
	}
	c.bytes += len(data)
	c.lineBuf += string(data)
	for {
		idx := strings.IndexByte(c.lineBuf, '\n')
		if idx < 0 {
			break
		}
		line := strings.TrimRight(c.lineBuf[:idx], "\r")
		c.lineBuf = c.lineBuf[idx+1:]
		c.appendLine(line)
	}
}

func (c *cappedLineCollector) Finish() {
	if c.seenInput && !c.lastByteNewline {
		c.originalLines++
	}
	if c.lineBuf != "" {
		c.appendLine(c.lineBuf)
		c.lineBuf = ""
	}
	if c.truncated {
		c.appendLine(fmt.Sprintf("... stdout truncated at %d bytes ...", c.limit))
	}
}

func (c *cappedLineCollector) appendLine(line string) {
	// Preserve blank lines so reconstructed output stays faithful to the
	// original spacing. Only the synthetic truncation marker is non-empty.
	c.lines = append(c.lines, line)
	if c.onLine != nil {
		c.onLine(line)
	}
}

func (c *cappedLineCollector) Lines() []string {
	return c.lines
}

func (c *cappedLineCollector) Bytes() int {
	return c.bytes
}

func (c *cappedLineCollector) OriginalBytes() int {
	return c.originalBytes
}

func (c *cappedLineCollector) OriginalLines() int {
	return c.originalLines
}

func (c *cappedLineCollector) Truncated() bool {
	return c.truncated
}

func (c *cappedLineCollector) recordOriginal(data []byte) {
	if len(data) == 0 {
		return
	}
	c.seenInput = true
	c.originalBytes += len(data)
	c.originalLines += bytes.Count(data, []byte{'\n'})
	c.lastByteNewline = data[len(data)-1] == '\n'
}

type cappedBuffer struct {
	limit           int
	bytes           int
	originalBytes   int
	originalLines   int
	seenInput       bool
	lastByteNewline bool
	truncated       bool
	buf             strings.Builder
}

func newCappedBuffer(limit int) *cappedBuffer {
	return &cappedBuffer{limit: limit}
}

func (b *cappedBuffer) Write(data []byte) {
	b.recordOriginal(data)
	if b.limit > 0 && b.bytes >= b.limit {
		b.truncated = true
		return
	}
	if b.limit > 0 && b.bytes+len(data) > b.limit {
		data = data[:b.limit-b.bytes]
		b.truncated = true
	}
	b.bytes += len(data)
	b.buf.Write(data)
}

func (b *cappedBuffer) String() string {
	if b.truncated {
		return b.buf.String() + fmt.Sprintf("\n... stderr truncated at %d bytes ...", b.limit)
	}
	return b.buf.String()
}

func (b *cappedBuffer) Bytes() int {
	return b.bytes
}

func (b *cappedBuffer) OriginalBytes() int {
	return b.originalBytes
}

func (b *cappedBuffer) OriginalLines() int {
	if b.seenInput && !b.lastByteNewline {
		return b.originalLines + 1
	}
	return b.originalLines
}

func (b *cappedBuffer) Truncated() bool {
	return b.truncated
}

func (b *cappedBuffer) recordOriginal(data []byte) {
	if len(data) == 0 {
		return
	}
	b.seenInput = true
	b.originalBytes += len(data)
	b.originalLines += bytes.Count(data, []byte{'\n'})
	b.lastByteNewline = data[len(data)-1] == '\n'
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

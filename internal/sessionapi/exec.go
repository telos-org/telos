package sessionapi

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// A person runs one command at a time in a paused goal's workspace, in place
// of its agents. Each finished command is one line of exec.jsonl, beside the
// current spec's evidence.jsonl, for people and agents to read.

// Limits on a command. A request is at most maxSessionRequestBytes (4 MiB)
// and carries stdin as base64. An answer carries both output streams as
// base64, and Cloud accepts 66 MiB. Cloudflare, in front of Cloud, drops a
// request with no answer after 100 s, so a command and the two stop graces
// after it fit in 90 s.
const (
	execStdinMaxBytes   = 2 << 20
	execOutputMaxBytes  = 24 << 20 // of each of stdout and stderr
	execDefaultTimeoutS = 60
	execMaxTimeoutS     = 80
)

// execStopGrace is how long a stopped command has to exit after SIGTERM
// before SIGKILL, and how long its output may stay open once it has exited.
const execStopGrace = 5 * time.Second

// ExecRequest is the body of POST /api/sessions/{id}/exec. The command runs
// in the agents' workspace.
type ExecRequest struct {
	Argv     []string `json:"argv"`
	StdinB64 string   `json:"stdin_b64,omitempty"`
	TimeoutS int      `json:"timeout_s,omitempty"`
	Operator string   `json:"operator"`
}

// ExecResponse is a finished command. A Truncated flag says that the stream
// was cut at 24 MiB. TimedOut says that its time limit stopped it.
type ExecResponse struct {
	ExitCode        int    `json:"exit_code"`
	StdoutB64       string `json:"stdout_b64"`
	StderrB64       string `json:"stderr_b64"`
	StdoutTruncated bool   `json:"stdout_truncated"`
	StderrTruncated bool   `json:"stderr_truncated"`
	TimedOut        bool   `json:"timed_out"`
	DurationMS      int64  `json:"duration_ms"`
}

// execRecord is one line of exec.jsonl: everything about a finished command.
type execRecord struct {
	TS       string   `json:"ts"`
	Operator string   `json:"operator"`
	Argv     []string `json:"argv"`
	TimeoutS int      `json:"timeout_s"`
	StdinB64 string   `json:"stdin_b64"`
	ExecResponse
}

// refusal is a request telosd will not carry out: its kind, ErrConflict,
// ErrInvalidRequest or ErrTooLarge, and the sentence that says why.
type refusal struct {
	kind   error
	detail string
}

func (e refusal) Error() string { return e.detail }
func (e refusal) Unwrap() error { return e.kind }

// Exec runs one command in a paused root session's workspace and appends it
// to exec.jsonl once it has finished. The command stops when ctx is done, at
// its time limit, or when the pause is lifted.
func (fs *FileStore) Exec(ctx context.Context, id string, req ExecRequest) (*ExecResponse, error) {
	stdin, err := base64.StdEncoding.DecodeString(req.StdinB64)
	switch {
	case len(req.Argv) == 0 || req.Argv[0] == "":
		return nil, refusal{ErrInvalidRequest, "The argv field must name a program to run."}
	case req.Operator == "":
		return nil, refusal{ErrInvalidRequest, "The operator field is required."}
	case req.TimeoutS < 0 || req.TimeoutS > execMaxTimeoutS:
		return nil, refusal{ErrInvalidRequest, fmt.Sprintf("The timeout_s field must be from 1 to %d, or left out for %d.", execMaxTimeoutS, execDefaultTimeoutS)}
	case err != nil:
		return nil, refusal{ErrInvalidRequest, "The stdin_b64 field is not valid base64."}
	case len(stdin) > execStdinMaxBytes:
		return nil, refusal{ErrTooLarge, "The stdin is larger than 2 MiB."}
	}

	fs.mu.Lock()
	m, err := fs.readRoot(id)
	var log string
	if err == nil {
		log, err = execLogPath(m)
	}
	if err == nil {
		err = fs.execRefusal(id, m)
	}
	if err != nil {
		fs.mu.Unlock()
		return nil, err
	}
	timeoutS := cmp.Or(req.TimeoutS, execDefaultTimeoutS)
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutS)*time.Second)
	if fs.execs == nil {
		fs.execs = map[string]context.CancelFunc{}
	}
	fs.execs[id] = cancel
	fs.mu.Unlock()
	defer func() {
		fs.mu.Lock()
		delete(fs.execs, id)
		fs.mu.Unlock()
		cancel()
	}()

	rec := execRecord{TS: execNow(), Operator: req.Operator, Argv: req.Argv, TimeoutS: timeoutS, StdinB64: req.StdinB64}
	if rec.ExecResponse, err = runCommand(ctx, req.Argv, filepath.Join(fs.sessionDir(id), "workspace"), stdin); err != nil {
		return nil, err
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return nil, err
	}
	return &rec.ExecResponse, nil
}

// execRefusal says why a command cannot run now, or is nil. The caller holds
// fs.mu.
func (fs *FileStore) execRefusal(id string, m *Manifest) error {
	detail := ""
	switch {
	case m.PausedAt == "": // never paused, or the resume note is already written
		detail = "This goal is not paused. Run `telos pause GOAL -m MESSAGE` first."
	case fs.agentTurnRunning(id):
		detail = "The agents are still stopping. Try again in a few seconds."
	case fs.execs[id] != nil:
		detail = "Another command is still running. Wait for it to finish, then try again."
	default:
		return nil
	}
	return refusal{ErrConflict, detail}
}

// agentTurnRunning reports whether the root session or one of its children
// has an epoch open. A hold stops the game loop, which closes its epoch once
// the interrupted turn has returned, so a paused goal with no open epoch runs
// no agent turn. An epoch that opens later sees the hold before its first
// turn.
func (fs *FileStore) agentTurnRunning(rootID string) bool {
	entries, err := os.ReadDir(fs.Root)
	if err != nil {
		return true
	}
	for _, entry := range entries {
		m, err := ReadManifest(fs.manifestPath(entry.Name()))
		if err == nil && (entry.Name() == rootID || strValue(m.ParentSessionID) == rootID) && m.OpenEpoch() != nil {
			return true
		}
	}
	return false
}

// execLogPath is exec.jsonl, beside the current spec's evidence.jsonl.
func execLogPath(m *Manifest) (string, error) {
	if len(m.Specs) == 0 || ptrOr(m.Specs[0].EvidencePath, "") == "" {
		return "", fmt.Errorf("the session has no evidence folder: %w", ErrInvalidSession)
	}
	return filepath.Join(filepath.Dir(*m.Specs[0].EvidencePath), "exec.jsonl"), nil
}

// execNow is the time now, to the microsecond, so that a command and the
// pause before it never share a time. It is fixed width, so times compare as
// strings.
func execNow() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000000Z")
}

// commandsSince counts the lines of an exec.jsonl with ts after since, and
// none when since is empty. It stops at a line it cannot read.
func commandsSince(path, since string) int {
	f, err := os.Open(path)
	if err != nil || since == "" {
		return 0
	}
	defer f.Close()
	n := 0
	for dec := json.NewDecoder(f); ; {
		var line struct {
			TS string `json:"ts"`
		}
		if dec.Decode(&line) != nil {
			return n
		}
		if line.TS > since {
			n++
		}
	}
}

// runCommand runs argv directly, with no shell, in its own process group.
// When ctx is done, the group gets SIGTERM, then SIGKILL after execStopGrace.
// When the command exits, anything left in its group is killed. A program
// that left the group on purpose, as `pg_ctl start` does, may still hold the
// output open: after execStopGrace, runCommand stops reading it.
func runCommand(ctx context.Context, argv []string, dir string, stdin []byte) (ExecResponse, error) {
	began := time.Now()
	var streams [2]*outputPipe // stdout, stderr
	for i := range streams {
		pipe, err := newOutputPipe()
		if err != nil {
			return ExecResponse{}, err
		}
		defer pipe.r.Close()
		defer pipe.w.Close()
		streams[i] = pipe
	}

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Stdout, cmd.Stderr = streams[0].w, streams[1].w
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = execStopGrace
	startErr := cmd.Start()
	for _, pipe := range streams {
		pipe.w.Close() // the command holds its own copy
	}
	if startErr == nil {
		_ = cmd.Wait()
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
	grace := time.AfterFunc(execStopGrace, func() {
		for _, pipe := range streams {
			pipe.r.Close()
		}
	})
	for _, pipe := range streams {
		<-pipe.drained
	}
	grace.Stop()

	stdout, stderr := &streams[0].out, &streams[1].out
	exitCode := 0
	switch {
	case startErr != nil: // as a shell reports it: 127 when not found, 126 when it cannot run
		exitCode = 126
		if errors.Is(startErr, exec.ErrNotFound) || errors.Is(startErr, os.ErrNotExist) {
			exitCode = 127
		}
		fmt.Fprintf(stderr, "telosd: %v\n", startErr)
	case cmd.ProcessState.Sys().(syscall.WaitStatus).Signaled(): // 128 plus the signal, as a shell reports it
		exitCode = 128 + int(cmd.ProcessState.Sys().(syscall.WaitStatus).Signal())
	default:
		exitCode = cmd.ProcessState.ExitCode()
	}
	return ExecResponse{
		ExitCode:        exitCode,
		StdoutB64:       base64.StdEncoding.EncodeToString(stdout.buf.Bytes()),
		StderrB64:       base64.StdEncoding.EncodeToString(stderr.buf.Bytes()),
		StdoutTruncated: stdout.cut,
		StderrTruncated: stderr.cut,
		TimedOut:        timedOut,
		DurationMS:      time.Since(began).Milliseconds(),
	}, nil
}

// outputPipe is a pipe a command writes to, and what has been read from it.
type outputPipe struct {
	r, w    *os.File
	out     capture
	drained chan struct{} // closed when reading stops
}

func newOutputPipe() (*outputPipe, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	pipe := &outputPipe{r: r, w: w, drained: make(chan struct{})}
	go func() {
		defer close(pipe.drained)
		_, _ = io.Copy(&pipe.out, r)
	}()
	return pipe, nil
}

// capture keeps the first execOutputMaxBytes written to it and drops the
// rest. It accepts every write, so the command never blocks on a full pipe.
type capture struct {
	buf bytes.Buffer
	cut bool
}

func (c *capture) Write(p []byte) (int, error) {
	keep := min(len(p), execOutputMaxBytes-c.buf.Len())
	c.cut = c.cut || keep < len(p)
	c.buf.Write(p[:keep])
	return len(p), nil
}

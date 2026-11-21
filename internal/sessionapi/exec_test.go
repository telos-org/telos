package sessionapi_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/telos-org/telos/internal/sessionapi"
)

// newPausedGoal returns a goal that a person has paused, as newGoal does,
// with the path of its agents' workspace.
func newPausedGoal(t *testing.T) (*httptest.Server, *sessionapi.FileStore, string, string) {
	t.Helper()
	srv, store, id := newGoal(t)
	workspace := filepath.Join(store.Root, id, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if code, body := operatorCall(t, srv, "PUT", "/api/sessions/"+id+"/pause", `{"paused":true}`); code != http.StatusOK {
		t.Fatalf("pause = %d %s", code, body)
	}
	return srv, store, id, workspace
}

// execCommand runs a command as ada@example.com and returns the status, the
// result and the refusal.
func execCommand(t *testing.T, srv *httptest.Server, id string, req sessionapi.ExecRequest) (int, sessionapi.ExecResponse, string) {
	t.Helper()
	req.Operator = "ada@example.com"
	body, _ := json.Marshal(req)
	code, answer := operatorCall(t, srv, "POST", "/api/sessions/"+id+"/exec", string(body))
	var resp sessionapi.ExecResponse
	var refusal struct{ Detail string }
	_ = json.Unmarshal([]byte(answer), &resp)
	_ = json.Unmarshal([]byte(answer), &refusal)
	return code, resp, refusal.Detail
}

func decodeB64(t *testing.T, s string) string {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// Cloud sends commands and notes only to a runtime whose session list says
// it can take them.
func TestSessionListReportsExec(t *testing.T) {
	srv, _, _ := newGoal(t)
	code, body := operatorCall(t, srv, "GET", "/api/sessions", "")
	var list struct{ Sessions []map[string]any }
	if code != http.StatusOK || json.Unmarshal([]byte(body), &list) != nil || len(list.Sessions) != 1 || list.Sessions[0]["exec"] != true {
		t.Fatalf("GET /api/sessions = %d %s, want one session with exec true", code, body)
	}
}

func TestExecRunsACommandInThePausedWorkspaceAndRecordsIt(t *testing.T) {
	srv, store, id, workspace := newPausedGoal(t)
	t.Setenv("TELOS_API_TOKEN", "operator-secret") // telosd's own token
	req := sessionapi.ExecRequest{
		Argv:     []string{"sh", "-c", "cat - note.txt; echo ${TELOS_API_TOKEN-no token}; echo oops >&2; exit 3"},
		StdinB64: base64.StdEncoding.EncodeToString([]byte("from stdin\n")),
	}
	if err := os.WriteFile(filepath.Join(workspace, "note.txt"), []byte("from the workspace\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, resp, detail := execCommand(t, srv, id, req)
	if code != http.StatusOK || resp.ExitCode != 3 || decodeB64(t, resp.StdoutB64) != "from stdin\nfrom the workspace\nno token\n" || decodeB64(t, resp.StderrB64) != "oops\n" {
		t.Fatalf("exec = %d %q %+v", code, detail, resp)
	}

	session, err := store.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	log, err := os.ReadFile(filepath.Join(filepath.Dir(*session.Specs[0].EvidencePath), "exec.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var line struct {
		TS       string   `json:"ts"`
		Operator string   `json:"operator"`
		Argv     []string `json:"argv"`
		TimeoutS int      `json:"timeout_s"`
		StdinB64 string   `json:"stdin_b64"`
		sessionapi.ExecResponse
	}
	if strings.Count(string(log), "\n") != 1 || json.Unmarshal(log, &line) != nil {
		t.Fatalf("exec.jsonl is not one line:\n%s", log)
	}
	if line.TS == "" || line.Operator != "ada@example.com" || strings.Join(line.Argv, " ") != strings.Join(req.Argv, " ") ||
		line.TimeoutS != 60 || line.StdinB64 != req.StdinB64 || line.ExecResponse != resp {
		t.Fatalf("exec.jsonl line = %+v, want the command and its answer %+v", line, resp)
	}
}

func TestExecRejectsARequestItCannotAccept(t *testing.T) {
	srv, _, id, _ := newPausedGoal(t)
	for _, tc := range []struct {
		req  sessionapi.ExecRequest
		want int
	}{
		{sessionapi.ExecRequest{}, http.StatusBadRequest},
		{sessionapi.ExecRequest{Argv: []string{"true"}, TimeoutS: 3601}, http.StatusBadRequest},
		{sessionapi.ExecRequest{Argv: []string{"cat"}, StdinB64: base64.StdEncoding.EncodeToString(make([]byte, 2<<20+1))}, http.StatusRequestEntityTooLarge},
	} {
		if code, _, detail := execCommand(t, srv, id, tc.req); code != tc.want {
			t.Errorf("exec %v = %d %q, want %d", tc.req.Argv, code, detail, tc.want)
		}
	}
}

// A command runs only while the goal is paused and no agent turn and no other
// command runs. Lifting the pause stops it.
func TestExecRunsOneCommandAtATimeUntilThePauseLifts(t *testing.T) {
	srv, store, id, workspace := newPausedGoal(t)
	manifest := filepath.Join(store.Root, id, "session.json")
	setEpoch := func(epochs []sessionapi.Epoch) {
		t.Helper()
		if _, err := sessionapi.MutateManifest(manifest, func(m *sessionapi.Manifest) error {
			m.Epochs = epochs
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	setEpoch([]sessionapi.Epoch{{ID: 1, StartedAt: "2026-11-21T10:00:00.000Z"}})
	if code, _, detail := execCommand(t, srv, id, sessionapi.ExecRequest{Argv: []string{"true"}}); code != http.StatusConflict || detail != "The agents are still stopping. Try again in a few seconds." {
		t.Fatalf("exec during an agent turn = %d %q", code, detail)
	}
	setEpoch(nil)

	type result struct {
		code int
		resp sessionapi.ExecResponse
	}
	first := make(chan result, 1)
	go func() {
		code, resp, _ := execCommand(t, srv, id, sessionapi.ExecRequest{Argv: []string{"sh", "-c", "touch started; exec sleep 60"}})
		first <- result{code, resp}
	}()
	waitFor(t, "the first command to start", func() bool {
		_, err := os.Stat(filepath.Join(workspace, "started"))
		return err == nil
	})
	if code, _, detail := execCommand(t, srv, id, sessionapi.ExecRequest{Argv: []string{"true"}}); code != http.StatusConflict || detail != "Another command is still running. Wait for it to finish, then try again." {
		t.Fatalf("a second command = %d %q", code, detail)
	}
	// A resume note written now would miss the running command.
	if code, body := operatorCall(t, srv, "POST", "/api/sessions/"+id+"/resume", `{"message":"Done.","operator":"ada@example.com"}`); code != http.StatusConflict || !strings.Contains(body, "A command is still running.") {
		t.Fatalf("resume during a command = %d %s", code, body)
	}

	if code, body := operatorCall(t, srv, "PUT", "/api/sessions/"+id+"/pause", `{"paused":false}`); code != http.StatusOK {
		t.Fatalf("lift the pause = %d %s", code, body)
	}
	select {
	case got := <-first:
		if got.code != http.StatusOK || got.resp.ExitCode != 128+int(syscall.SIGTERM) || got.resp.TimedOut {
			t.Fatalf("the stopped command = %d %+v", got.code, got.resp)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("lifting the pause did not stop the command")
	}
	if code, _, detail := execCommand(t, srv, id, sessionapi.ExecRequest{Argv: []string{"true"}}); code != http.StatusConflict || detail != "This goal is not paused. Run `telos pause GOAL -m MESSAGE` first." {
		t.Fatalf("exec on a goal that is not paused = %d %q", code, detail)
	}
}

func TestExecStopsTheCommandAtItsLimits(t *testing.T) {
	srv, _, id, _ := newPausedGoal(t)
	run := func(t *testing.T, argv ...string) sessionapi.ExecResponse {
		t.Helper()
		_, resp, _ := execCommand(t, srv, id, sessionapi.ExecRequest{Argv: argv, TimeoutS: 2})
		return resp
	}

	t.Run("time limit", func(t *testing.T) {
		if resp := run(t, "sleep", "60"); !resp.TimedOut || resp.ExitCode != 128+int(syscall.SIGTERM) {
			t.Errorf("a command past its time limit = %+v", resp)
		}
	})
	t.Run("24 MiB of output", func(t *testing.T) {
		resp := run(t, "head", "-c", "25165825", "/dev/zero")
		if !resp.StdoutTruncated || len(decodeB64(t, resp.StdoutB64)) != 24<<20 || resp.ExitCode != 0 {
			t.Errorf("exit %d, cut %t, %d bytes", resp.ExitCode, resp.StdoutTruncated, len(decodeB64(t, resp.StdoutB64)))
		}
	})
	t.Run("program not found", func(t *testing.T) {
		if resp := run(t, "no-such-program"); resp.ExitCode != 127 || !strings.Contains(decodeB64(t, resp.StderrB64), "no-such-program") {
			t.Errorf("a program that is not found = %+v", resp)
		}
	})
	t.Run("what it leaves in its group is killed", func(t *testing.T) {
		resp := run(t, "sh", "-c", "sleep 60 & echo $!")
		var pid int
		if _, err := fmt.Sscan(decodeB64(t, resp.StdoutB64), &pid); err != nil || resp.ExitCode != 0 {
			t.Fatalf("background command = %+v", resp)
		}
		waitFor(t, "the background process to be killed", func() bool {
			return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
		})
	})
	t.Run("output held open outside its group", func(t *testing.T) {
		if _, err := exec.LookPath("perl"); err != nil {
			t.Skip("needs perl")
		}
		// The child leaves the group, as `pg_ctl start` does, and keeps
		// stdout open. The answer comes once the stop grace is over.
		began := time.Now()
		resp := run(t, "perl", "-e", `$|=1; if ($p = fork) { print "$p\n"; exit 0 } setpgrp; sleep 60`)
		var pid int
		if _, err := fmt.Sscan(decodeB64(t, resp.StdoutB64), &pid); err != nil || resp.ExitCode != 0 || resp.TimedOut {
			t.Fatalf("command = %+v", resp)
		}
		_ = syscall.Kill(pid, syscall.SIGKILL)
		if took := time.Since(began); took > 15*time.Second {
			t.Errorf("the answer took %s", took)
		}
	})
}

// waitFor polls cond until it holds, for at most 30 seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); !cond(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// The resume note counts the commands run since the goal was last paused, and
// tells the agents where they are.
func TestResumeNotePointsAtTheCommandsRunSinceThePause(t *testing.T) {
	srv, store, id, _ := newPausedGoal(t)
	base := "/api/sessions/" + id
	for _, step := range []struct{ method, path, body string }{
		{"POST", "/exec", `{"argv":["true"],"operator":"ada@example.com"}`}, // during an earlier pause
		{"PUT", "/pause", `{"paused":false}`},
		{"PUT", "/pause", `{"paused":true,"message":"Fixing the header.","operator":"ada@example.com"}`},
		{"POST", "/exec", `{"argv":["true"],"operator":"ada@example.com"}`},
	} {
		if code, body := operatorCall(t, srv, step.method, base+step.path, step.body); code != http.StatusOK {
			t.Fatalf("%s %s = %d %s", step.method, step.path, code, body)
		}
	}
	m, err := sessionapi.ReadManifest(filepath.Join(store.Root, id, "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	pausedAt := m.PausedAt
	if code, body := operatorCall(t, srv, "POST", base+"/resume", `{"message":"Fixed the header.","operator":"ada@example.com"}`); code != http.StatusOK {
		t.Fatalf("resume = %d %s", code, body)
	}

	specDir := filepath.Dir(*m.Specs[0].EvidencePath)
	transcript, err := os.ReadFile(filepath.Join(specDir, "transcript-"+id+".md"))
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("Fixed the header.\n\nCommands run while paused (from %s to TIME): 1. "+
		"They are the lines of `%s` with `ts` after %s. Each line holds a command's stdin, stdout and stderr in base64, so read the lines one at a time.\n</external_update>\n",
		pausedAt, filepath.Join(specDir, "exec.jsonl"), pausedAt)
	if !endsWithNote(string(transcript), want) {
		t.Fatalf("transcript does not end with %q:\n%s", want, transcript)
	}
	// Once the note is written, a command would miss it, even before the pause lifts.
	if code, _, detail := execCommand(t, srv, id, sessionapi.ExecRequest{Argv: []string{"true"}}); code != http.StatusConflict || !strings.HasPrefix(detail, "This goal is not paused.") {
		t.Fatalf("exec after the resume note = %d %q", code, detail)
	}

	// A later resume, with no pause since, points at no commands.
	for _, step := range []struct{ method, path, body string }{
		{"PUT", "/pause", `{"paused":false}`},
		{"POST", "/resume", `{"message":"Try again.","operator":"bob@example.com"}`},
	} {
		if code, body := operatorCall(t, srv, step.method, base+step.path, step.body); code != http.StatusOK {
			t.Fatalf("%s %s = %d %s", step.method, step.path, code, body)
		}
	}
	if transcript, _ = os.ReadFile(filepath.Join(specDir, "transcript-"+id+".md")); !strings.HasSuffix(string(transcript), "\n\nTry again.\n\nCommands run while paused: 0.\n</external_update>\n") {
		t.Fatalf("the later resume note points at old commands:\n%s", transcript)
	}
}

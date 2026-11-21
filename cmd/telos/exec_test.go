package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestExecPassesOnTheCommandsOutputAndExitCode(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString
	for _, tt := range []struct {
		name           string
		args           []string
		status         int    // 0: Cloud is not called
		answer         string // Cloud's answer
		stdout, stderr string
		exit           int
	}{
		{
			name:   "command output",
			args:   []string{"goal_1", "--", "git", "diff"},
			status: http.StatusOK,
			answer: `{"exit_code":3,"stdout_b64":"` + b64([]byte("out\x00")) + `","stderr_b64":"` + b64([]byte("warn\n")) + `"}`,
			stdout: "out\x00", stderr: "warn\n", exit: 3,
		},
		{
			name:   "time limit",
			args:   []string{"goal_1", "--", "sleep", "600"},
			status: http.StatusOK,
			answer: `{"exit_code":143,"timed_out":true}`,
			stderr: "error: the command was stopped at its time limit of 1m0s. To allow more time, use --timeout, up to 80s.\n",
			exit:   143,
		},
		{
			name:   "output cut",
			args:   []string{"goal_1", "--", "cat", "big"},
			status: http.StatusOK,
			answer: `{"exit_code":1,"stdout_b64":"` + b64([]byte("partial")) + `","stdout_truncated":true}`,
			stdout: "partial",
			stderr: "warning: the output was cut at 24 MiB per stream. Write the output to a file on the Goal and read it in parts.\n",
			exit:   1,
		},
		{
			name:   "refused",
			args:   []string{"goal_1", "--", "ls"},
			status: http.StatusConflict,
			answer: `{"detail":"This goal is not paused. Run ` + "`telos pause GOAL -m MESSAGE`" + ` first."}`,
			stderr: "error: This goal is not paused. Run `telos pause GOAL -m MESSAGE` first. (HTTP 409)\n",
			exit:   execFailed,
		},
		{
			name:   "no command",
			args:   []string{"goal_1", "ls"},
			stderr: "error: expected GOAL -- COMMAND", // the usage follows
			exit:   2,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if requests++; tt.status == 0 || requests > 1 || r.Method+" "+r.URL.Path != "POST /api/deployments/goal_1/exec" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusTeapot)
					return
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.answer))
			}))
			defer server.Close()
			configureCloudTest(t, server.URL)
			t.Setenv("TELOS_CONTEXT", "")

			stdout, stderr, err := inferenceCLIProcess(t, append([]string{"exec"}, tt.args...)...)
			if tt.exit == 2 {
				stderr, _, _ = strings.Cut(stderr, "\n")
			}
			if exitCode(err) != tt.exit || stdout != tt.stdout || stderr != tt.stderr {
				t.Fatalf("exit %d, stdout %q, stderr %q", exitCode(err), stdout, stderr)
			}
		})
	}
}

func TestExecSendsTheCommandAsGiven(t *testing.T) {
	type execRequest struct {
		Argv     []string `json:"argv"`
		StdinB64 string   `json:"stdin_b64"`
		TimeoutS int      `json:"timeout_s"`
	}
	requests := make(chan execRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request execRequest
		if r.Method+" "+r.URL.Path != "POST /api/deployments/goal_1/exec" || json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		requests <- request
		_, _ = w.Write([]byte(`{"exit_code":0}`))
	}))
	defer server.Close()
	configureCloudTest(t, server.URL)
	t.Setenv("TELOS_CONTEXT", "")
	stdinPath := filepath.Join(t.TempDir(), "fix.patch")
	if err := os.WriteFile(stdinPath, []byte("+fix\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, stderr, err := inferenceCLIProcess(t, "exec", "goal_1", "--stdin", stdinPath, "--timeout", "75s", "--", "git", "apply", "--", "-"); err != nil {
		t.Fatalf("stderr %q, %v", stderr, err)
	}
	request := <-requests
	if !slices.Equal(request.Argv, []string{"git", "apply", "--", "-"}) || request.StdinB64 != base64.StdEncoding.EncodeToString([]byte("+fix\n")) || request.TimeoutS != 75 {
		t.Fatalf("request = %+v", request)
	}
}

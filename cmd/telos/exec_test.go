package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/telos-org/telos/internal/sessionapi"
)

func TestExecForwardsCommandArgumentsAndExitCode(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request sessionapi.SessionExecRequest
		if r.Method != "POST" || r.URL.Path != "/api/deployments/goal_exec/exec" || r.Header.Get("Authorization") != "Bearer control-token" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if want := []string{"printf", "--json", "--timeout", "a b", "; exit 90"}; !reflect.DeepEqual(request.Argv, want) || request.TimeoutMS != 1234 {
			t.Errorf("request = %+v", request)
		}
		_ = json.NewEncoder(w).Encode(sessionapi.SessionExecResponse{Stdout: []byte{'a', 0, 255}, Stderr: []byte("failed\n"), ExitCode: 37})
	}))
	defer server.Close()
	configureCloudTest(t, server.URL)
	t.Setenv("TELOS_CONTEXT", "")

	args := []string{"exec", "goal_exec", "--timeout", "1234ms", "--", "printf", "--json", "--timeout", "a b", "; exit 90"}
	stdout, stderr, err := inferenceCLIProcess(t, args...)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 37 || stdout != string([]byte{'a', 0, 255}) || stderr != "failed\n" {
		t.Fatalf("stdout %q, stderr %q, error %v", stdout, stderr, err)
	}
	jsonArgs := append([]string{"exec", "--json"}, args[1:]...)
	stdout, stderr, err = inferenceCLIProcess(t, jsonArgs...)
	var result sessionapi.SessionExecResponse
	if !errors.As(err, &exit) || exit.ExitCode() != 37 || stderr != "" || json.Unmarshal([]byte(stdout), &result) != nil || string(result.Stdout) != string([]byte{'a', 0, 255}) {
		t.Fatalf("JSON stdout %q, stderr %q, error %v", stdout, stderr, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("command retried: %d requests", calls.Load())
	}
}

func TestExecRejectsInvalidCLIRequestsBeforeCallingCloud(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	configureCloudTest(t, server.URL)
	t.Setenv("TELOS_CONTEXT", "")
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"goal_exec"}, "provide a command after --"},
		{[]string{"goal_exec", "--"}, "provide a command after --"},
		{[]string{"goal_exec", "true"}, "one GOAL before --"},
		{[]string{"goal_exec", "--timeout", "6m", "--", "true"}, "--timeout must be"},
		{[]string{"goal_exec", "--timeout", "0", "--", "true"}, "--timeout must be"},
		{[]string{"local_123", "--", "true"}, "only for Cloud Goals"},
	} {
		_, stderr, err := inferenceCLIProcess(t, append([]string{"exec"}, tc.args...)...)
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 2 || !strings.Contains(stderr, tc.want) {
			t.Fatalf("args %v: stderr %q, error %v", tc.args, stderr, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid request reached Cloud: %d calls", calls.Load())
	}
}

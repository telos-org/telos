package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/config"
)

func TestDescribeShowsStatusReasonMessageAndRetryTimes(t *testing.T) {
	now := time.Now()
	session := cloud.SessionRecord{
		ID:          "goal_c7d2f0a4e8",
		Name:        "docs-site",
		Status:      "retrying",
		Reason:      "INTERNAL_ERROR",
		Message:     "The build server stopped responding.",
		LastRetryAt: now.Add(-90 * time.Second).Format(time.RFC3339),
		NextRetryAt: now.Add(150 * time.Second).Format(time.RFC3339),
	}
	var out bytes.Buffer
	printCloudSessionDescriptionForContext(&out, session, "", nil)
	for _, want := range []string{
		"Status    retrying\n",
		"Reason    INTERNAL_ERROR\n",
		"Message   The build server stopped responding.\n",
		"Last retry 1m ago\n",
		"Next retry in 2m\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("description missing %q:\n%s", want, out.String())
		}
	}
	if got := cloudSessionStatusCell(session); got != "retrying (INTERNAL_ERROR)" {
		t.Fatalf("list status = %q", got)
	}
	for _, session := range []cloud.SessionRecord{{Status: "working"}, {Status: "stopped", Reason: "STOPPED"}} {
		if got := cloudSessionStatusCell(session); got != session.Status {
			t.Fatalf("list status = %q, want %q", got, session.Status)
		}
	}
}

func TestPauseAndResumeCallCloudAndReportTheGoal(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if r.URL.Path == "/api/deployments/goal_2/pause" {
			http.Error(w, `{"detail":"organization admin privileges required"}`, http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "goal_1", "status": "paused"})
	}))
	defer server.Close()
	t.Setenv(config.ConfigPathEnv, filepath.Join(t.TempDir(), "config.yaml"))
	t.Setenv(config.APIEndpointEnv, server.URL)
	t.Setenv(config.AuthTokenEnv, "token")
	t.Setenv("TELOS_CONTEXT", "")

	for command, want := range map[string]string{"pause": "Paused goal_1\n", "resume": "Resumed goal_1\n"} {
		if out, stderr, err := inferenceCLIProcess(t, command, "goal_1"); err != nil || out != want {
			t.Fatalf("%s: stdout %q, stderr %q, %v", command, out, stderr, err)
		}
	}
	if _, stderr, err := inferenceCLIProcess(t, "pause", "goal_2"); err == nil || !strings.Contains(stderr, "admin privileges required") {
		t.Fatalf("non-admin pause: %q, %v", stderr, err)
	}
	if _, stderr, err := inferenceCLIProcess(t, "pause", "local_20261101_120000_00"); err == nil || !strings.Contains(stderr, "only for Cloud Goals") {
		t.Fatalf("local pause: %q, %v", stderr, err)
	}
	slices.Sort(calls)
	if want := []string{"POST /api/deployments/goal_1/pause", "POST /api/deployments/goal_1/resume", "POST /api/deployments/goal_2/pause"}; !slices.Equal(calls, want) {
		t.Fatalf("calls = %v", calls)
	}
}

func TestPlanAndApplyNameAMissingSpec(t *testing.T) {
	for _, command := range []string{"plan", "apply"} {
		t.Run(command, func(t *testing.T) {
			process := exec.Command(os.Args[0], "-test.run=^TestInferenceCLIProcess$", "--", command, "missing.md")
			process.Dir = t.TempDir()
			process.Env = append(os.Environ(), "TELOS_TEST_INFERENCE_COMMAND=1", "TELOS_SESSION_ID=")
			out, err := process.CombinedOutput()
			if err == nil || !strings.Contains(string(out), "error: spec file not found: missing.md\n") {
				t.Fatalf("err=%v output=%s", err, out)
			}
		})
	}
}

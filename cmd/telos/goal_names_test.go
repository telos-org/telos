package main

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/sessionapi"
)

func TestApplyAndPlanRefuseADuplicateGoalName(t *testing.T) {
	var publications, deployments atomic.Int32
	server := inferenceTestServer(t, map[string]http.HandlerFunc{
		"GET /api/deployments": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"deployments":[` +
				`{"id":"goal_gone","name":"example","state":"deleted"},` +
				`{"id":"goal_live","name":"example","state":"running","status":"ready"},` +
				`{"id":"goal_other","name":"other","state":"running","status":"ready"}]}`))
		},
		"POST /api/packages": func(w http.ResponseWriter, r *http.Request) {
			publications.Add(1)
			_, _ = w.Write([]byte(`{"ref":"@person/example:1.0.0"}`))
		},
		"POST /api/deployments": func(w http.ResponseWriter, r *http.Request) {
			deployments.Add(1)
			http.Error(w, "unexpected create", http.StatusInternalServerError)
		},
	})
	defer server.Close()
	configureCloudTest(t, server.URL)
	for _, name := range []string{"TELOS_CONTEXT", "TELOS_MODEL", "TELOS_THINKING", "TELOS_SESSION_ID", "TELOS_RUNTIME"} {
		t.Setenv(name, "")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "SPEC.md")
	if err := os.WriteFile(path, []byte("---\nname: example\nversion: 1.0.0\n---\n# Goal\nTest.\n# Acceptance\n- Test passes.\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, command := range []string{"apply", "plan"} {
		t.Run(command, func(t *testing.T) {
			process := exec.Command(os.Args[0], "-test.run=^TestInferenceCLIProcess$", "--", command, "SPEC.md")
			process.Dir = dir
			process.Env = append(os.Environ(), "TELOS_TEST_INFERENCE_COMMAND=1")
			out, err := process.CombinedOutput()
			want := "error: a Goal named example already exists in personal (goal_live)\n" +
				"To update it: telos " + command + " SPEC.md --goal goal_live\n"
			if err == nil || !strings.Contains(string(out), want) {
				t.Fatalf("err=%v output=%s\nwant %q", err, out, want)
			}
			if publications.Load() != 0 || deployments.Load() != 0 {
				t.Fatalf("publications=%d deployments=%d, want none", publications.Load(), deployments.Load())
			}
		})
	}
}

func TestDuplicateGoalErrorNamesTheContextWhenNeeded(t *testing.T) {
	err := &duplicateGoalError{
		name:     "reading-list",
		context:  "@team",
		id:       "goal_c7d2f0a4e8",
		command:  "apply",
		specArg:  "specs/reading list.md",
		followUp: "@team",
	}
	want := "a Goal named reading-list already exists in @team (goal_c7d2f0a4e8)\n" +
		"To update it: telos apply 'specs/reading list.md' --goal goal_c7d2f0a4e8 --context @team"
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}

func TestGoalJSONNamesSessionFieldsAsGoals(t *testing.T) {
	parent, name := "local_parent", "example"
	data, err := json.Marshal(goalJSON(sessionapi.SessionListItem{
		SessionID:       "local_child",
		ParentSessionID: &parent,
		SpecName:        &name,
		Status:          sessionapi.StatusRunning,
	}))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["goal_id"] != "local_child" || fields["parent_goal_id"] != "local_parent" || fields["spec_name"] != "example" {
		t.Fatalf("goal JSON = %s", data)
	}
	for key := range fields {
		if strings.Contains(key, "session") {
			t.Fatalf("goal JSON kept %q: %s", key, data)
		}
	}
}

func TestNotFoundErrorsNameTheGoal(t *testing.T) {
	missing := &cloud.APIError{StatusCode: 404, Detail: "deployment not found"}
	err := goalNotFound(missing, "goal_c7d2f0a4e8", "personal")
	if err.Error() != "Goal goal_c7d2f0a4e8 not found in personal" {
		t.Fatalf("Cloud not-found error = %q", err)
	}
	if !cloud.IsStatus(err, 404) {
		t.Fatal("reworded not-found error lost its HTTP status")
	}
	failed := &cloud.APIError{StatusCode: 500, Detail: "internal error"}
	if got := goalNotFound(failed, "goal_c7d2f0a4e8", "personal"); got != error(failed) {
		t.Fatalf("other Cloud errors changed: %v", got)
	}
	if got := localSessionNotFoundError("goal_c7d2f0a4e8").Error(); got != "Goal goal_c7d2f0a4e8 not found" {
		t.Fatalf("Cloud ID not-found error = %q", got)
	}
}

package main

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/sessionapi"
)

// applyTestSpec writes a spec for the Goal named example and returns its folder.
func applyTestSpec(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"TELOS_CONTEXT", "TELOS_MODEL", "TELOS_THINKING", "TELOS_SESSION_ID", "TELOS_RUNTIME"} {
		t.Setenv(name, "")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SPEC.md"), []byte("---\nname: example\nversion: 1.0.0\n---\n# Goal\nTest.\n# Acceptance\n- Test passes.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func applyTestProcess(t *testing.T, dir string, args ...string) (string, string, error) {
	t.Helper()
	process := exec.Command(os.Args[0], append([]string{"-test.run=^TestInferenceCLIProcess$", "--"}, args...)...)
	process.Dir = dir
	process.Env = append(os.Environ(), "TELOS_TEST_INFERENCE_COMMAND=1")
	var stderr strings.Builder
	process.Stderr = &stderr
	stdout, err := process.Output()
	return string(stdout), stderr.String(), err
}

func TestApplyDeploysToTheGoalTheSpecNames(t *testing.T) {
	goal := `{"id":"goal_live","name":"example","state":"running","status":"ready","can_deploy":true,"package_ref":"@person/example:1.0.0"}`
	for _, tt := range []struct {
		name, goals, route, want string
	}{
		{"existing Goal", `{"deployments":[{"id":"goal_other","name":"other","state":"running"},` + goal + `]}`, "PUT /api/deployments/goal_live", "Updated example"},
		{"no Goal with that name", `{"deployments":[{"id":"goal_other","name":"other","state":"running"}]}`, "POST /api/deployments", "Created example"},
		{"name of a Goal shared by link", `{"deployments":[{"id":"goal_shared","name":"example","state":"running","access_source":"link","can_deploy":true}]}`, "POST /api/deployments", "Created example"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var message any
			server := inferenceTestServer(t, map[string]http.HandlerFunc{
				"GET /api/deployments": func(w http.ResponseWriter, r *http.Request) {
					_, _ = w.Write([]byte(tt.goals))
				},
				"GET /api/deployments/goal_live":                         jsonResponse(map[string]any{"id": "goal_live", "name": "example", "package_ref": "@person/example:0.9.0"}),
				"GET /api/packages/person/example/versions/0.9.0":        http.NotFound,
				"GET /api/packages/person/example/versions/0.9.0/bundle": http.NotFound,
				"GET /api/packages/person/example/versions/1.0.0":        http.NotFound,
				"POST /api/packages": func(w http.ResponseWriter, r *http.Request) {
					_, _ = w.Write([]byte(`{"ref":"@person/example:1.0.0"}`))
				},
				tt.route: func(w http.ResponseWriter, r *http.Request) {
					var body map[string]any
					_ = json.NewDecoder(r.Body).Decode(&body)
					message = body["revision_message"]
					_, _ = w.Write([]byte(goal))
				},
			})
			defer server.Close()
			configureCloudTest(t, server.URL)
			dir := applyTestSpec(t)

			out, stderr, err := applyTestProcess(t, dir, "apply", "SPEC.md", "-m", "Send on Mondays")
			if err != nil || !strings.HasPrefix(out, tt.want+"\n") {
				t.Fatalf("err=%v stdout=%q stderr=%q, want %q", err, out, stderr, tt.want)
			}
			if message != "Send on Mondays" {
				t.Fatalf("revision_message = %#v", message)
			}
		})
	}
}

func TestApplySaysWhenTheGoalAlreadyRunsTheSpec(t *testing.T) {
	goal := `{"id":"goal_live","name":"example","state":"running","status":"ready","can_deploy":true,"package_digest":"sha256:aaa"}`
	for _, tt := range []struct {
		name, answer, want string
	}{
		{"same package", goal, "No changes to example\n"},
		{"new package", `{"id":"goal_live","name":"example","state":"running","status":"working","package_digest":"sha256:bbb"}`, "Updated example\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := inferenceTestServer(t, map[string]http.HandlerFunc{
				"GET /api/deployments": func(w http.ResponseWriter, r *http.Request) {
					_, _ = w.Write([]byte(`{"deployments":[` + goal + `]}`))
				},
				"GET /api/deployments/goal_live":                  jsonResponse(map[string]any{"id": "goal_live", "name": "example", "package_digest": "sha256:aaa"}),
				"GET /api/packages/person/example/versions/1.0.0": http.NotFound,
				"POST /api/packages": func(w http.ResponseWriter, r *http.Request) {
					_, _ = w.Write([]byte(`{"ref":"@person/example:1.0.0","version":"1.0.0"}`))
				},
				"PUT /api/deployments/goal_live": func(w http.ResponseWriter, r *http.Request) {
					_, _ = w.Write([]byte(tt.answer))
				},
			})
			defer server.Close()
			configureCloudTest(t, server.URL)
			dir := applyTestSpec(t)

			out, stderr, err := applyTestProcess(t, dir, "apply", "SPEC.md")
			if err != nil || !strings.HasPrefix(out, tt.want) {
				t.Fatalf("err=%v stdout=%q stderr=%q, want %q", err, out, stderr, tt.want)
			}
		})
	}
}

func TestApplyRefusesBeforeUploadingTheSpec(t *testing.T) {
	for _, tt := range []struct {
		name, goals string
		args        []string
		want        string
	}{
		{
			"Goal requires proposals",
			`{"deployments":[{"id":"goal_c7d2f0a4e8","name":"example","state":"running","can_deploy":false}]}`,
			[]string{"apply", "SPEC.md"},
			"error: example (goal_c7d2f0a4e8) requires proposals\nTo propose: telos propose SPEC.md -t \"TITLE\"\n",
		},
		{
			"force on a new Goal",
			`{"deployments":[]}`,
			[]string{"apply", "SPEC.md", "--force"},
			"error: --force applies only to an update, and personal has no Goal named example yet\n",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Any upload or deploy is an unexpected request, which fails the test.
			proposalCLITest(t, map[string]http.HandlerFunc{
				"GET /api/deployments": func(w http.ResponseWriter, r *http.Request) {
					_, _ = w.Write([]byte(tt.goals))
				},
			})
			out, stderr, err := applyTestProcess(t, applyTestSpec(t), tt.args...)
			if err == nil || out != "" || stderr != tt.want {
				t.Fatalf("err=%v stdout=%q stderr=%q, want %q", err, out, stderr, tt.want)
			}
		})
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

func TestGoalReadsNameTheSelectedContextWhenNotFound(t *testing.T) {
	server := inferenceTestServer(t, map[string]http.HandlerFunc{
		"GET /api/deployments/goal_missing": func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"detail":"deployment not found"}`, http.StatusNotFound)
		},
	})
	defer server.Close()
	configureCloudTest(t, server.URL)
	t.Setenv("TELOS_SESSION_ID", "")
	t.Setenv("TELOS_SESSION_DIR", filepath.Join(t.TempDir(), "sessions"))
	for _, context := range []string{"personal", "@telos"} {
		t.Setenv("TELOS_CONTEXT", context)
		for _, command := range []string{"describe", "logs"} {
			for _, explicit := range []bool{false, true} {
				args := []string{command, "goal_missing"}
				if explicit {
					args = append(args, "--context", context)
				}
				out, stderr, err := inferenceCLIProcess(t, args...)
				want := "error: Goal goal_missing not found in " + context + "\n"
				if err == nil || out != "" || stderr != want {
					t.Fatalf("%v: exit = %v, stdout = %q, stderr = %q; want %q", args, err, out, stderr, want)
				}
			}
		}
	}
}

func TestApplyCreatingAGoalThatRequiresProposalsSaysToPropose(t *testing.T) {
	proposalCLITest(t, map[string]http.HandlerFunc{
		"GET /api/packages/person/example/versions/1.0.0": http.NotFound,
		"POST /api/packages":                              respondJSON(map[string]any{"ref": "@person/example:1.0.0"}),
		"POST /api/deployments":                           respondError(http.StatusForbidden, "proposal_required", "example requires proposals"),
	})
	_, stderr, err := applyTestProcess(t, applyTestSpec(t), "apply", "SPEC.md")
	if want := "error: example requires proposals\nTo propose: telos propose SPEC.md -t \"TITLE\"\n"; err == nil || stderr != want {
		t.Fatalf("err=%v stderr=%q, want %q", err, stderr, want)
	}
}

func TestCommandsRefuseTheWrongKindOfID(t *testing.T) {
	proposalCLITest(t, nil)
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"describe", "c_4d7a1f0e"}, "error: telos describe takes a GOAL or PROPOSAL; c_4d7a1f0e is a comment ID\n"},
		{[]string{"apply", "c_4d7a1f0e"}, "error: telos apply takes SPEC.md or a PROPOSAL; c_4d7a1f0e is a comment ID\n"},
		{[]string{"review", "goal_c7d2f0a4e8", "--approved"}, "error: telos review takes a PROPOSAL; goal_c7d2f0a4e8 is a goal ID\n"},
	} {
		if _, stderr, err := inferenceCLIProcess(t, tt.args...); err == nil || stderr != tt.want {
			t.Fatalf("%v: err=%v stderr=%q, want %q", tt.args, err, stderr, tt.want)
		}
	}
}

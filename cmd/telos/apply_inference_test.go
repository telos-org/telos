package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/spec"
)

func configureInferenceApplyTest(t *testing.T, endpoint string) {
	t.Helper()
	configureCloudTest(t, endpoint)
	for _, name := range []string{"TELOS_CONTEXT", "TELOS_SESSION_ID", "TELOS_SESSION_DIR", "TELOS_RUNTIME", "TELOS_API_TOKEN"} {
		t.Setenv(name, "")
	}
	// Existing deployments must never inherit creation defaults, even invalid ones.
	t.Setenv("TELOS_MODEL", "unrequested/model")
	t.Setenv("TELOS_THINKING", "not-a-thinking-level")
	t.Setenv("TELOS_MAX_COST_USD", "not-a-number")
}

func inferenceCLIProcess(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	command := exec.Command(os.Args[0], append([]string{"-test.run=^TestInferenceCLIProcess$", "--"}, args...)...)
	command.Env = append(os.Environ(), "TELOS_TEST_INFERENCE_COMMAND=1")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	stdout, err := command.Output()
	return string(stdout), stderr.String(), err
}

func inferenceApplyHandlers(pkg *spec.ApplyPackage) map[string]http.HandlerFunc {
	handlers := registryApplyHandlers(pkg)
	handlers["GET /api/deployments/goal_test"] = jsonResponse(cloud.SessionRecord{
		ID: "goal_test", Name: "demo", PackageRef: "@telos/demo:1.2.3", PackageDigest: pkg.Digest,
		CurrentRevisionID: "rev_before", AgentModel: "telos-bifrost/telos/default", AgentThinking: "medium",
		Inference: &cloud.InferenceSummary{Source: "managed", Tier: "default"},
	})
	handlers["GET /api/deployments/goal_test/inference"] = jsonResponse(cloud.DeploymentInference{
		Selection:  &cloud.InferenceSelection{Source: "managed", Tier: "default"},
		Inference:  cloud.InferenceSummary{Source: "managed", Tier: "default"},
		AgentModel: "telos-bifrost/telos/default", AgentThinking: "medium",
	})
	return handlers
}

func TestApplyInferenceExplicitSettings(t *testing.T) {
	for _, tt := range []struct {
		name, model, thinking string
		selection             *cloud.InferenceSelection
	}{
		{"managed model", "telos/max", "", &cloud.InferenceSelection{Source: "managed", Tier: "max"}},
		{"thinking only", "", "high", nil},
		{"combined", "telos/max", "off", &cloud.InferenceSelection{Source: "managed", Tier: "max"}},
		{"native max", "", "max", nil},
		{"API key", "Work Anthropic/claude-test", "high", &cloud.InferenceSelection{Source: "byok", ConnectionID: "key_work", Model: "claude-test"}},
		{"subscription", "My ChatGPT/gpt-test", "", &cloud.InferenceSelection{Source: "subscription", ConnectionID: "sub_work", Model: "gpt-test"}},
		{"model with slash", "Work/Router/anthropic/claude-test", "", &cloud.InferenceSelection{Source: "byok", ConnectionID: "key_router", Model: "anthropic/claude-test"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var writes atomic.Int32
			handlers := inferenceApplyHandlers(testApplyPackage(t))
			handlers["PUT /api/deployments/goal_test"] = func(w http.ResponseWriter, r *http.Request) {
				writes.Add(1)
				if r.Header.Get("X-Telos-Org-Id") != "org_telos" || r.Header.Get("Authorization") != "Bearer control-token" {
					t.Error("settings write lost authentication or context")
				}
				var raw map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
					t.Error(err)
				}
				data, _ := json.Marshal(raw)
				var request cloud.SessionUpdateOptions
				_ = json.Unmarshal(data, &request)
				if request.PackageRef != "@telos/demo:1.2.3" || request.ExpectedCurrentRevisionID != "rev_before" {
					t.Errorf("update omitted package or revision: %s", data)
				}
				if tt.selection == nil {
					if _, exists := raw["inference"]; exists {
						t.Error("thinking-only update included a model")
					}
				} else if request.Inference == nil || *request.Inference != *tt.selection {
					t.Errorf("selection = %+v, want %+v", request.Inference, tt.selection)
				}
				if tt.thinking == "" {
					if _, exists := raw["agent_thinking"]; exists {
						t.Error("model-only update included thinking")
					}
				} else if request.AgentThinking == nil || *request.AgentThinking != tt.thinking {
					t.Errorf("thinking = %v", request.AgentThinking)
				}
				_ = json.NewEncoder(w).Encode(cloud.SessionRecord{
					ID: "goal_test", Name: "demo", CurrentRevisionID: "rev_after", PackageRef: "@telos/demo:1.2.3",
					Inference:  &cloud.InferenceSummary{Source: "managed", Tier: "max"},
					AgentModel: "telos-bifrost/telos/max", AgentThinking: "high",
				})
			}
			server := inferenceTestServer(t, handlers)
			defer server.Close()
			configureInferenceApplyTest(t, server.URL)
			args := []string{"@telos/demo:1.2.3", "--context", "@telos", "--json"}
			if tt.model != "" {
				args = append(args, "--model", tt.model)
			}
			if tt.thinking != "" {
				args = append(args, "--thinking", tt.thinking)
			}
			out := captureStdout(t, func() { cmdApply(args) })
			var change struct {
				Context   string              `json:"context"`
				Operation string              `json:"operation"`
				Goal      cloud.SessionRecord `json:"goal"`
			}
			if err := json.Unmarshal([]byte(out), &change); err != nil {
				t.Fatal(err)
			}
			if writes.Load() != 1 || change.Goal.ID != "goal_test" || change.Context != "@telos" || change.Operation != "updated" || change.Goal.AgentThinking != "high" {
				t.Fatalf("unexpected settings output: %s (writes %d)", out, writes.Load())
			}
		})
	}
}

func TestApplyInferenceInvalidFlagsDoNoWork(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected network access", http.StatusInternalServerError)
	}))
	defer server.Close()
	configureInferenceApplyTest(t, server.URL)
	for _, args := range [][]string{
		{}, {"--model", "telos/max"}, {"--goal", "sess_test"},
		{"@telos/demo:1.2.3", "--model", ""},
		{"@telos/demo:1.2.3", "--thinking", ""},
		{"@telos/demo:1.2.3", "--thinking", "HIGH"},
		{"--goal", "sess_test", "--model", ""},
		{"--goal", "sess_test", "--thinking", ""},
		{"--goal", "sess_test", "--thinking", "HIGH"},
		{"--goal", "local_test", "--thinking", "high"},
		{"--goal", "sess_test", "--thinking", "high", "--force"},
		{"--goal", "sess_test", "--thinking", "high", "--workspace", "."},
		{"--goal", "sess_test", "--thinking", "high", "--max-cost-usd", "1"},
		{"--goal", "local_test", "--thinking", "high", "--context", "@telos"},
		{"does-not-exist.md", "--goal", "sess_test", "--model", "telos/max"},
		{"@telos/demo:1.0.0", "--goal", "sess_test", "--thinking", "high"},
		{"does-not-exist.md", "--goal", "local_test", "--thinking", "high"},
	} {
		for _, dryRun := range []bool{false, true} {
			commandArgs := append([]string{"apply"}, args...)
			if dryRun {
				commandArgs = append(commandArgs, "--dry-run")
			}
			t.Run(strings.Join(commandArgs, " "), func(t *testing.T) {
				_, stderr, err := inferenceCLIProcess(t, commandArgs...)
				if err == nil || (!strings.Contains(stderr, "error:") && !strings.Contains(stderr, "flag provided but not defined:")) || requests.Load() != 0 {
					t.Fatalf("invalid command did work: err=%v, requests=%d, stderr=%s", err, requests.Load(), stderr)
				}
			})
		}
	}
	t.Setenv("TELOS_SESSION_ID", "sess_parent")
	t.Setenv("TELOS_API_TOKEN", "agent-token")
	_, stderr, err := inferenceCLIProcess(t, "apply", "@telos/demo:1.2.3", "--thinking", "high")
	if err == nil || !strings.Contains(stderr, "cannot be used from inside") || requests.Load() != 0 {
		t.Fatalf("agent bypassed apply restriction: %v %s", err, stderr)
	}
}

func TestSpecApplyIgnoresInferenceEnvironment(t *testing.T) {
	pkg := testApplyPackage(t)
	var updates atomic.Int32
	server := inferenceTestServer(t, map[string]http.HandlerFunc{
		"GET /api/deployments/sess_test": func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(cloud.SessionRecord{
				ID: "sess_test", PackageRef: "@telos/demo:1.2.3", PackageDigest: pkg.Digest,
			})
		},
		"GET /api/deployments": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"deployments":[{"id":"sess_test","name":"demo","state":"healthy","can_deploy":true}]}`))
		},
		"GET /api/packages/telos/demo/versions/1.2.3": func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]string{"scope": "telos", "name": "demo", "version": "1.2.3", "ref": "@telos/demo:1.2.3", "digest": pkg.Digest})
		},
		"GET /api/packages/telos/demo/versions/1.2.3/bundle": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(pkg.Bytes)
		},
		"PUT /api/deployments/sess_test": func(w http.ResponseWriter, r *http.Request) {
			updates.Add(1)
			var body map[string]json.RawMessage
			_ = json.NewDecoder(r.Body).Decode(&body)
			if len(body) != 1 || string(body["package_ref"]) != `"@telos/demo:1.2.3"` {
				t.Errorf("spec update included settings: %s", body)
			}
			_, _ = w.Write([]byte(`{"id":"sess_test","state":"deploying"}`))
		},
	})
	defer server.Close()
	configureInferenceApplyTest(t, server.URL)
	out := captureStdout(t, func() { cmdApply([]string{"@telos/demo:1.2.3", "--json"}) })
	if updates.Load() != 1 || !strings.Contains(out, `"operation": "updated"`) {
		t.Fatalf("existing spec update failed with inference env vars: %s", out)
	}
}

func TestApplyInferenceUsesTheGoalReceipt(t *testing.T) {
	handlers := inferenceApplyHandlers(testApplyPackage(t))
	handlers["PUT /api/deployments/goal_test"] = jsonResponse(cloud.SessionRecord{
		ID: "goal_test", Name: "demo", Status: "working", PackageRef: "@telos/demo:1.2.3", CurrentRevisionID: "rev_after",
		Inference:  &cloud.InferenceSummary{Source: "byok", Model: "claude-test", ConnectionName: "Work Anthropic"},
		AgentModel: "anthropic/claude-test", AgentThinking: "medium",
	})
	server := inferenceTestServer(t, handlers)
	defer server.Close()
	configureInferenceApplyTest(t, server.URL)
	out := captureStdout(t, func() { cmdApply([]string{"@telos/demo:1.2.3", "--model", "Work Anthropic/claude-test"}) })
	for _, want := range []string{"Updated demo", "Status    working", "Goal      goal_test", "Package   @telos/demo:1.2.3", "Model     Work Anthropic/claude-test", "Thinking  medium"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestApplyInferenceReportsCloudRejection(t *testing.T) {
	for _, rejection := range []struct {
		status        int
		code, message string
	}{
		{http.StatusForbidden, "inference_forbidden", "Only workspace owners and admins can change inference settings."},
		{http.StatusConflict, "stale_current_revision", "The Goal changed during the update."},
	} {
		t.Run(rejection.code, func(t *testing.T) {
			handlers := inferenceApplyHandlers(testApplyPackage(t))
			var writes atomic.Int32
			handlers["PUT /api/deployments/goal_test"] = func(w http.ResponseWriter, r *http.Request) {
				writes.Add(1)
				respondError(rejection.status, rejection.code, rejection.message)(w, r)
			}
			server := inferenceTestServer(t, handlers)
			defer server.Close()
			configureInferenceApplyTest(t, server.URL)
			out, stderr, err := inferenceCLIProcess(t, "apply", "@telos/demo:1.2.3", "--model", "telos/max")
			if err == nil || out != "" || !strings.Contains(stderr, rejection.message) || writes.Load() != 1 {
				t.Fatalf("rejection not reported: %v %s %s (writes %d)", err, out, stderr, writes.Load())
			}
		})
	}
}

func TestApplySettingsWithAnUnchangedLocalSpec(t *testing.T) {
	for _, change := range []struct {
		name, model, thinking, operation string
	}{
		{"model", "telos/max", "medium", "update"},
		{"thinking", "", "high", "update"},
		{"identical settings", "telos/default", "medium", "unchanged"},
	} {
		for _, dryRun := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/dry-run=%v", change.name, dryRun), func(t *testing.T) {
				pkg := testApplyPackage(t)
				markdown, _, err := spec.ApplyPackageSpec(pkg.Bytes)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(t.TempDir(), "SPEC.md")
				if err := os.WriteFile(path, markdown, 0o600); err != nil {
					t.Fatal(err)
				}
				compiled, err := spec.CompileEnvironment(path)
				if err != nil {
					t.Fatal(err)
				}
				pkg, err = spec.BuildApplyPackageWithSkillRefs(compiled, map[string]string{})
				if err != nil {
					t.Fatal(err)
				}
				handlers := inferenceApplyHandlers(pkg)
				handlers["GET /api/account/bootstrap"] = dryRunPublishScope
				var publications, updates atomic.Int32
				handlers["POST /api/packages"] = func(w http.ResponseWriter, r *http.Request) {
					publications.Add(1)
					jsonResponse(cloud.PackageVersionRecord{Ref: "@telos/demo:1.2.3", Digest: pkg.Digest})(w, r)
				}
				handlers["PUT /api/deployments/goal_test"] = func(w http.ResponseWriter, r *http.Request) {
					updates.Add(1)
					var request cloud.SessionUpdateOptions
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
					}
					if request.PackageRef != "@telos/demo:1.2.3" || request.ExpectedCurrentRevisionID != "rev_before" ||
						request.AgentThinking == nil || *request.AgentThinking != change.thinking ||
						(change.model == "") != (request.Inference == nil) {
						t.Errorf("update = %+v", request)
					}
					accepted := cloud.SessionRecord{
						ID: "goal_test", Name: "demo", PackageRef: request.PackageRef, PackageDigest: pkg.Digest,
						CurrentRevisionID: "rev_before", AgentModel: "telos-bifrost/telos/default", AgentThinking: change.thinking,
						Inference: &cloud.InferenceSummary{Source: "managed", Tier: "default"},
					}
					if change.operation == "update" {
						accepted.CurrentRevisionID = "rev_after"
					}
					if change.model == "telos/max" {
						accepted.AgentModel = "telos-bifrost/telos/max"
						accepted.Inference.Tier = "max"
					}
					jsonResponse(accepted)(w, r)
				}
				server := inferenceTestServer(t, handlers)
				defer server.Close()
				configureInferenceApplyTest(t, server.URL)
				args := []string{"apply", path, "--thinking", change.thinking, "--json"}
				if change.model != "" {
					args = append(args, "--model", change.model)
				}
				operation, writes := change.operation, int32(0)
				if dryRun {
					args = append(args, "--dry-run")
				} else {
					writes = 1
					if operation == "update" {
						operation = "updated"
					}
				}
				stdout, stderr, err := inferenceCLIProcess(t, args...)
				var result struct{ Operation string }
				if err != nil || json.Unmarshal([]byte(stdout), &result) != nil || result.Operation != operation ||
					updates.Load() != writes || publications.Load() != writes {
					t.Fatalf("err=%v stdout=%s stderr=%s updates=%d publications=%d", err, stdout, stderr, updates.Load(), publications.Load())
				}
				if !bytes.Equal(mustReadFile(t, path), markdown) {
					t.Fatal("applying settings rewrote the spec")
				}
			})
		}
	}
}

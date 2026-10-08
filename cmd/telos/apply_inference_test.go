package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/sessionapi"
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

func initialCloudInference() cloud.DeploymentInferenceState {
	return cloud.DeploymentInferenceState{
		Inference:  cloud.InferenceSummary{Source: "managed", Tier: "default"},
		AgentModel: "telos-bifrost/telos/default", AgentThinking: "medium", Revision: 7,
	}
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
		{"native minimal", "", "minimal", nil},
		{"API key", "Work Anthropic/claude-test", "high", &cloud.InferenceSelection{Source: "byok", ConnectionID: "key_work", Model: "claude-test"}},
		{"subscription", "My ChatGPT/gpt-test", "", &cloud.InferenceSelection{Source: "subscription", ConnectionID: "sub_work", Model: "gpt-test"}},
		{"model with slash", "Work/Router/anthropic/claude-test", "", &cloud.InferenceSelection{Source: "byok", ConnectionID: "key_router", Model: "anthropic/claude-test"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var writes atomic.Int32
			server := inferenceTestServer(t, map[string]http.HandlerFunc{
				"GET /api/deployments/sess_test/inference": func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("X-Telos-Org-Id") != "org_telos" {
						t.Error("settings read lost the selected context")
					}
					_ = json.NewEncoder(w).Encode(initialCloudInference())
				},
				"PUT /api/deployments/sess_test/inference": func(w http.ResponseWriter, r *http.Request) {
					writes.Add(1)
					if r.Header.Get("X-Telos-Org-Id") != "org_telos" || r.Header.Get("Authorization") != "Bearer control-token" {
						t.Error("settings write lost authentication or context")
					}
					var raw map[string]json.RawMessage
					if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
						t.Error(err)
					}
					data, _ := json.Marshal(raw)
					var request cloud.DeploymentInferenceRequest
					_ = json.Unmarshal(data, &request)
					if request.RequestID == "" || request.ExpectedRevision != 7 {
						t.Errorf("request identity/revision = %+v", request)
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
					state := initialCloudInference()
					state.Request, state.Status, state.Revision = &request, "pending", 8
					w.WriteHeader(http.StatusAccepted)
					_ = json.NewEncoder(w).Encode(state)
				},
			})
			defer server.Close()
			configureInferenceApplyTest(t, server.URL)
			args := []string{"--session", "sess_test", "--context", "@telos", "--json"}
			if tt.model != "" {
				args = append(args, "--model", tt.model)
			}
			if tt.thinking != "" {
				args = append(args, "--thinking", tt.thinking)
			}
			out := captureStdout(t, func() { cmdApply(args) })
			var receipt inferenceReceipt
			if err := json.Unmarshal([]byte(out), &receipt); err != nil {
				t.Fatal(err)
			}
			if writes.Load() != 1 || receipt.Status != "pending" || receipt.Settings.Thinking != "medium" || receipt.Settings.Model != "telos-bifrost/telos/default" || receipt.Context != "@telos" {
				t.Fatalf("pending change presented incorrect confirmed settings: %s (writes %d)", out, writes.Load())
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
		{}, {"--model", "telos/max"}, {"--session", "sess_test"},
		{"--session", "sess_test", "--model", ""},
		{"--session", "sess_test", "--thinking", ""},
		{"--session", "sess_test", "--thinking", "HIGH"},
		{"--session", "bad_id", "--thinking", "high"},
		{"--session", "sess_test", "--thinking", "high", "--force"},
		{"--session", "sess_test", "--thinking", "high", "--workspace", "."},
		{"--session", "sess_test", "--thinking", "high", "--max-cost-usd", "1"},
		{"--session", "local_test", "--thinking", "high", "--context", "@telos"},
		{"does-not-exist.md", "--session", "sess_test", "--model", "telos/max"},
		{"@telos/demo:1.0.0", "--session", "sess_test", "--thinking", "high"},
		{"does-not-exist.md", "--session", "local_test", "--thinking", "high"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			_, stderr, err := inferenceCLIProcess(t, append([]string{"apply"}, args...)...)
			if err == nil || !strings.Contains(stderr, "error:") || requests.Load() != 0 {
				t.Fatalf("invalid command did work: err=%v, requests=%d, stderr=%s", err, requests.Load(), stderr)
			}
		})
	}
	t.Setenv("TELOS_SESSION_ID", "sess_parent")
	t.Setenv("TELOS_API_TOKEN", "agent-token")
	_, stderr, err := inferenceCLIProcess(t, "apply", "--session", "sess_test", "--thinking", "high")
	if err == nil || !strings.Contains(stderr, "cannot be used from inside") || requests.Load() != 0 {
		t.Fatalf("agent bypassed apply restriction: %v %s", err, stderr)
	}
}

func TestApplyInferenceOutcomesAndLostReplies(t *testing.T) {
	for _, outcome := range []string{"applied", "applying", "partial", "rejected", "unknown", "bad-request-id", "bad-revision", "bad-status", "missing-status", "invalid-json", "conflict", "unavailable", "lost-reply"} {
		t.Run(outcome, func(t *testing.T) {
			var writes atomic.Int32
			var submittedID string
			server := inferenceTestServer(t, map[string]http.HandlerFunc{
				"GET /api/deployments/sess_test/inference": func(w http.ResponseWriter, r *http.Request) {
					_ = json.NewEncoder(w).Encode(initialCloudInference())
				},
				"PUT /api/deployments/sess_test/inference": func(w http.ResponseWriter, r *http.Request) {
					writes.Add(1)
					var request cloud.DeploymentInferenceRequest
					_ = json.NewDecoder(r.Body).Decode(&request)
					submittedID = request.RequestID
					state := initialCloudInference()
					state.Request, state.Revision, state.Status = &request, 8, outcome
					switch outcome {
					case "applied", "partial":
						state.AgentModel = "telos-bifrost/telos/max"
						if outcome == "applied" {
							state.AgentThinking = "high"
						} else {
							state.Error = "thinking level is unsupported"
						}
					case "bad-request-id":
						state.Status, request.RequestID = "applied", "another-request"
					case "bad-revision":
						state.Status, state.Revision = "applied", 99
					case "missing-status":
						state.Status = ""
					case "invalid-json":
						_, _ = w.Write([]byte(`{`))
						return
					case "conflict", "unavailable":
						code := http.StatusConflict
						if outcome == "unavailable" {
							code = http.StatusServiceUnavailable
						}
						http.Error(w, "change not confirmed", code)
						return
					case "lost-reply":
						connection, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						_ = connection.Close()
						return
					}
					_ = json.NewEncoder(w).Encode(state)
				},
			})
			configureInferenceApplyTest(t, server.URL)
			stdout, stderr, err := inferenceCLIProcess(t, "apply", "--session", "sess_test", "--model", "telos/max", "--thinking", "high", "--json")
			server.Close()
			wantSuccess := outcome == "applied" || outcome == "applying"
			if (err == nil) != wantSuccess || writes.Load() != 1 {
				t.Fatalf("err=%v, writes=%d, stdout=%s, stderr=%s", err, writes.Load(), stdout, stderr)
			}
			if validInferenceStatus(outcome) {
				var receipt inferenceReceipt
				if json.Unmarshal([]byte(stdout), &receipt) != nil || receipt.Status != outcome || receipt.RequestID != submittedID {
					t.Fatalf("lost structured outcome: %s", stdout)
				}
				if outcome == "partial" && (receipt.Settings.Model != "telos-bifrost/telos/max" || receipt.Settings.Thinking != "medium") {
					t.Fatalf("partial result lied about confirmed pair: %s", stdout)
				}
			} else if !strings.Contains(stderr, submittedID) || !strings.Contains(stderr, "Check confirmed settings with: telos describe") || stdout != "" {
				t.Fatalf("unconfirmed request lost identity or suggested success: %s %s", stdout, stderr)
			}
		})
	}
}

func TestApplyLocalInferenceQueuesAndDescribeShowsConfirmation(t *testing.T) {
	configureInferenceApplyTest(t, "http://unused.invalid")
	root := t.TempDir()
	t.Setenv("TELOS_SESSION_DIR", root)
	id := "local_settings"
	dir := filepath.Join(root, id)
	path := filepath.Join(dir, "session.json")
	manifest := &sessionapi.Manifest{SessionID: id, SessionKind: sessionapi.KindController, Runtime: sessionapi.RuntimeLocal, Config: sessionapi.SessionConfig{Model: "provider/old", Thinking: "medium"}}
	if err := sessionapi.WriteManifest(path, manifest); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() { cmdApply([]string{"--session", id, "--model", "provider/new", "--thinking", "max"}) })
	want := "Session   local_settings\nModel     provider/old -> provider/new (next turn)\nThinking  medium -> max (next turn)\n"
	if out != want {
		t.Fatalf("pending receipt: %s", out)
	}
	out = captureStdout(t, func() { cmdDescribe([]string{id}) })
	if !strings.Contains(out, "Model     provider/old -> provider/new (next turn)") || !strings.Contains(out, "Thinking  medium -> max (next turn)") {
		t.Fatalf("describe omitted queued settings: %s", out)
	}
	for _, unwanted := range []string{"Settings", "Request", "pending", "not confirmed"} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("describe exposed request details %q: %s", unwanted, out)
		}
	}
	out = captureStdout(t, func() { cmdDescribe([]string{id, "--json"}) })
	assertDescribeInferenceSettings(t, out, sessionapi.InferenceSettings{Model: "provider/old", Thinking: "medium"})
	update, err := sessionapi.ClaimInferenceUpdate(path, "attempt", "receipt.json", sessionapi.InferenceSettings{})
	if err != nil || update == nil || *update.Model != "provider/new" || *update.Thinking != "max" {
		t.Fatalf("saved update = %+v, %v", update, err)
	}
	if err := sessionapi.FinishInferenceUpdate(path, update.RequestID, "applied", "", &sessionapi.InferenceSettings{Model: "provider/new", Thinking: "max"}); err != nil {
		t.Fatal(err)
	}
	out = captureStdout(t, func() { cmdDescribe([]string{id}) })
	if !strings.Contains(out, "Model     provider/new") || !strings.Contains(out, "Thinking  max") || strings.Contains(out, "->") {
		t.Fatalf("describe did not settle on confirmed settings: %s", out)
	}
	out = captureStdout(t, func() { cmdDescribe([]string{id, "--json"}) })
	assertDescribeInferenceSettings(t, out, sessionapi.InferenceSettings{Model: "provider/new", Thinking: "max"})
}

func TestDescribeInferenceCompatibilityAndFreshness(t *testing.T) {
	for _, tt := range []struct {
		name       string
		statusCode int
		status     string
	}{
		{"pending", http.StatusOK, "pending"},
		{"applying", http.StatusOK, "applying"},
		{"applied", http.StatusOK, "applied"},
		{"rejected", http.StatusOK, "rejected"},
		{"unknown", http.StatusOK, "unknown"},
		{"not found", http.StatusNotFound, ""},
		{"method not allowed", http.StatusMethodNotAllowed, ""},
		{"not implemented", http.StatusNotImplemented, ""},
		{"forbidden", http.StatusForbidden, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := inferenceTestServer(t, map[string]http.HandlerFunc{
				"GET /api/deployments/sess_test": func(w http.ResponseWriter, r *http.Request) {
					_ = json.NewEncoder(w).Encode(cloud.SessionRecord{ID: "sess_test", State: "running", AgentModel: "telos-bifrost/telos/default", AgentThinking: "medium"})
				},
				"GET /api/deployments/sess_test/inference": func(w http.ResponseWriter, r *http.Request) {
					if tt.statusCode != http.StatusOK {
						http.Error(w, "settings unavailable", tt.statusCode)
						return
					}
					state := initialCloudInference()
					if tt.status == "applied" {
						state.AgentModel, state.AgentThinking = "telos-bifrost/telos/max", "high"
					}
					thinking := "high"
					state.Request = &cloud.DeploymentInferenceRequest{RequestID: "other-browser", ExpectedRevision: 6, Inference: &cloud.InferenceSelection{Source: "managed", Tier: "max"}, AgentThinking: &thinking}
					state.Status = tt.status
					_ = json.NewEncoder(w).Encode(state)
				},
			})
			defer server.Close()
			configureInferenceApplyTest(t, server.URL)
			for _, contextArgs := range [][]string{nil, {"--context", "@telos"}} {
				args := append([]string{"sess_test"}, contextArgs...)
				out := captureStdout(t, func() { cmdDescribe(append(args, "--json")) })
				var description cloudDescription
				if err := json.Unmarshal([]byte(out), &description); err != nil || description.SessionRecord == nil || description.ID != "sess_test" {
					t.Fatalf("describe lost existing fields: %v %s", err, out)
				}
				text := captureStdout(t, func() { cmdDescribe(args) })
				switch tt.statusCode {
				case http.StatusOK:
					confirmed := sessionapi.InferenceSettings{Model: "telos-bifrost/telos/default", Thinking: "medium"}
					model, thinking := "telos/default", "medium"
					if tt.status == "applied" {
						confirmed = sessionapi.InferenceSettings{Model: "telos-bifrost/telos/max", Thinking: "high"}
						model, thinking = "telos/max", "high"
					}
					assertDescribeInferenceSettings(t, out, confirmed)
					if description.AgentModel != confirmed.Model || description.AgentThinking != confirmed.Thinking {
						t.Fatalf("describe showed stale session settings: %s", out)
					}
					if tt.status == "pending" || tt.status == "applying" {
						model += " -> telos/max (next turn)"
						thinking += " -> high (next turn)"
					} else if strings.Contains(text, "->") {
						t.Fatalf("describe showed an inactive change as queued: %s", text)
					}
					if !strings.Contains(text, "Model     "+model) || !strings.Contains(text, "Thinking  "+thinking) {
						t.Fatalf("text settings disagreed with change state: %s", text)
					}
					for _, unwanted := range []string{"Settings", "Request", "(requested)", "applied", "other-browser"} {
						if strings.Contains(text, unwanted) {
							t.Fatalf("describe exposed stale settings or request details %q: %s", unwanted, text)
						}
					}
				case http.StatusForbidden:
					if description.InferenceError == "" || !strings.Contains(text, "unavailable:") {
						t.Fatalf("describe hid settings fetch failure: %s %s", out, text)
					}
				default:
					if description.InferenceState != nil || description.InferenceError != "" || !strings.Contains(text, "telos-bifrost/telos/default") {
						t.Fatalf("older Cloud describe compatibility broken: %s %s", out, text)
					}
				}
			}
		})
	}
}

func assertDescribeInferenceSettings(t *testing.T, output string, expected sessionapi.InferenceSettings) {
	t.Helper()
	var description struct {
		InferenceState map[string]json.RawMessage `json:"inference_state"`
	}
	if err := json.Unmarshal([]byte(output), &description); err != nil {
		t.Fatal(err)
	}
	if len(description.InferenceState) != 1 {
		t.Fatalf("describe must only include confirmed inference settings: %s", output)
	}
	var settings sessionapi.InferenceSettings
	if err := json.Unmarshal(description.InferenceState["settings"], &settings); err != nil || settings != expected {
		t.Fatalf("describe settings = %+v, want %+v: %v", settings, expected, err)
	}
}

func TestApplyLocalInferenceRetryKeepsQueuedRequest(t *testing.T) {
	configureInferenceApplyTest(t, "http://unused.invalid")
	root := t.TempDir()
	t.Setenv("TELOS_SESSION_DIR", root)
	id := "local_retry"
	dir := filepath.Join(root, id)
	path := filepath.Join(dir, "session.json")
	if err := sessionapi.WriteManifest(path, &sessionapi.Manifest{
		SessionID: id, SessionKind: sessionapi.KindController,
		Config: sessionapi.SessionConfig{Model: "provider/old", Thinking: "medium"},
	}); err != nil {
		t.Fatal(err)
	}
	receipt, submitErr := applySessionInference(id, "provider/new", "high", "")
	pending, err := store().Inference(id)
	if err != nil || pending.Update == nil || pending.Update.Status != "pending" {
		t.Fatalf("notification failure lost saved change: %+v %v", pending, err)
	}
	if receipt == nil || submitErr != nil || receipt.Status != "pending" {
		t.Fatalf("queue failed: %+v %v", receipt, submitErr)
	}
	// Omitting a requested field is different intent; it cannot reuse the ID.
	if receipt, err := applySessionInference(id, "provider/new", "", ""); err == nil || receipt != nil || !strings.Contains(err.Error(), "already in progress") {
		t.Fatalf("different flags replaced the pending change: %+v %v", receipt, err)
	}
	receipt, err = applySessionInference(id, "provider/new", "high", "")
	if err != nil || receipt == nil || receipt.RequestID != pending.Update.RequestID || receipt.Revision != pending.Revision || receipt.Status != "pending" {
		t.Fatalf("retry did not preserve saved request: %+v %v", receipt, err)
	}

}

func TestSpecApplyIgnoresInferenceEnvironment(t *testing.T) {
	pkg := testApplyPackage(t)
	var updates atomic.Int32
	server := inferenceTestServer(t, map[string]http.HandlerFunc{
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
	out := captureStdout(t, func() { cmdApply([]string{"@telos/demo:1.2.3", "--session", "sess_test", "--json"}) })
	if updates.Load() != 1 || !strings.Contains(out, `"operation": "updated"`) {
		t.Fatalf("existing spec update failed with inference env vars: %s", out)
	}
}

func TestApplyInferenceRejectsInvalidPreflightWithoutWriting(t *testing.T) {
	var writes atomic.Int32
	server := inferenceTestServer(t, map[string]http.HandlerFunc{
		"GET /api/deployments/sess_test/inference": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{}`))
		},
		"PUT /api/deployments/sess_test/inference": func(w http.ResponseWriter, r *http.Request) {
			writes.Add(1)
		},
	})
	defer server.Close()
	configureInferenceApplyTest(t, server.URL)
	if receipt, err := applySessionInference("sess_test", "telos/max", "", ""); err == nil || receipt != nil || writes.Load() != 0 {
		t.Fatalf("malformed preflight proceeded: %+v, %v, writes=%d", receipt, err, writes.Load())
	}
}

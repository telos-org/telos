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
			server := inferenceTestServer(t, map[string]http.HandlerFunc{
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
					_ = json.NewEncoder(w).Encode(cloud.DeploymentInference{
						Inference:  cloud.InferenceSummary{Source: "managed", Tier: "max"},
						AgentModel: "telos-bifrost/telos/max", AgentThinking: "high", Generation: 8, Applied: true,
					})
				},
			})
			defer server.Close()
			configureInferenceApplyTest(t, server.URL)
			args := []string{"--goal", "sess_test", "--context", "@telos", "--json"}
			if tt.model != "" {
				args = append(args, "--model", tt.model)
			}
			if tt.thinking != "" {
				args = append(args, "--thinking", tt.thinking)
			}
			out := captureStdout(t, func() { cmdApply(args) })
			var change inferenceChange
			if err := json.Unmarshal([]byte(out), &change); err != nil {
				t.Fatal(err)
			}
			if writes.Load() != 1 || change.SessionID != "sess_test" || change.Context != "@telos" || change.Generation != 8 || !change.Applied || change.AgentThinking != "high" {
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
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			_, stderr, err := inferenceCLIProcess(t, append([]string{"apply"}, args...)...)
			if err == nil || (!strings.Contains(stderr, "error:") && !strings.Contains(stderr, "flag provided but not defined:")) || requests.Load() != 0 {
				t.Fatalf("invalid command did work: err=%v, requests=%d, stderr=%s", err, requests.Load(), stderr)
			}
		})
	}
	t.Setenv("TELOS_SESSION_ID", "sess_parent")
	t.Setenv("TELOS_API_TOKEN", "agent-token")
	_, stderr, err := inferenceCLIProcess(t, "apply", "--goal", "sess_test", "--thinking", "high")
	if err == nil || !strings.Contains(stderr, "cannot be used from inside") || requests.Load() != 0 {
		t.Fatalf("agent bypassed apply restriction: %v %s", err, stderr)
	}
}

func TestApplyLocalInferenceRejectedWithoutChangingManifest(t *testing.T) {
	configureInferenceApplyTest(t, "http://unused.invalid")
	root := t.TempDir()
	t.Setenv("TELOS_SESSION_DIR", root)
	id := "local_settings"
	dir := filepath.Join(root, id)
	path := filepath.Join(dir, "session.json")
	if err := sessionapi.WriteManifest(path, &sessionapi.Manifest{
		SessionID: id, SessionKind: sessionapi.KindController,
		Config: sessionapi.SessionConfig{Model: "provider/old", Thinking: "medium"},
	}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if change, err := applySessionInference(id, "provider/new", "high", ""); err == nil || change != nil || !strings.Contains(err.Error(), "telos apply only updates Telos Cloud Goals") {
		t.Fatalf("local settings apply not rejected: %+v %v", change, err)
	}
	stdout, stderr, err := inferenceCLIProcess(t, "apply", "--goal", id, "--thinking", "high")
	if err == nil || stdout != "" || !strings.Contains(stderr, "telos apply only updates Telos Cloud Goals") {
		t.Fatalf("local CLI settings apply not rejected: err=%v, stdout=%s, stderr=%s", err, stdout, stderr)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("rejected local settings apply changed manifest: %v", err)
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
	out := captureStdout(t, func() { cmdApply([]string{"@telos/demo:1.2.3", "--goal", "sess_test", "--json"}) })
	if updates.Load() != 1 || !strings.Contains(out, `"operation": "updated"`) {
		t.Fatalf("existing spec update failed with inference env vars: %s", out)
	}
}

func TestApplyInferenceTextShowsUndeliveredChange(t *testing.T) {
	server := inferenceTestServer(t, map[string]http.HandlerFunc{
		"PUT /api/deployments/sess_test/inference": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"inference":{"source":"byok","model":"claude-test","connection_name":"Work Anthropic"},"agent_model":"anthropic/claude-test","agent_thinking":"high","generation":3,"applied":false}`))
		},
	})
	defer server.Close()
	configureInferenceApplyTest(t, server.URL)
	out := captureStdout(t, func() { cmdApply([]string{"--goal", "sess_test", "--model", "Work Anthropic/claude-test"}) })
	for _, want := range []string{"Model     Work Anthropic/claude-test", "Thinking  high", "Applies   next turn after the Goal's runtime receives it"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestApplyInferenceReportsCloudRejection(t *testing.T) {
	server := inferenceTestServer(t, map[string]http.HandlerFunc{
		"PUT /api/deployments/sess_test/inference": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"detail":"changing the inference connection is not supported"}`))
		},
	})
	defer server.Close()
	configureInferenceApplyTest(t, server.URL)
	_, stderr, err := inferenceCLIProcess(t, "apply", "--goal", "sess_test", "--model", "telos/max")
	if err == nil || !strings.Contains(stderr, "changing the inference connection is not supported") {
		t.Fatalf("rejection not reported: %v %s", err, stderr)
	}
}

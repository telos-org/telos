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
	"sync/atomic"
	"testing"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/config"
)

func TestResolveCloudInference(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("X-Telos-Inference-Version") != "2" {
			t.Error("missing shared discovery version header")
		}
		if r.Method != http.MethodGet || r.URL.Path != "/api/inference/connections" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"errors":{},"connections":[
			{"source":"subscription","id":"conn_rohan","name":"openai-rohan","provider":"chatgpt-codex","status":"connected","account_label":"rohan@example.com","plan":"pro"},
			{"source":"subscription","id":"conn_james","name":"openai-james","provider":"chatgpt-codex","status":"needs_attention","account_label":"james@example.com"}
		]}`))
	}))
	defer server.Close()
	client := cloud.NewClient(server.URL, "token")

	managed, err := resolveCloudInference(client, "telos/max")
	if err != nil || managed.Source != "managed" || managed.Tier != "max" {
		t.Fatalf("managed = %#v, err = %v", managed, err)
	}
	if requests.Load() != 0 {
		t.Fatal("managed selection requested discovery")
	}
	subscription, err := resolveCloudInference(client, "openai-rohan/gpt-5.6-sol")
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatalf("named selection used %d requests, want one", requests.Load())
	}
	if subscription.Source != "subscription" ||
		subscription.ConnectionID != "conn_rohan" ||
		subscription.Model != "gpt-5.6-sol" {
		t.Fatalf("subscription = %#v", subscription)
	}
	if _, err := resolveCloudInference(client, "openai-james/gpt-5.6-sol"); err == nil {
		t.Fatal("needs-attention connection resolved")
	}
	if _, err := resolveCloudInference(client, "missing/gpt-5.6-sol"); err == nil {
		t.Fatal("missing connection resolved")
	}
}

func TestCloudApplyModelPrecedenceIgnoresLegacyDefault(t *testing.T) {
	pkg := testApplyPackage(t)
	for _, tt := range []struct {
		name  string
		env   string
		flags []string
		want  *cloud.InferenceSelection
	}{
		{name: "workspace default"},
		{
			name: "environment override",
			env:  "telos/max",
			want: &cloud.InferenceSelection{Source: "managed", Tier: "max"},
		},
		{
			name:  "flag overrides environment",
			env:   "telos/max",
			flags: []string{"--model", "telos/default"},
			want:  &cloud.InferenceSelection{Source: "managed", Tier: "default"},
		},
		{
			name:  "empty flag defers to workspace",
			env:   "telos/max",
			flags: []string{"--model", ""},
		},
		{
			name:  "subscription override",
			flags: []string{"--model", "openai-rohan/gpt-5.6-sol"},
			want: &cloud.InferenceSelection{
				Source: "subscription", ConnectionID: "conn_rohan", Model: "gpt-5.6-sol",
			},
		},
		{
			name:  "API key override",
			flags: []string{"--model", "Work Anthropic/claude-test", "--thinking", "high"},
			want:  &cloud.InferenceSelection{Source: "byok", ConnectionID: "key_work", Model: "claude-test"},
		},
		{
			name:  "named connection with provider-prefixed model",
			env:   "telos/max",
			flags: []string{"--model", "Work/Router/anthropic/claude-test"},
			want:  &cloud.InferenceSelection{Source: "byok", ConnectionID: "key_router", Model: "anthropic/claude-test"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			requests := make(chan map[string]json.RawMessage, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/packages/telos/demo/versions/1.2.3":
					_ = json.NewEncoder(w).Encode(map[string]string{
						"scope": "telos", "name": "demo", "version": "1.2.3",
						"ref": "@telos/demo:1.2.3", "digest": pkg.Digest,
					})
				case r.Method == http.MethodGet && r.URL.Path == "/api/packages/telos/demo/versions/1.2.3/bundle":
					_, _ = w.Write(pkg.Bytes)
				case r.Method == http.MethodGet && r.URL.Path == "/api/inference/connections":
					_, _ = w.Write([]byte(`{"errors":{},"connections":[{"source":"subscription","id":"conn_rohan","name":"openai-rohan","provider":"chatgpt-codex","status":"connected"},{"source":"byok","status":"saved","id":"key_work","name":"Work Anthropic","provider":"anthropic"},{"source":"byok","status":"saved","id":"key_router","name":"Work/Router","provider":"openrouter"}]}`))
				case r.Method == http.MethodPost && r.URL.Path == "/api/deployments":
					var request map[string]json.RawMessage
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Errorf("decode deployment request: %v", err)
						http.Error(w, "invalid request", http.StatusBadRequest)
						return
					}
					requests <- request
					_, _ = w.Write([]byte(`{"id":"sess_test","name":"demo","state":"provisioning"}`))
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			configureCloudTest(t, server.URL)
			for _, name := range []string{"TELOS_CONTEXT", "TELOS_THINKING", "TELOS_MAX_COST_USD", "TELOS_SESSION_ID"} {
				t.Setenv(name, "")
			}
			t.Setenv("TELOS_MODEL", tt.env)
			if err := os.WriteFile(os.Getenv(config.ConfigPathEnv), []byte("default_model: telos/max\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			captureStdout(t, func() {
				cmdApply(append([]string{"@telos/demo:1.2.3", "--json"}, tt.flags...))
			})

			var request map[string]json.RawMessage
			select {
			case request = <-requests:
			default:
				t.Fatal("apply did not create a deployment")
			}
			if _, ok := request["agent_model"]; ok {
				t.Fatal("apply sent a legacy agent_model override")
			}
			raw, present := request["inference"]
			if tt.want == nil {
				if present {
					t.Fatalf("apply should defer to the workspace, got inference %s", raw)
				}
				return
			}
			var got cloud.InferenceSelection
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if got != *tt.want {
				t.Fatalf("inference = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func inferenceTestServer(t *testing.T, overrides map[string]http.HandlerFunc) *httptest.Server {
	t.Helper()
	responses := map[string]string{
		"GET /api/account/bootstrap":     `{"personal_org_id":"org_personal","organizations":[{"id":"org_personal","handle":"person","role":"owner"},{"id":"org_telos","handle":"telos","role":"owner"}]}`,
		"GET /api/inference/connections": `{"errors":{},"connections":[{"source":"subscription","id":"sub_work","name":"My ChatGPT","provider":"chatgpt-codex","status":"connected"},{"source":"byok","status":"saved","id":"key_work","name":"Work Anthropic","provider":"anthropic","api_key":"never-print-this-key"},{"source":"byok","status":"saved","id":"key_router","name":"Work/Router","provider":"openrouter"}]}`,
		"GET /api/inference/preference":  `{"selection":{"source":"byok","connection_id":"key_work","model":"claude-test"}}`,
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		key := r.Method + " " + r.URL.Path
		if handler := overrides[key]; handler != nil {
			handler(w, r)
			return
		}
		if body, ok := responses[key]; ok {
			_, _ = w.Write([]byte(body))
			return
		}
		t.Errorf("unexpected request: %s", key)
		http.NotFound(w, r)
	}))
}

func TestCloudInferenceSelectsSavedConnections(t *testing.T) {
	server := inferenceTestServer(t, nil)
	defer server.Close()
	for _, tt := range []struct {
		name, model string
		want        cloud.InferenceSelection
	}{
		{"API key name", "Work Anthropic/claude-test", cloud.InferenceSelection{Source: "byok", ConnectionID: "key_work", Model: "claude-test"}},
		{"slashes in both name and model", "Work/Router/anthropic/claude-test", cloud.InferenceSelection{Source: "byok", ConnectionID: "key_router", Model: "anthropic/claude-test"}},
		{"subscription", "My ChatGPT/gpt-test", cloud.InferenceSelection{Source: "subscription", ConnectionID: "sub_work", Model: "gpt-test"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveCloudInference(cloud.NewClient(server.URL, "token"), tt.model)
			if err != nil || got == nil || *got != tt.want {
				t.Fatalf("selection = %#v, err = %v; want %#v", got, err, tt.want)
			}
		})
	}
}

func TestCloudInferenceRejectsAmbiguityAndPreservesModelSlashes(t *testing.T) {
	connections := []cloud.InferenceConnection{
		{ID: "sub", Name: "Work", Source: "subscription"},
		{ID: "key", Name: "Work", Source: "byok"},
		{ID: "slash", Name: "Work/Router", Source: "byok"},
	}
	for _, model := range []string{"Work/model", "Work/Router/vendor/model"} {
		if _, _, err := selectInferenceConnection(connections, model); err == nil || !strings.Contains(err.Error(), "ambiguous") {
			t.Fatalf("selector %q: %v", model, err)
		}
	}
	got, model, err := selectInferenceConnection(connections[2:], "Work/Router/vendor/model")
	if err != nil || got.ID != "slash" || model != "vendor/model" {
		t.Fatalf("named connection: %#v %q %v", got, model, err)
	}
}

func TestCloudInferenceRequiresCompleteConnectionInventory(t *testing.T) {
	for _, source := range []string{"subscription", "byok"} {
		t.Run(source, func(t *testing.T) {
			server := inferenceTestServer(t, map[string]http.HandlerFunc{
				"GET /api/inference/connections": func(w http.ResponseWriter, r *http.Request) {
					_ = json.NewEncoder(w).Encode(map[string]any{
						"connections": []cloud.InferenceConnection{{ID: "available", Name: "Available", Source: "byok", Status: "saved"}},
						"errors":      map[string]string{source: "unavailable"},
					})
				},
			})
			defer server.Close()
			configureCloudTest(t, server.URL)
			t.Setenv("TELOS_CONTEXT", "")
			out := captureStdout(t, func() { cmdConfig([]string{"--json"}) })
			var report configReport
			if err := json.Unmarshal([]byte(out), &report); err != nil || !strings.Contains(report.Error, "unavailable") || len(report.Connections) == 0 || report.WorkspaceDefault == nil {
				t.Fatalf("partial config lost available settings or lookup error: %s, %v", out, err)
			}
			client := cloud.NewClient(server.URL, "token")
			for _, model := range []string{"Work Anthropic/claude-test", "My ChatGPT/gpt-test"} {
				if _, err := resolveCloudInference(client, model); err == nil || !strings.Contains(err.Error(), "cannot resolve") {
					t.Fatalf("selection guessed through an incomplete inventory: %v", err)
				}
			}
			for _, model := range []string{"", "telos/default", "telos/max"} {
				if _, err := resolveCloudInference(client, model); err != nil {
					t.Fatalf("default or managed inference depends on connection discovery: %v", err)
				}
			}
		})
	}
}

func TestInferenceCLIProcess(t *testing.T) {
	if os.Getenv("TELOS_TEST_INFERENCE_COMMAND") != "1" {
		return
	}
	index := slices.Index(os.Args, "--")
	if index == -1 {
		os.Exit(2)
	}
	args := os.Args[index+1:]
	switch args[0] {
	case "apply":
		cmdApply(args[1:])
	case "config":
		cmdConfig(args[1:])
	default:
		os.Exit(2)
	}
	os.Exit(0)
}

func TestCloudApplyInferenceErrors(t *testing.T) {
	var publications, deployments atomic.Int32
	server := inferenceTestServer(t, map[string]http.HandlerFunc{
		"POST /api/packages": func(w http.ResponseWriter, r *http.Request) {
			publications.Add(1)
			_, _ = w.Write([]byte(`{"ref":"@person/example:1.0.0"}`))
		},
		"POST /api/deployments": func(w http.ResponseWriter, r *http.Request) {
			deployments.Add(1)
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"detail":"model is unavailable for this connection"}`))
		},
	})
	defer server.Close()
	configureCloudTest(t, server.URL)
	for _, name := range []string{"TELOS_CONTEXT", "TELOS_MODEL", "TELOS_THINKING", "TELOS_MAX_COST_USD", "TELOS_SESSION_ID", "TELOS_RUNTIME", "TELOS_API_TOKEN"} {
		t.Setenv(name, "")
	}
	path := filepath.Join(t.TempDir(), "SPEC.md")
	if err := os.WriteFile(path, []byte("---\nname: example\nversion: 1.0.0\nplatform: cloud\n---\n# Goal\nTest.\n# Acceptance\n- Test passes.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name     string
		args     []string
		message  string
		requests int32
	}{
		{"API key model rejected by Cloud", []string{"apply", path, "--model", "Work Anthropic/missing"}, "model is unavailable for this connection (HTTP 422)", 1},
		{"subscription model rejected by Cloud", []string{"apply", path, "--model", "My ChatGPT/missing"}, "model is unavailable for this connection (HTTP 422)", 1},
		{"missing connection", []string{"apply", path, "--model", "Missing/model"}, "not found", 0},
		{"missing model", []string{"apply", path, "--model", "Work Anthropic/"}, "model ID is required", 0},
		{"invalid syntax", []string{"apply", path, "--model", "Work Anthropic"}, "--model must be", 0},
		{"model cannot change existing deployment", []string{"apply", path, "--session", "sess_existing", "--model", "Work Anthropic/claude-test"}, "cannot update an existing", 0},
		{"empty context", []string{"config", "--context", ""}, "requires @handle", 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			publications.Store(0)
			deployments.Store(0)
			args := append([]string{"-test.run=^TestInferenceCLIProcess$", "--"}, tt.args...)
			command := exec.Command(os.Args[0], args...)
			command.Env = append(os.Environ(), "TELOS_TEST_INFERENCE_COMMAND=1")
			out, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(out), tt.message) {
				t.Fatalf("command err=%v output=%s", err, out)
			}
			if publications.Load() != tt.requests || deployments.Load() != tt.requests {
				t.Fatalf("publications=%d deployments=%d, want %d each", publications.Load(), deployments.Load(), tt.requests)
			}
		})
	}
}

func TestCloudReceiptShowsSavedInference(t *testing.T) {
	session := cloud.SessionRecord{ID: "sess_test", AgentModel: "internal/model", AgentThinking: "high", Inference: &cloud.InferenceSummary{Source: "byok", ConnectionName: "Work Anthropic", Provider: "anthropic", Model: "claude-test"}}
	var out bytes.Buffer
	printCloudSessionDescription(&out, session)
	for _, want := range []string{"API key", "Work Anthropic", "claude-test", "high (requested)"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("description omitted %q: %s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "internal/model") {
		t.Fatal("description replaced the public selection with the internal runtime model")
	}
	encoded := captureStdout(t, func() { printCloudSessionJSON(&session, "@telos") })
	var decoded struct{ Inference cloud.InferenceSummary }
	if err := json.Unmarshal([]byte(encoded), &decoded); err != nil || decoded.Inference.ConnectionName != "Work Anthropic" {
		t.Fatalf("JSON omitted inference: %s, %v", encoded, err)
	}
}

func TestCloudReceiptPreservesCustomManagedModel(t *testing.T) {
	for _, tt := range []struct{ model, want string }{
		{"openai/gpt-4.1", "openai/gpt-4.1"},
		{"telos-bifrost/telos/default", "telos/default"},
	} {
		session := cloud.SessionRecord{ID: "sess_test", AgentModel: tt.model, Inference: &cloud.InferenceSummary{Source: "managed", Tier: "default", Model: tt.model}}
		var description, receipt bytes.Buffer
		printCloudSessionDescription(&description, session)
		printCloudSessionReceipt(&receipt, "created", &session)
		for _, output := range []string{description.String(), receipt.String()} {
			if got := configOutputValue(t, output, "Model"); got != tt.want {
				t.Fatalf("displayed model = %q, want %q", got, tt.want)
			}
		}
	}
}

func TestCloudInferenceRejectsLegacyConnectionResponse(t *testing.T) {
	server := inferenceTestServer(t, map[string]http.HandlerFunc{
		"GET /api/inference/connections": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"connections":[{"id":"old","name":"Work","status":"connected"}]}`))
		},
	})
	defer server.Close()
	if _, err := resolveCloudInference(cloud.NewClient(server.URL, "token"), "Work/model"); err == nil || !strings.Contains(err.Error(), "update Cloud") {
		t.Fatalf("unsupported Cloud response was accepted: %v", err)
	}
}

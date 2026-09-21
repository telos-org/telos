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
)

func inferenceTestServer(t *testing.T, overrides map[string]http.HandlerFunc) *httptest.Server {
	t.Helper()
	responses := map[string]string{
		"GET /api/account/bootstrap":          `{"personal_org_id":"org_personal","organizations":[{"id":"org_personal","handle":"person","role":"owner"},{"id":"org_telos","handle":"telos","role":"owner"}]}`,
		"GET /api/inference/connections":      `{"connections":[{"id":"sub_work","name":"My ChatGPT","provider":"chatgpt-codex","status":"connected"}]}`,
		"GET /api/inference/api-keys":         `{"connections":[{"id":"key_work","name":"Work Anthropic","provider":"anthropic","api_key":"never-print-this-key"},{"id":"key_router","name":"Work/Router","provider":"openrouter"}]}`,
		"GET /api/inference/catalog":          `{"models":[{"id":"gpt-test","label":"GPT Test","provider":"chatgpt-codex","connection_ids":["sub_work"]},{"id":"private-model","provider":"chatgpt-codex","connection_ids":["another-account"]}]}`,
		"GET /api/inference/api-keys/catalog": `{"enabled":true,"connections":[{"connection_id":"key_work","fetched_at":"2026-09-15T00:00:00Z","models":[{"id":"claude-test","label":"Claude Test","provider":"anthropic"}]},{"connection_id":"key_router","models":[{"id":"anthropic/claude-test","provider":"openrouter"}]}]}`,
		"GET /api/inference/preference":       `{"selection":{"source":"byok","connection_id":"key_work","model":"claude-test"}}`,
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
	if _, err := resolveCloudInference(cloud.NewClient(server.URL, "token"), "My ChatGPT/private-model"); err == nil {
		t.Fatal("model restricted to a different subscription was selected")
	}
}

func TestCloudInferenceRejectsAmbiguityAndPreservesModelSlashes(t *testing.T) {
	connections := []inferenceConnection{
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
	for _, endpoint := range []string{"/api/inference/connections", "/api/inference/api-keys"} {
		t.Run(endpoint, func(t *testing.T) {
			server := inferenceTestServer(t, map[string]http.HandlerFunc{
				"GET " + endpoint: func(w http.ResponseWriter, r *http.Request) {
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
				},
			})
			defer server.Close()
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

func TestCloudInferenceRejectsUnusableAPIKeyCatalog(t *testing.T) {
	for _, tt := range []struct {
		name, catalog string
	}{
		{"disabled", `{"enabled":false}`},
		{"missing connection", `{"enabled":true,"connections":[]}`},
		{"stale models", `{"enabled":true,"connections":[{"connection_id":"key_work","error":"Provider unavailable","models":[{"id":"claude-test","provider":"anthropic"}]}]}`},
		{"wrong provider", `{"enabled":true,"connections":[{"connection_id":"key_work","models":[{"id":"claude-test","provider":"openai"}]}]}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := inferenceTestServer(t, map[string]http.HandlerFunc{
				"GET /api/inference/api-keys/catalog": func(w http.ResponseWriter, r *http.Request) {
					_, _ = w.Write([]byte(tt.catalog))
				},
			})
			defer server.Close()
			selection, err := resolveCloudInference(cloud.NewClient(server.URL, "token"), "Work Anthropic/claude-test")
			if err == nil || selection != nil {
				t.Fatalf("unusable catalog allowed selection: %#v, %v", selection, err)
			}
		})
	}
}

func TestConfigJSONShowsWorkspaceDefaultAndOverridesWithoutKeys(t *testing.T) {
	server := inferenceTestServer(t, nil)
	defer server.Close()
	configureCloudTest(t, server.URL)
	t.Setenv("TELOS_CONTEXT", "@telos")
	t.Setenv("TELOS_MODEL", "telos/max")
	t.Setenv("TELOS_THINKING", "high")
	path := os.Getenv("TELOS_CONFIG")
	before := []byte("context: personal\n")
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() { cmdConfig([]string{"--json"}) })
	var report configReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if report.Authentication != "valid" || report.Context != "@telos" || report.WorkspaceDefault == nil || report.WorkspaceDefault.Source != "byok" || report.ModelOverride != "telos/max" || report.ThinkingOverride != "high" {
		t.Fatalf("report = %#v", report)
	}
	for _, connection := range report.Connections {
		if connection.Source == "byok" && connection.Status != "saved" {
			t.Fatalf("stored key claims a connection test: %#v", connection)
		}
	}
	for _, secret := range []string{"control-token", "never-print-this-key", "auth_token", "api_key"} {
		if strings.Contains(out, secret) {
			t.Fatalf("JSON exposed a credential field/value: %q", secret)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("config inspection changed saved settings: %s, %v", after, err)
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

func TestInvalidInferenceStopsBeforePublishingOrDeployment(t *testing.T) {
	var mutations atomic.Int32
	base := inferenceTestServer(t, nil)
	defer base.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			mutations.Add(1)
			http.Error(w, "unexpected mutation", http.StatusBadRequest)
			return
		}
		base.Config.Handler.ServeHTTP(w, r)
	}))
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
		name    string
		args    []string
		message string
	}{
		{"unavailable model", []string{"apply", path, "--model", "Work Anthropic/missing"}, "unavailable"},
		{"missing connection", []string{"apply", path, "--model", "Missing/model"}, "not found"},
		{"removed connection ID", []string{"apply", path, "--connection-id", "key_work"}, "flag provided but not defined"},
		{"model cannot change existing deployment", []string{"apply", path, "--session", "sess_existing", "--model", "Work Anthropic/claude-test"}, "cannot update an existing"},
		{"removed workspace setter", []string{"config", "--workspace-model", "telos/max"}, "flag provided but not defined"},
		{"removed model listing", []string{"config", "--models"}, "flag provided but not defined"},
		{"removed model refresh", []string{"config", "--refresh"}, "flag provided but not defined"},
		{"removed config connection ID", []string{"config", "--connection-id", "key_work"}, "flag provided but not defined"},
		{"empty context", []string{"config", "--context", ""}, "requires @handle"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			args := append([]string{"-test.run=^TestInferenceCLIProcess$", "--"}, tt.args...)
			command := exec.Command(os.Args[0], args...)
			command.Env = append(os.Environ(), "TELOS_TEST_INFERENCE_COMMAND=1")
			out, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(out), tt.message) {
				t.Fatalf("command err=%v output=%s", err, out)
			}
		})
	}
	if mutations.Load() != 0 {
		t.Fatalf("invalid inference caused %d remote mutations", mutations.Load())
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

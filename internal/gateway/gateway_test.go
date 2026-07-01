package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveUsesEnvGatewayFirst(t *testing.T) {
	t.Setenv("TELOS_GATEWAY_BASE_URL", "https://env.example.com/v1")
	t.Setenv("TELOS_GATEWAY_API_KEY", "env-key")

	cred, err := Resolve("sess-1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cred.BaseURL != "https://env.example.com/v1" || cred.APIKey != "env-key" {
		t.Fatalf("credential: %+v", cred)
	}
	if cred.Transport != TransportOpenAISync || cred.Kind != KindOpenAI {
		t.Fatalf("gateway defaults: %+v", cred)
	}
	if cred.CostHardLimit {
		t.Fatalf("env BYO gateway should not hard-enforce unknown cost by default: %+v", cred)
	}
}

func TestResolveEnvGatewayUsesBifrostDefaults(t *testing.T) {
	t.Setenv("TELOS_GATEWAY_BASE_URL", "https://env.example.com/openai")
	t.Setenv("TELOS_GATEWAY_API_KEY", "env-key")
	t.Setenv("TELOS_GATEWAY_KIND", KindBifrost)
	t.Setenv("TELOS_GATEWAY_HEADERS", `{"x-bf-vk":"sk-bf"}`)

	cred, err := Resolve("sess-1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cred.Transport != TransportBifrostAsync || cred.Kind != KindBifrost || cred.Headers["x-bf-vk"] != "sk-bf" {
		t.Fatalf("credential: %+v", cred)
	}
}

func TestValidateTransportAndKindRejectsInvalidValues(t *testing.T) {
	if _, _, err := ValidateTransportAndKind("bogus", "openai"); err == nil {
		t.Fatal("expected invalid transport error")
	}
	if _, _, err := ValidateTransportAndKind("openai_sync", "bogus"); err == nil {
		t.Fatal("expected invalid kind error")
	}
}

func TestResolveEnvGatewayCanBeBillingBacked(t *testing.T) {
	t.Setenv("TELOS_GATEWAY_BASE_URL", "https://env.example.com/v1")
	t.Setenv("TELOS_GATEWAY_API_KEY", "env-key")
	t.Setenv("TELOS_ENV_ID", "env_test")
	t.Setenv("TELOS_BILLING_ENV_TOKEN", "billing-token")

	cred, err := Resolve("sess-1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !cred.CostHardLimit {
		t.Fatalf("billing-backed env gateway should hard-enforce unknown cost: %+v", cred)
	}
}

func TestResolveUsesBYOConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(`
gateway:
  mode: byo
  base_url: https://file.example.com/v1
  api_key: file-key
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TELOS_CONFIG", cfgPath)
	t.Setenv("TELOS_GATEWAY_BASE_URL", "")
	t.Setenv("TELOS_GATEWAY_API_KEY", "")

	cred, err := Resolve("sess-1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cred.BaseURL != "https://file.example.com/v1" || cred.APIKey != "file-key" {
		t.Fatalf("credential: %+v", cred)
	}
	if cred.CostHardLimit {
		t.Fatalf("BYO config should not hard-enforce unknown cost: %+v", cred)
	}
}

func TestResolveManagedMintsSessionKey(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Header.Get("Authorization") != "Bearer login-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"session_id": "sess-managed",
			"base_url":   "https://managed.example.com/v1",
			"api_key":    "sk-managed",
			"budget_usd": 5.0,
		})
	}))
	defer server.Close()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("billing_endpoint: "+server.URL+"\nauth_token: login-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TELOS_CONFIG", cfgPath)
	t.Setenv("TELOS_GATEWAY_BASE_URL", "")
	t.Setenv("TELOS_GATEWAY_API_KEY", "")

	cred, err := Resolve("sess-managed")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if gotPath != "/api/billing/session-key" {
		t.Fatalf("path: got %q", gotPath)
	}
	if cred.BaseURL != "https://managed.example.com/v1" || cred.APIKey != "sk-managed" {
		t.Fatalf("credential: %+v", cred)
	}
	if !cred.CostHardLimit {
		t.Fatalf("managed gateway should hard-enforce unknown cost: %+v", cred)
	}
}

func TestProbeResponsesOpenAISync(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if r.Header.Get("x-extra") != "ok" {
			t.Fatalf("missing extra header: %s", r.Header.Get("x-extra"))
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":     "resp_probe",
			"status": "completed",
			"output": []map[string]any{{
				"type": "message",
				"content": []map[string]string{{
					"type": "output_text",
					"text": "OK",
				}},
			}},
		})
	}))
	defer server.Close()

	err := ProbeResponses(server.URL, "test-key", "test-model", ProbeConfig{Headers: map[string]string{"x-extra": "ok"}})
	if err != nil {
		t.Fatalf("ProbeResponses: %v", err)
	}
	if gotPath != "/responses" || gotAuth != "Bearer test-key" || gotBody["model"] != "test-model" {
		t.Fatalf("request: path=%q auth=%q body=%v", gotPath, gotAuth, gotBody)
	}
}

func TestProbeResponsesBifrostAsync(t *testing.T) {
	var submitSeen, pollSeen bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openai/responses" {
			t.Fatalf("path: got %q", r.URL.Path)
		}
		switch {
		case r.Header.Get("x-bf-async") == "true":
			submitSeen = true
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "job_1", "status": "processing"})
		case r.Header.Get("x-bf-async-id") == "job_1":
			pollSeen = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":     "resp_probe",
				"status": "completed",
				"output": []map[string]any{{
					"type": "message",
					"content": []map[string]string{{
						"type": "output_text",
						"text": "OK",
					}},
				}},
			})
		default:
			t.Fatalf("missing async headers: %+v", r.Header)
		}
	}))
	defer server.Close()

	err := ProbeResponses(server.URL+"/openai", "test-key", "test-model", ProbeConfig{Transport: TransportBifrostAsync})
	if err != nil {
		t.Fatalf("ProbeResponses: %v", err)
	}
	if !submitSeen || !pollSeen {
		t.Fatalf("submitSeen=%t pollSeen=%t", submitSeen, pollSeen)
	}
}

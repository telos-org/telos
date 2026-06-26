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
	t.Setenv("TELOS_LITELLM_BASE_URL", "https://env.example.com/v1")
	t.Setenv("TELOS_LITELLM_API_KEY", "env-key")

	cred, err := Resolve("sess-1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cred.BaseURL != "https://env.example.com/v1" || cred.APIKey != "env-key" {
		t.Fatalf("credential: %+v", cred)
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
	t.Setenv("TELOS_LITELLM_BASE_URL", "")
	t.Setenv("TELOS_LITELLM_API_KEY", "")

	cred, err := Resolve("sess-1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cred.BaseURL != "https://file.example.com/v1" || cred.APIKey != "file-key" {
		t.Fatalf("credential: %+v", cred)
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
	if err := os.WriteFile(cfgPath, []byte("api_endpoint: "+server.URL+"\nauth_token: login-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TELOS_CONFIG", cfgPath)
	t.Setenv("TELOS_LITELLM_BASE_URL", "")
	t.Setenv("TELOS_LITELLM_API_KEY", "")

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
}

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigFromFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfgPath, []byte("api_endpoint: https://test.example.com\nauth_token: secret123\ncontext: org_telos\n"), 0o644)

	t.Setenv("TELOS_CONFIG", cfgPath)
	t.Setenv("TELOS_API_ENDPOINT", "")
	t.Setenv("TELOS_AUTH_TOKEN", "")
	t.Setenv("TELOS_CONTEXT", "")

	cfg := LoadConfig()
	if cfg.APIEndpoint != "https://test.example.com" {
		t.Errorf("endpoint: got %q", cfg.APIEndpoint)
	}
	if cfg.AuthToken != "secret123" {
		t.Errorf("token: got %q", cfg.AuthToken)
	}
	if cfg.Context != "org_telos" {
		t.Errorf("context: got %q", cfg.Context)
	}
}

func TestLoadConfigEnvOverride(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfgPath, []byte("api_endpoint: https://file.example.com\nauth_token: file-token\ncontext: org_file\n"), 0o644)

	t.Setenv("TELOS_CONFIG", cfgPath)
	t.Setenv("TELOS_API_ENDPOINT", "https://env.example.com")
	t.Setenv("TELOS_AUTH_TOKEN", "env-token")
	t.Setenv("TELOS_CONTEXT", "@env")

	cfg := LoadConfig()
	if cfg.APIEndpoint != "https://env.example.com" {
		t.Errorf("endpoint: got %q (should be env override)", cfg.APIEndpoint)
	}
	if cfg.AuthToken != "env-token" {
		t.Errorf("token: got %q (should be env override)", cfg.AuthToken)
	}
	if cfg.Context != "@env" {
		t.Errorf("context: got %q (should be env override)", cfg.Context)
	}
}

func TestLoadStoredConfigIgnoresEnvOverride(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfgPath, []byte("api_endpoint: https://file.example.com\nauth_token: file-token\ncontext: org_file\n"), 0o644)

	t.Setenv("TELOS_CONFIG", cfgPath)
	t.Setenv("TELOS_API_ENDPOINT", "https://env.example.com")
	t.Setenv("TELOS_AUTH_TOKEN", "env-token")
	t.Setenv("TELOS_CONTEXT", "@env")

	cfg := LoadStoredConfig()
	if cfg.APIEndpoint != "https://file.example.com" {
		t.Errorf("endpoint: got %q", cfg.APIEndpoint)
	}
	if cfg.AuthToken != "file-token" {
		t.Errorf("token: got %q", cfg.AuthToken)
	}
	if cfg.Context != "org_file" {
		t.Errorf("context: got %q", cfg.Context)
	}
}

func TestLoadConfigMissing(t *testing.T) {
	t.Setenv("TELOS_CONFIG", filepath.Join(t.TempDir(), "nonexistent.yaml"))
	t.Setenv("TELOS_API_ENDPOINT", "")
	t.Setenv("TELOS_AUTH_TOKEN", "")
	t.Setenv("TELOS_CONTEXT", "")

	cfg := LoadConfig()
	if cfg.APIEndpoint != "" {
		t.Errorf("expected empty endpoint, got %q", cfg.APIEndpoint)
	}
}

func TestLoadConfigIgnoresRemovedOrganizationNames(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(cfgPath, []byte("org_id: org_legacy\n"), 0o644)
	t.Setenv("TELOS_CONFIG", cfgPath)
	t.Setenv("TELOS_CONTEXT", "")
	t.Setenv("TELOS_ORG_ID", "org_env_legacy")

	if got := LoadConfig().Context; got != "" {
		t.Fatalf("removed organization config unexpectedly selected %q", got)
	}
}

func TestSaveAndLoadConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "telos", "config.yaml")
	t.Setenv("TELOS_CONFIG", cfgPath)
	t.Setenv("TELOS_API_ENDPOINT", "")
	t.Setenv("TELOS_AUTH_TOKEN", "")
	t.Setenv("TELOS_CONTEXT", "")

	err := SaveConfig(&Config{
		APIEndpoint: "https://saved.example.com",
		AuthToken:   "saved-token",
		Context:     "org_saved",
	})
	if err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	cfg := LoadConfig()
	if cfg.APIEndpoint != "https://saved.example.com" {
		t.Errorf("endpoint: got %q", cfg.APIEndpoint)
	}
	if cfg.AuthToken != "saved-token" {
		t.Errorf("token: got %q", cfg.AuthToken)
	}
	if cfg.Context != "org_saved" {
		t.Errorf("context: got %q", cfg.Context)
	}
}

func TestIsConfigured(t *testing.T) {
	t.Setenv("TELOS_CONFIG", filepath.Join(t.TempDir(), "nonexistent.yaml"))
	t.Setenv("TELOS_API_ENDPOINT", "")
	t.Setenv("TELOS_AUTH_TOKEN", "")
	t.Setenv("TELOS_CONTEXT", "")

	if IsConfigured() {
		t.Error("should not be configured with no file or env")
	}

	t.Setenv("TELOS_API_ENDPOINT", "https://example.com")
	if IsConfigured() {
		t.Error("endpoint without token should not be configured")
	}

	t.Setenv("TELOS_AUTH_TOKEN", "token")
	if !IsConfigured() {
		t.Error("auth token should mark cloud access configured")
	}
}

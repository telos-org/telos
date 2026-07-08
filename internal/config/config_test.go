package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigFromFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfgPath, []byte("api_endpoint: https://test.example.com\nauth_token: secret123\norg_id: org_telos\n"), 0o644)

	t.Setenv("TELOS_CONFIG", cfgPath)
	t.Setenv("TELOS_API_ENDPOINT", "")
	t.Setenv("TELOS_AUTH_TOKEN", "")

	cfg := LoadConfig()
	if cfg.APIEndpoint != "https://test.example.com" {
		t.Errorf("endpoint: got %q", cfg.APIEndpoint)
	}
	if cfg.AuthToken != "secret123" {
		t.Errorf("token: got %q", cfg.AuthToken)
	}
	if cfg.OrgID != "org_telos" {
		t.Errorf("org id: got %q", cfg.OrgID)
	}
}

func TestLoadConfigEnvOverride(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfgPath, []byte("api_endpoint: https://file.example.com\nauth_token: file-token\norg_id: org_file\n"), 0o644)

	t.Setenv("TELOS_CONFIG", cfgPath)
	t.Setenv("TELOS_API_ENDPOINT", "https://env.example.com")
	t.Setenv("TELOS_AUTH_TOKEN", "env-token")
	t.Setenv("TELOS_ORG_ID", "org_env")

	cfg := LoadConfig()
	if cfg.APIEndpoint != "https://env.example.com" {
		t.Errorf("endpoint: got %q (should be env override)", cfg.APIEndpoint)
	}
	if cfg.AuthToken != "env-token" {
		t.Errorf("token: got %q (should be env override)", cfg.AuthToken)
	}
	if cfg.OrgID != "org_env" {
		t.Errorf("org id: got %q (should be env override)", cfg.OrgID)
	}
}

func TestLoadConfigMissing(t *testing.T) {
	t.Setenv("TELOS_CONFIG", filepath.Join(t.TempDir(), "nonexistent.yaml"))
	t.Setenv("TELOS_API_ENDPOINT", "")
	t.Setenv("TELOS_AUTH_TOKEN", "")

	cfg := LoadConfig()
	if cfg.APIEndpoint != "" {
		t.Errorf("expected empty endpoint, got %q", cfg.APIEndpoint)
	}
}

func TestSaveAndLoadConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "telos", "config.yaml")
	t.Setenv("TELOS_CONFIG", cfgPath)
	t.Setenv("TELOS_API_ENDPOINT", "")
	t.Setenv("TELOS_AUTH_TOKEN", "")

	err := SaveConfig(&Config{
		APIEndpoint: "https://saved.example.com",
		AuthToken:   "saved-token",
		OrgID:       "org_saved",
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
	if cfg.OrgID != "org_saved" {
		t.Errorf("org id: got %q", cfg.OrgID)
	}
}

func TestIsConfigured(t *testing.T) {
	t.Setenv("TELOS_CONFIG", filepath.Join(t.TempDir(), "nonexistent.yaml"))
	t.Setenv("TELOS_API_ENDPOINT", "")
	t.Setenv("TELOS_AUTH_TOKEN", "")

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

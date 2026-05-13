package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigFromFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfgPath, []byte("api_endpoint: https://test.example.com\nauth_token: secret123\n"), 0o644)

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
}

func TestLoadConfigEnvOverride(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfgPath, []byte("api_endpoint: https://file.example.com\nauth_token: file-token\n"), 0o644)

	t.Setenv("TELOS_CONFIG", cfgPath)
	t.Setenv("TELOS_API_ENDPOINT", "https://env.example.com")
	t.Setenv("TELOS_AUTH_TOKEN", "env-token")

	cfg := LoadConfig()
	if cfg.APIEndpoint != "https://env.example.com" {
		t.Errorf("endpoint: got %q (should be env override)", cfg.APIEndpoint)
	}
	if cfg.AuthToken != "env-token" {
		t.Errorf("token: got %q (should be env override)", cfg.AuthToken)
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
}

func TestEnvironmentAccess(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, "environments.yaml")
	t.Setenv("TELOS_ENVIRONMENTS_CONFIG", envPath)
	t.Setenv("TELOS_CONFIG", filepath.Join(dir, "config.yaml"))

	envs := []EnvironmentAccess{
		{ID: "env-1", Token: "token-1"},
		{ID: "env-2", Token: "token-2"},
	}
	err := SaveEnvironmentAccess(envs)
	if err != nil {
		t.Fatalf("SaveEnvironmentAccess: %v", err)
	}

	loaded := LoadEnvironmentAccess()
	if len(loaded) != 2 {
		t.Fatalf("expected 2 environments, got %d", len(loaded))
	}
	if loaded[0].ID != "env-1" || loaded[0].Token != "token-1" {
		t.Errorf("env-1: got %+v", loaded[0])
	}
	if loaded[1].ID != "env-2" || loaded[1].Token != "token-2" {
		t.Errorf("env-2: got %+v", loaded[1])
	}

	env, ok := EnvironmentAccessByID("env-1")
	if !ok {
		t.Fatal("expected env-1")
	}
	if env.Token != "token-1" {
		t.Errorf("env-1 token: got %q", env.Token)
	}
}

func TestLoadEnvironmentAccessLegacyList(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, "environments.yaml")
	t.Setenv("TELOS_ENVIRONMENTS_CONFIG", envPath)
	t.Setenv("TELOS_CONFIG", filepath.Join(dir, "config.yaml"))

	if err := os.WriteFile(envPath, []byte(`
environments:
  - id: env-legacy
    env_handle: env-legacy.usetelos.ai
    env_api_key: legacy-token
`), 0o600); err != nil {
		t.Fatal(err)
	}

	loaded := LoadEnvironmentAccess()
	if len(loaded) != 1 {
		t.Fatalf("expected 1 environment, got %d", len(loaded))
	}
	if loaded[0].ID != "env-legacy" || loaded[0].Token != "legacy-token" {
		t.Fatalf("unexpected environment: %+v", loaded[0])
	}
}

func TestLoadEnvironmentAccessList(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, "environments.yaml")
	t.Setenv("TELOS_ENVIRONMENTS_CONFIG", envPath)
	t.Setenv("TELOS_CONFIG", filepath.Join(dir, "config.yaml"))

	if err := os.WriteFile(envPath, []byte(`
environments:
  - id: env-token
    env_handle: env-token.usetelos.ai
    access_token: scoped-token
`), 0o600); err != nil {
		t.Fatal(err)
	}

	loaded := LoadEnvironmentAccess()
	if len(loaded) != 1 {
		t.Fatalf("expected 1 environment, got %d", len(loaded))
	}
	if loaded[0].ID != "env-token" || loaded[0].Token != "scoped-token" {
		t.Fatalf("unexpected environment: %+v", loaded[0])
	}
}

func TestSaveEnvironmentAccessEntry(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TELOS_ENVIRONMENTS_CONFIG", filepath.Join(dir, "environments.yaml"))
	t.Setenv("TELOS_CONFIG", filepath.Join(dir, "config.yaml"))

	if err := SaveEnvironmentAccessEntry(EnvironmentAccess{ID: "env-2", Token: "token-2"}); err != nil {
		t.Fatalf("SaveEnvironmentAccessEntry: %v", err)
	}
	if err := SaveEnvironmentAccessEntry(EnvironmentAccess{ID: "env-1", Token: "token-1"}); err != nil {
		t.Fatalf("SaveEnvironmentAccessEntry: %v", err)
	}
	if err := SaveEnvironmentAccessEntry(EnvironmentAccess{ID: "env-2", Token: "token-2b"}); err != nil {
		t.Fatalf("SaveEnvironmentAccessEntry: %v", err)
	}

	loaded := LoadEnvironmentAccess()
	if len(loaded) != 2 {
		t.Fatalf("expected 2 environments, got %d", len(loaded))
	}
	if loaded[0].ID != "env-1" || loaded[0].Token != "token-1" {
		t.Errorf("env-1: got %+v", loaded[0])
	}
	if loaded[1].ID != "env-2" || loaded[1].Token != "token-2b" {
		t.Errorf("env-2: got %+v", loaded[1])
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
		t.Error("auth token should mark hosted access configured")
	}
}

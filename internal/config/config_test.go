package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigFromFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("api_endpoint: https://test.example.com\nauth_token: secret123\ncontext: org_telos\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv(ConfigPathEnv, cfgPath)
	t.Setenv(APIEndpointEnv, "")
	t.Setenv(AuthTokenEnv, "")
	t.Setenv(ContextEnv, "")

	cfg := loadConfigForTest(t)
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
	if err := os.WriteFile(cfgPath, []byte("api_endpoint: https://file.example.com\nauth_token: file-token\ncontext: org_file\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv(ConfigPathEnv, cfgPath)
	t.Setenv(APIEndpointEnv, "https://env.example.com")
	t.Setenv(AuthTokenEnv, "env-token")
	t.Setenv(ContextEnv, "@env")

	cfg := loadConfigForTest(t)
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
	if err := os.WriteFile(cfgPath, []byte("api_endpoint: https://file.example.com\nauth_token: file-token\ncontext: org_file\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv(ConfigPathEnv, cfgPath)
	t.Setenv(APIEndpointEnv, "https://env.example.com")
	t.Setenv(AuthTokenEnv, "env-token")
	t.Setenv(ContextEnv, "@env")

	cfg := loadStoredConfigForTest(t)
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

func TestLoadConfigMissingOrEmpty(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv(ConfigPathEnv, cfgPath)
	t.Setenv(APIEndpointEnv, "")
	t.Setenv(AuthTokenEnv, "")
	t.Setenv(ContextEnv, "")

	if cfg := loadConfigForTest(t); cfg.APIEndpoint != "" {
		t.Errorf("missing file: expected empty endpoint, got %q", cfg.APIEndpoint)
	}
	if err := os.WriteFile(cfgPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if cfg := loadConfigForTest(t); cfg.APIEndpoint != "" {
		t.Errorf("empty file: expected empty endpoint, got %q", cfg.APIEndpoint)
	}
}

func TestLoadConfigRejectsInvalidFiles(t *testing.T) {
	tests := map[string]string{
		"malformed YAML":     "context: [\n",
		"unknown field":      "org_id: org_legacy\n",
		"wrong value type":   "auth_token: [secret]\n",
		"multiple documents": "context: personal\n---\ncontext: org_telos\n",
	}
	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			cfgPath := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(cfgPath, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv(ConfigPathEnv, cfgPath)

			_, err := LoadConfig()
			if err == nil {
				t.Fatal("LoadConfig succeeded")
			}
			if !strings.Contains(err.Error(), cfgPath) {
				t.Fatalf("error %q does not include config path", err)
			}
		})
	}
}

func TestLoadConfigReportsReadError(t *testing.T) {
	cfgPath := t.TempDir()
	t.Setenv(ConfigPathEnv, cfgPath)

	_, err := LoadConfig()
	if err == nil {
		t.Fatal("LoadConfig succeeded for a directory")
	}
	if !strings.Contains(err.Error(), cfgPath) || !strings.Contains(err.Error(), "read Telos config") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSaveAndLoadConfig(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "telos")
	cfgPath := filepath.Join(configDir, "config.yaml")
	t.Setenv(ConfigPathEnv, cfgPath)
	t.Setenv(APIEndpointEnv, "")
	t.Setenv(AuthTokenEnv, "")
	t.Setenv(ContextEnv, "")

	err := SaveConfig(&Config{
		APIEndpoint: "https://saved.example.com",
		AuthToken:   "saved-token",
		Context:     "org_saved",
	})
	if err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	cfg := loadConfigForTest(t)
	if cfg.APIEndpoint != "https://saved.example.com" {
		t.Errorf("endpoint: got %q", cfg.APIEndpoint)
	}
	if cfg.AuthToken != "saved-token" {
		t.Errorf("token: got %q", cfg.AuthToken)
	}
	if cfg.Context != "org_saved" {
		t.Errorf("context: got %q", cfg.Context)
	}
	assertMode(t, configDir, 0o700)
	assertMode(t, cfgPath, 0o600)
}

func TestSaveConfigTightensExistingFilePermissions(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("auth_token: old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ConfigPathEnv, cfgPath)

	if err := SaveConfig(&Config{AuthToken: "new"}); err != nil {
		t.Fatal(err)
	}
	assertMode(t, cfgPath, 0o600)
}

func TestIsConfigured(t *testing.T) {
	t.Setenv(ConfigPathEnv, filepath.Join(t.TempDir(), "nonexistent.yaml"))
	t.Setenv(APIEndpointEnv, "")
	t.Setenv(AuthTokenEnv, "")
	t.Setenv(ContextEnv, "")

	if configuredForTest(t) {
		t.Error("should not be configured with no file or env")
	}

	t.Setenv(APIEndpointEnv, "https://example.com")
	if configuredForTest(t) {
		t.Error("endpoint without token should not be configured")
	}

	t.Setenv(AuthTokenEnv, "token")
	if !configuredForTest(t) {
		t.Error("auth token should mark cloud access configured")
	}
}

func loadConfigForTest(t *testing.T) *Config {
	t.Helper()
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func loadStoredConfigForTest(t *testing.T) *Config {
	t.Helper()
	cfg, err := LoadStoredConfig()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func configuredForTest(t *testing.T) bool {
	t.Helper()
	configured, err := IsConfigured()
	if err != nil {
		t.Fatal(err)
	}
	return configured
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode for %s = %04o, want %04o", path, got, want)
	}
}

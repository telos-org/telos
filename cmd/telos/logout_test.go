package main

import (
	"path/filepath"
	"testing"

	"github.com/telos-org/telos/internal/config"
)

func TestLogoutDoesNotPersistEnvironmentOverrides(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv(config.ConfigPathEnv, configPath)
	if err := config.SaveConfig(&config.Config{
		APIEndpoint: "https://stored.example.com",
		AuthToken:   "stored-token",
		Context:     "org_stored",
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.APIEndpointEnv, "https://environment.example.com")
	t.Setenv(config.AuthTokenEnv, "environment-token")
	t.Setenv(config.ContextEnv, "@environment")

	cmdLogout(nil)

	stored := config.LoadStoredConfig()
	if stored.AuthToken != "" {
		t.Fatalf("stored token was not cleared")
	}
	if stored.APIEndpoint != "https://stored.example.com" {
		t.Fatalf("endpoint = %q", stored.APIEndpoint)
	}
	if stored.Context != "org_stored" {
		t.Fatalf("context = %q", stored.Context)
	}
}

package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/telos-org/telos/internal/config"
)

func TestLogoutRevokesSavedLoginAtItsEndpoint(t *testing.T) {
	for _, environmentToken := range []string{"", "environment-token"} {
		t.Run("TELOS_AUTH_TOKEN="+environmentToken, func(t *testing.T) {
			var revocations, overrideRequests atomic.Int32
			savedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				revocations.Add(1)
				if r.Method != http.MethodDelete || r.URL.Path != "/api/api-tokens/tok_saved" {
					t.Errorf("request = %s %s", r.Method, r.URL.Path)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer telos_pat_tok_saved.secret" {
					t.Errorf("Authorization = %q", got)
				}
			}))
			defer savedServer.Close()
			overrideServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				overrideRequests.Add(1)
			}))
			defer overrideServer.Close()

			t.Setenv(config.ConfigPathEnv, filepath.Join(t.TempDir(), "config.yaml"))
			t.Setenv(config.APIEndpointEnv, overrideServer.URL)
			t.Setenv(config.AuthTokenEnv, environmentToken)
			t.Setenv(config.ContextEnv, "@environment")
			if err := config.SaveConfig(&config.Config{
				APIEndpoint: savedServer.URL,
				AuthToken:   "telos_pat_tok_saved.secret",
				Context:     "@saved",
			}); err != nil {
				t.Fatal(err)
			}

			captureStdout(t, func() { cmdLogout(nil) })
			wantRevocations := int32(1)
			if environmentToken != "" {
				wantRevocations = 0
			}
			if revocations.Load() != wantRevocations || overrideRequests.Load() != 0 {
				t.Fatalf("saved revocations=%d, override requests=%d", revocations.Load(), overrideRequests.Load())
			}
			stored, err := config.LoadStoredConfig()
			if err != nil {
				t.Fatal(err)
			}
			if stored.AuthToken != "" || stored.APIEndpoint != savedServer.URL || stored.Context != "@saved" {
				t.Fatalf("stored config after logout = %#v", stored)
			}
		})
	}
}

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

	stored, err := config.LoadStoredConfig()
	if err != nil {
		t.Fatal(err)
	}
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

package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/config"
)

func TestCmdConfigSetsContextAndPreservesLogin(t *testing.T) {
	server := accountBootstrapServer(t)
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv(config.ConfigPathEnv, configPath)
	t.Setenv(config.APIEndpointEnv, "")
	t.Setenv(config.AuthTokenEnv, "")
	t.Setenv(config.ContextEnv, "")
	if err := config.SaveConfig(&config.Config{
		APIEndpoint: server.URL,
		AuthToken:   "test-token",
	}); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		cmdConfig([]string{"--context", "@telos"})
	})
	if !strings.Contains(out, "context set to @telos") {
		t.Fatalf("output = %q", out)
	}
	stored := config.LoadStoredConfig()
	if stored.Context != "org_telos" {
		t.Fatalf("context = %q", stored.Context)
	}
	if stored.APIEndpoint != server.URL || stored.AuthToken != "test-token" {
		t.Fatalf("login config changed: %#v", stored)
	}
}

func TestCmdConfigShowsResolvedContextWithoutToken(t *testing.T) {
	server := accountBootstrapServer(t)
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv(config.ConfigPathEnv, configPath)
	t.Setenv(config.APIEndpointEnv, "")
	t.Setenv(config.AuthTokenEnv, "")
	t.Setenv(config.ContextEnv, "")
	if err := config.SaveConfig(&config.Config{
		APIEndpoint: server.URL,
		AuthToken:   "test-token",
		Context:     "org_telos",
	}); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		cmdConfig(nil)
	})
	for _, want := range []string{server.URL, "Authenticated", "yes", "Context", "@telos"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output %q does not contain %q", out, want)
		}
	}
	if strings.Contains(out, "test-token") {
		t.Fatalf("output leaked auth token: %q", out)
	}
}

func TestCmdConfigClearsContextForPersonalDefault(t *testing.T) {
	server := accountBootstrapServer(t)
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv(config.ConfigPathEnv, configPath)
	t.Setenv(config.APIEndpointEnv, "")
	t.Setenv(config.AuthTokenEnv, "")
	t.Setenv(config.ContextEnv, "")
	if err := config.SaveConfig(&config.Config{
		APIEndpoint: server.URL,
		AuthToken:   "test-token",
		Context:     "org_telos",
	}); err != nil {
		t.Fatal(err)
	}

	captureStdout(t, func() {
		cmdConfig([]string{"--context", "personal"})
	})
	if got := config.LoadStoredConfig().Context; got != "" {
		t.Fatalf("context = %q, want personal default", got)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "context:") {
		t.Fatalf("personal config should omit context key:\n%s", data)
	}
}

func TestCmdConfigShowsUnauthenticatedLocalValues(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv(config.ConfigPathEnv, configPath)
	t.Setenv(config.APIEndpointEnv, "")
	t.Setenv(config.AuthTokenEnv, "")
	t.Setenv(config.ContextEnv, "")
	if err := config.SaveConfig(&config.Config{Context: "org_telos"}); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		cmdConfig(nil)
	})
	for _, want := range []string{"https://api.usetelos.ai", "no", "org_telos"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output %q does not contain %q", out, want)
		}
	}
}

func accountBootstrapServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/account/bootstrap" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"personal_org_id": "org_personal",
			"organizations": [
				{"id":"org_personal","handle":"grohan","display_name":"Rohan","kind":"personal","role":"owner","default_publish_scope":"grohan"},
				{"id":"org_telos","handle":"telos","display_name":"Telos","kind":"platform","role":"owner","default_publish_scope":"telos"}
			]
		}`))
	}))
}

package main

import (
	"fmt"
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
	if stored.Context != "@telos" {
		t.Fatalf("context = %q", stored.Context)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "@telos") ||
		strings.Contains(string(data), "org_telos") {
		t.Fatalf("stored config is not handle-based:\n%s", data)
	}
	if stored.APIEndpoint != server.URL || stored.AuthToken != "test-token" {
		t.Fatalf("login config changed: %#v", stored)
	}
}

func TestCmdConfigNormalizesOrganizationIDToHandle(t *testing.T) {
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
		cmdConfig([]string{"--context", "org_telos"})
	})

	if got := config.LoadStoredConfig().Context; got != "@telos" {
		t.Fatalf("context = %q", got)
	}
}

func TestCmdConfigUsesStoredLoginForContextResolution(t *testing.T) {
	storedServer := accountBootstrapServer(t)
	defer storedServer.Close()

	environmentServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"personal_org_id": "org_environment_personal",
			"organizations": [
				{"id":"org_environment","handle":"telos","display_name":"Environment Telos","kind":"platform","role":"owner"}
			]
		}`))
	}))
	defer environmentServer.Close()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv(config.ConfigPathEnv, configPath)
	if err := config.SaveConfig(&config.Config{
		APIEndpoint: storedServer.URL,
		AuthToken:   "test-token",
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.APIEndpointEnv, environmentServer.URL)
	t.Setenv(config.AuthTokenEnv, "environment-token")
	t.Setenv(config.ContextEnv, "")

	captureStdout(t, func() {
		cmdConfig([]string{"--context", "@telos"})
	})

	stored := config.LoadStoredConfig()
	if stored.Context != "@telos" {
		t.Fatalf("context = %q", stored.Context)
	}
	if stored.APIEndpoint != storedServer.URL || stored.AuthToken != "test-token" {
		t.Fatalf("login config changed: %#v", stored)
	}
}

func TestCmdConfigShowsResolvedContextWithoutExposingToken(t *testing.T) {
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
	for _, want := range []string{server.URL, "Context", "@telos"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output %q does not contain %q", out, want)
		}
	}
	if got := configOutputValue(t, out, "Authentication"); got != "valid" {
		t.Fatalf("authentication = %q", got)
	}
	if strings.Contains(out, "test-token") {
		t.Fatalf("output leaked auth token: %q", out)
	}
}

func TestCmdConfigClearsResolvedPersonalContext(t *testing.T) {
	server := accountBootstrapServer(t)
	defer server.Close()

	for _, value := range []string{"personal", "@grohan", "org_personal"} {
		t.Run(value, func(t *testing.T) {
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
				cmdConfig([]string{"--context", value})
			})
			if !strings.Contains(out, "context set to personal") {
				t.Fatalf("output = %q", out)
			}
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
		})
	}
}

func TestCmdConfigSurfacesAuthenticationFailure(t *testing.T) {
	for _, statusCode := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(statusCode), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, `{"detail":"token rejected"}`, statusCode)
			}))
			defer server.Close()

			configPath := filepath.Join(t.TempDir(), "config.yaml")
			t.Setenv(config.ConfigPathEnv, configPath)
			t.Setenv(config.APIEndpointEnv, "")
			t.Setenv(config.AuthTokenEnv, "")
			t.Setenv(config.ContextEnv, "")
			if err := config.SaveConfig(&config.Config{
				APIEndpoint: server.URL,
				AuthToken:   "rejected-token",
			}); err != nil {
				t.Fatal(err)
			}

			out := captureStdout(t, func() {
				cmdConfig(nil)
			})
			if got := configOutputValue(t, out, "Authentication"); got != "invalid" {
				t.Fatalf("authentication = %q", got)
			}
			for _, want := range []string{"token rejected", fmt.Sprintf("HTTP %d", statusCode)} {
				if !strings.Contains(out, want) {
					t.Fatalf("output %q does not contain %q", out, want)
				}
			}
		})
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
	for _, want := range []string{"https://api.usetelos.ai", "not configured", "org_telos"} {
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
				{"id":"org_personal","handle":"grohan","display_name":"Rohan","role":"owner","default_publish_scope":"grohan"},
				{"id":"org_telos","handle":"telos","display_name":"Telos","kind":"platform","role":"owner","default_publish_scope":"telos"}
			]
		}`))
	}))
}

func configOutputValue(t *testing.T, output, label string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == label {
			return strings.Join(fields[1:], " ")
		}
	}
	t.Fatalf("output %q has no %s row", output, label)
	return ""
}

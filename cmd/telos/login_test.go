package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/config"
)

func TestLoginCLIProcess(t *testing.T) {
	if os.Getenv("TELOS_TEST_LOGIN_COMMAND") != "1" {
		return
	}
	index := slices.Index(os.Args, "--")
	if index == -1 {
		os.Exit(2)
	}
	cmdLogin(os.Args[index+1:])
	os.Exit(0)
}

func TestLoginEndpointPrecedence(t *testing.T) {
	for _, source := range []string{"saved", "environment", "flag"} {
		t.Run(source, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if source == "saved" {
					if r.Method != http.MethodGet || r.URL.Path != "/api/me" {
						t.Errorf("request = %s %s", r.Method, r.URL.Path)
					}
					if got := r.Header.Get("Authorization"); got != "Bearer saved-token" {
						t.Errorf("Authorization = %q", got)
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"subject":"user-1"}`))
					return
				}
				if r.Method != http.MethodPost || r.URL.Path != "/api/cli/auth/start" {
					t.Errorf("request = %s %s", r.Method, r.URL.Path)
				}
				if got := r.Header.Get("Authorization"); got != "" {
					t.Errorf("token sent to a different endpoint: %q", got)
				}
				http.Error(w, "selected login endpoint", http.StatusBadRequest)
			}))
			defer server.Close()

			configPath := filepath.Join(t.TempDir(), "config.yaml")
			t.Setenv(config.ConfigPathEnv, configPath)
			t.Setenv(config.APIEndpointEnv, "")
			t.Setenv(config.AuthTokenEnv, "environment-token")
			t.Setenv(config.ContextEnv, "@environment")
			stored := config.Config{APIEndpoint: server.URL, AuthToken: "saved-token", Context: "@saved"}
			args := []string{"-test.run=^TestLoginCLIProcess$", "--"}
			if source != "saved" {
				stored.APIEndpoint = "http://127.0.0.1:1"
				t.Setenv(config.APIEndpointEnv, server.URL+"/")
			}
			if source == "flag" {
				t.Setenv(config.APIEndpointEnv, "http://127.0.0.1:1")
				args = append(args, "--endpoint", server.URL+"/")
			}
			if err := config.SaveConfig(&stored); err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], args...)
			command.Env = append(os.Environ(), "TELOS_TEST_LOGIN_COMMAND=1")
			out, err := command.CombinedOutput()
			if source == "saved" {
				if err != nil || !strings.Contains(string(out), "already logged in to "+server.URL) {
					t.Fatalf("command err=%v output=%s", err, out)
				}
			} else if err == nil || !strings.Contains(string(out), "selected login endpoint") {
				t.Fatalf("command err=%v output=%s", err, out)
			}
			got, err := config.LoadStoredConfig()
			if err != nil {
				t.Fatal(err)
			}
			if *got != stored {
				t.Fatalf("stored config = %#v, want %#v", got, stored)
			}
		})
	}
}

func TestConfiguredLoginUsesConfiguredToken(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodGet || r.URL.Path != "/api/me" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer existing-token" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"subject":"user-1","email":"person@example.com"}`))
	}))
	defer server.Close()

	who, loggedIn, err := configuredLogin(server.URL, &config.Config{
		APIEndpoint: server.URL,
		AuthToken:   "existing-token",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !loggedIn || who != "person@example.com" {
		t.Fatalf("loggedIn = %v, who = %q", loggedIn, who)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

func TestConfiguredLoginContinuesWhenTokenIsRejected(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "invalid token", status)
			}))
			defer server.Close()

			who, loggedIn, err := configuredLogin(server.URL, &config.Config{
				APIEndpoint: server.URL,
				AuthToken:   "rejected-token",
			})
			if err != nil {
				t.Fatal(err)
			}
			if loggedIn || who != "" {
				t.Fatalf("loggedIn = %v, who = %q", loggedIn, who)
			}
		})
	}
}

func TestConfiguredLoginSkipsDifferentEndpoint(t *testing.T) {
	who, loggedIn, err := configuredLogin("https://other.example.com", &config.Config{
		APIEndpoint: "https://api.usetelos.ai",
		AuthToken:   "existing-token",
	})
	if err != nil {
		t.Fatal(err)
	}
	if loggedIn || who != "" {
		t.Fatalf("loggedIn = %v, who = %q", loggedIn, who)
	}
}

func TestConfiguredLoginSurfacesVerificationFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	_, loggedIn, err := configuredLogin(server.URL, &config.Config{
		APIEndpoint: server.URL,
		AuthToken:   "existing-token",
	})
	if err == nil {
		t.Fatal("expected verification error")
	}
	if loggedIn {
		t.Fatal("unexpected logged-in result")
	}
}

func TestBrowserLoginDoesNotPromptForToken(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	_, err := browserLogin(server.URL)
	if !cloud.IsStatus(err, http.StatusNotFound) {
		t.Fatalf("error = %v, want HTTP 404", err)
	}
}

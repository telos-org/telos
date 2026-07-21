package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/telos-org/telos/internal/config"
)

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

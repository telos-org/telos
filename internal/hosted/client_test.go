package hosted

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/telos-org/telos-go/internal/sessionapi"
)

func TestNormalizeEndpoint(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"https://api.example.com", "https://api.example.com"},
		{"https://api.example.com/", "https://api.example.com"},
		{"http://localhost:8080/", "http://localhost:8080"},
		{"api.example.com", "https://api.example.com"},
	}
	for _, tt := range tests {
		got := NormalizeEndpoint(tt.input)
		if got != tt.expected {
			t.Errorf("NormalizeEndpoint(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestClientGetSession(t *testing.T) {
	// Mock server
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sessions/test-session" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sessionapi.Session{
			SessionID: "test-session",
			Status:    sessionapi.StatusCompleted,
			Runtime:   sessionapi.RuntimeHosted,
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-token")
	session, err := client.GetSession("test-session")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if session.SessionID != "test-session" {
		t.Errorf("session_id: got %q", session.SessionID)
	}
	if session.Status != sessionapi.StatusCompleted {
		t.Errorf("status: got %q", session.Status)
	}
	if session.Runtime != sessionapi.RuntimeHosted {
		t.Errorf("runtime: got %q", session.Runtime)
	}
}

func TestClientListSessions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sessionapi.SessionListResponse{
			Sessions: []sessionapi.Session{
				{SessionID: "s1", Status: sessionapi.StatusCompleted, Runtime: sessionapi.RuntimeHosted},
				{SessionID: "s2", Status: sessionapi.StatusRunning, Runtime: sessionapi.RuntimeHosted},
			},
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "token")
	sessions, err := client.ListSessions(0)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Errorf("expected 2 sessions, got %d", len(sessions))
	}
}

func TestClientListEnvironments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/environments" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"environments": []map[string]any{{
				"id":                         "env_123",
				"env_handle":                 "env-abc.usetelos.ai",
				"state":                      "ready",
				"has_recoverable_env_access": true,
			}},
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "token")
	envs, err := client.ListEnvironments()
	if err != nil {
		t.Fatalf("ListEnvironments: %v", err)
	}
	if len(envs) != 1 {
		t.Fatalf("expected 1 environment, got %d", len(envs))
	}
	if envs[0].ID != "env_123" || envs[0].Handle != "env-abc.usetelos.ai" {
		t.Fatalf("unexpected environment: %+v", envs[0])
	}
	if !envs[0].HasRecoverable {
		t.Fatal("expected recoverable environment")
	}
}

func TestClientCreateEnvironment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/environments" || r.Method != "POST" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":          "env_123",
			"env_handle":  "env-abc.usetelos.ai",
			"env_api_key": "env-key",
			"state":       "provisioning",
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "token")
	env, err := client.CreateEnvironment()
	if err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}
	if env.ID != "env_123" || env.Handle != "env-abc.usetelos.ai" || env.EnvAPIKey != "env-key" {
		t.Fatalf("unexpected environment: %+v", env)
	}
}

func TestClientStopSession(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sessionapi.Session{
			SessionID: "stop-me",
			Status:    sessionapi.StatusStopped,
			Runtime:   sessionapi.RuntimeHosted,
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "token")
	session, err := client.StopSession("stop-me")
	if err != nil {
		t.Fatalf("StopSession: %v", err)
	}
	if session.Status != sessionapi.StatusStopped {
		t.Errorf("status: got %q", session.Status)
	}
}

func TestClientGetTranscript(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("# PVG Transcript\n\nSome content"))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "token")
	text, err := client.GetTranscript("test-session")
	if err != nil {
		t.Fatalf("GetTranscript: %v", err)
	}
	if text != "# PVG Transcript\n\nSome content" {
		t.Errorf("transcript: got %q", text)
	}
}

func TestClientNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(404)
		json.NewEncoder(w).Encode(map[string]string{"detail": "not found"})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "token")
	_, err := client.GetSession("missing")
	if err == nil {
		t.Fatal("expected error")
	}
	if !contains(err.Error(), "not found") {
		t.Errorf("error should contain 'not found': got %q", err.Error())
	}
}

func TestSharedAPIModel(t *testing.T) {
	// Verify that local and hosted share the same Session type
	// by round-tripping through JSON
	local := sessionapi.Session{
		SessionID: "local_test",
		Status:    sessionapi.StatusCompleted,
		Runtime:   sessionapi.RuntimeLocal,
	}
	hosted := sessionapi.Session{
		SessionID: "hosted_test",
		Status:    sessionapi.StatusRunning,
		Runtime:   sessionapi.RuntimeHosted,
	}

	for _, s := range []*sessionapi.Session{&local, &hosted} {
		data, _ := json.Marshal(s)
		var decoded sessionapi.Session
		json.Unmarshal(data, &decoded)
		if decoded.SessionID != s.SessionID {
			t.Errorf("round-trip failed for %s", s.SessionID)
		}
		if decoded.Runtime != s.Runtime {
			t.Errorf("runtime round-trip: got %q", decoded.Runtime)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsImpl(s, substr))
}

func containsImpl(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

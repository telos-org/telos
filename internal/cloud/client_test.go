package cloud

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/sessionapi"
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

func TestClientPublishPackageVersion(t *testing.T) {
	var uploadedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/api/packages/telos/auth/versions/1.2.3":
			uploadedBody, _ = io.ReadAll(r.Body)
			json.NewEncoder(w).Encode(map[string]any{
				"scope":      "telos",
				"name":       "auth",
				"version":    "1.2.3",
				"ref":        "@telos/auth:1.2.3",
				"digest":     "sha256:abc",
				"created_at": "now",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-token")
	uploaded, err := client.PublishPackageVersion("telos", "auth", "1.2.3", []byte("package"))
	if err != nil {
		t.Fatalf("PublishPackageVersion: %v", err)
	}
	if uploaded.Ref != "@telos/auth:1.2.3" || uploaded.Digest != "sha256:abc" || string(uploadedBody) != "package" {
		t.Fatalf("upload: got %+v body %q", uploaded, uploadedBody)
	}
}

func TestClientCreateSession(t *testing.T) {
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/deployments" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("User-Agent") != UserAgent {
			t.Fatalf("user-agent: got %q", r.Header.Get("User-Agent"))
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"id":             "sess_123",
			"name":           "auth",
			"state":          "provisioning",
			"package_ref":    "@telos/auth:1.2.3",
			"package_digest": "sha256:abc",
			"created_at":     "now",
			"updated_at":     "now",
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-token")
	session, err := client.CreateSession("auth", "@telos/auth:1.2.3")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if session.ID != "sess_123" || session.Name != "auth" || session.State != "provisioning" {
		t.Fatalf("session: got %+v", session)
	}
	if gotBody["name"] != "auth" || gotBody["package_ref"] != "@telos/auth:1.2.3" {
		t.Fatalf("body: got %#v", gotBody)
	}
}

func TestClientUpdateSession(t *testing.T) {
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/deployments/sess_123" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"id":             "sess_123",
			"name":           "auth",
			"state":          "deploying",
			"package_ref":    "@telos/auth:1.2.4",
			"package_digest": "sha256:def",
			"created_at":     "then",
			"updated_at":     "now",
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-token")
	session, err := client.UpdateSession("sess_123", "@telos/auth:1.2.4")
	if err != nil {
		t.Fatalf("UpdateSession: %v", err)
	}
	if session.ID != "sess_123" || session.PackageDigest != "sha256:def" || session.State != "deploying" {
		t.Fatalf("session: got %+v", session)
	}
	if gotBody["package_ref"] != "@telos/auth:1.2.4" {
		t.Fatalf("body: got %#v", gotBody)
	}
}

func TestClientListSessions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/deployments" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"deployments": []map[string]any{{
				"id":             "sess_123",
				"name":           "auth",
				"state":          "healthy",
				"package_ref":    "@telos/auth:1.2.3",
				"package_digest": "sha256:abc",
				"created_at":     "then",
				"updated_at":     "now",
			}},
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-token")
	sessions, err := client.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != "sess_123" || sessions[0].Name != "auth" {
		t.Fatalf("sessions: got %+v", sessions)
	}
}

func TestClientGetSession(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/deployments/sess_123" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"id":             "sess_123",
			"name":           "auth",
			"state":          "healthy",
			"package_ref":    "@telos/auth:1.2.3",
			"package_digest": "sha256:abc",
			"created_at":     "then",
			"updated_at":     "now",
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-token")
	session, err := client.GetSession("sess_123")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if session.ID != "sess_123" || session.PackageRef != "@telos/auth:1.2.3" {
		t.Fatalf("session: got %+v", session)
	}
}

func TestClientDeleteSession(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/deployments/sess_123" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"id":             "sess_123",
			"name":           "auth",
			"state":          "deleted",
			"package_ref":    "@telos/auth:1.2.3",
			"package_digest": "sha256:abc",
			"created_at":     "then",
			"updated_at":     "now",
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-token")
	session, err := client.DeleteSession("sess_123")
	if err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if session.ID != "sess_123" || session.State != "deleted" {
		t.Fatalf("session: got %+v", session)
	}
}

func TestClientOpenSession(t *testing.T) {
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/deployments/sess_123/open" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("User-Agent") != UserAgent {
			t.Fatalf("user-agent: got %q", r.Header.Get("User-Agent"))
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"url":        "https://auth.example.com/admin",
			"expires_at": "2026-07-04T08:00:00Z",
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-token")
	openURL, err := client.OpenSession("sess_123", "dashboard", "/admin")
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if openURL.URL != "https://auth.example.com/admin" || openURL.ExpiresAt == "" {
		t.Fatalf("open URL: got %+v", openURL)
	}
	if gotBody["target"] != "dashboard" || gotBody["path"] != "/admin" {
		t.Fatalf("body: got %#v", gotBody)
	}
}

func TestClientGetSessionLogs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/deployments/sess_123/logs" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(sessionapi.SessionEventsResponse{
			Events: []sessionapi.SessionEvent{
				{Event: "agent_progress", Data: map[string]any{"kind": "progress_update", "text": "ready"}},
			},
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-token")
	events, err := client.GetSessionLogs("sess_123")
	if err != nil {
		t.Fatalf("GetSessionLogs: %v", err)
	}
	if len(events) != 1 || events[0].Event != "agent_progress" || events[0].Data["text"] != "ready" {
		t.Fatalf("events: got %#v", events)
	}
}

func TestClientStreamSessionLogs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/deployments/sess_123/logs" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Fatalf("Accept: got %q", r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"event\":\"game_end\",\"data\":{\"game_result\":\"completed\"}}\n\n"))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-token")
	var events []sessionapi.SessionEvent
	err := client.StreamSessionLogs(context.Background(), "sess_123", func(event sessionapi.SessionEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("StreamSessionLogs: %v", err)
	}
	if len(events) != 1 || events[0].Event != "game_end" || events[0].Data["game_result"] != "completed" {
		t.Fatalf("events: got %#v", events)
	}
}

func TestSessionCreateRequestOmitsEmptyRuntimeDefaults(t *testing.T) {
	markdown := "---\nversion: v0\nname: demo\n---\n# Demo\n"
	body, err := json.Marshal(sessionapi.SessionCreateRequest{SpecMarkdown: &markdown})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "model") ||
		strings.Contains(string(body), "thinking") ||
		strings.Contains(string(body), "session_kind") {
		t.Fatalf("empty defaults should be omitted: %s", body)
	}
}

func TestReadErrorPreservesStructuredDetail(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Body:       io.NopCloser(strings.NewReader(`{"detail":{"error":"bad spec"}}`)),
	}
	err := readError(resp)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "bad spec") || !strings.Contains(err.Error(), "400") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSharedAPIModel(t *testing.T) {
	// Verify that local and cloud share the same Session type
	// by round-tripping through JSON
	local := sessionapi.Session{
		SessionID: "local_test",
		Status:    sessionapi.StatusCompleted,
		Runtime:   sessionapi.RuntimeLocal,
	}
	cloud := sessionapi.Session{
		SessionID: "cloud_test",
		Status:    sessionapi.StatusRunning,
		Runtime:   sessionapi.RuntimeCloud,
	}

	for _, s := range []*sessionapi.Session{&local, &cloud} {
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

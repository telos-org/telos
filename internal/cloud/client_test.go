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

func TestClientCreateDeployment(t *testing.T) {
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
	deployment, err := client.CreateDeployment("auth", "@telos/auth:1.2.3")
	if err != nil {
		t.Fatalf("CreateDeployment: %v", err)
	}
	if deployment.ID != "sess_123" || deployment.Name != "auth" || deployment.State != "provisioning" {
		t.Fatalf("deployment: got %+v", deployment)
	}
	if gotBody["name"] != "auth" || gotBody["package_ref"] != "@telos/auth:1.2.3" {
		t.Fatalf("body: got %#v", gotBody)
	}
}

func TestClientUpdateDeployment(t *testing.T) {
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
	deployment, err := client.UpdateDeployment("sess_123", "@telos/auth:1.2.4")
	if err != nil {
		t.Fatalf("UpdateDeployment: %v", err)
	}
	if deployment.ID != "sess_123" || deployment.PackageDigest != "sha256:def" || deployment.State != "deploying" {
		t.Fatalf("deployment: got %+v", deployment)
	}
	if gotBody["package_ref"] != "@telos/auth:1.2.4" {
		t.Fatalf("body: got %#v", gotBody)
	}
}

func TestClientListDeployments(t *testing.T) {
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
	deployments, err := client.ListDeployments()
	if err != nil {
		t.Fatalf("ListDeployments: %v", err)
	}
	if len(deployments) != 1 || deployments[0].ID != "sess_123" || deployments[0].Name != "auth" {
		t.Fatalf("deployments: got %+v", deployments)
	}
}

func TestClientGetDeployment(t *testing.T) {
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
	deployment, err := client.GetDeployment("sess_123")
	if err != nil {
		t.Fatalf("GetDeployment: %v", err)
	}
	if deployment.ID != "sess_123" || deployment.PackageRef != "@telos/auth:1.2.3" {
		t.Fatalf("deployment: got %+v", deployment)
	}
}

func TestClientDeleteDeployment(t *testing.T) {
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
	deployment, err := client.DeleteDeployment("sess_123")
	if err != nil {
		t.Fatalf("DeleteDeployment: %v", err)
	}
	if deployment.ID != "sess_123" || deployment.State != "deleted" {
		t.Fatalf("deployment: got %+v", deployment)
	}
}

func TestClientGetDeploymentLogs(t *testing.T) {
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
	events, err := client.GetDeploymentLogs("sess_123")
	if err != nil {
		t.Fatalf("GetDeploymentLogs: %v", err)
	}
	if len(events) != 1 || events[0].Event != "agent_progress" || events[0].Data["text"] != "ready" {
		t.Fatalf("events: got %#v", events)
	}
}

func TestClientStreamDeploymentLogs(t *testing.T) {
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
	err := client.StreamDeploymentLogs(context.Background(), "sess_123", func(event sessionapi.SessionEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("StreamDeploymentLogs: %v", err)
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

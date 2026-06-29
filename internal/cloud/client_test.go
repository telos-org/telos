package cloud

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
			Runtime:   sessionapi.RuntimeCloud,
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
	if session.Runtime != sessionapi.RuntimeCloud {
		t.Errorf("runtime: got %q", session.Runtime)
	}
}

func TestClientListSessions(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sessionapi.SessionListResponse{
			Sessions: []sessionapi.SessionListItem{
				{SessionID: "s1", Status: sessionapi.StatusCompleted, Runtime: sessionapi.RuntimeCloud},
				{SessionID: "s2", Status: sessionapi.StatusRunning, Runtime: sessionapi.RuntimeCloud},
			},
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "token")
	sessions, err := client.ListSessions(0, false)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Errorf("expected 2 sessions, got %d", len(sessions))
	}
	if gotPath != "/api/sessions" {
		t.Fatalf("request path: got %q", gotPath)
	}
	sessions, err = client.ListSessions(2, false)
	if err != nil {
		t.Fatalf("ListSessions limit: %v", err)
	}
	if gotPath != "/api/sessions?limit=2" {
		t.Fatalf("limited request path: got %q", gotPath)
	}
	_, err = client.ListSessions(2, true)
	if err != nil {
		t.Fatalf("ListSessions include children: %v", err)
	}
	if gotPath != "/api/sessions?limit=2&include_children=true" {
		t.Fatalf("include children request path: got %q", gotPath)
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
			"id":           "env_123",
			"env_handle":   "env-abc.usetelos.ai",
			"access_token": "env-token",
			"state":        "provisioning",
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "token")
	env, err := client.CreateEnvironment()
	if err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}
	if env.ID != "env_123" || env.Handle != "env-abc.usetelos.ai" || env.AccessToken != "env-token" {
		t.Fatalf("unexpected environment: %+v", env)
	}
}

func TestClientCreateEnvironmentAcceptsLegacyAccessField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/environments" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":          "env_123",
			"env_handle":  "env-abc.usetelos.ai",
			"env_api_key": "legacy-token",
			"state":       "provisioning",
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "token")
	env, err := client.CreateEnvironment()
	if err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}
	if env.AccessToken != "legacy-token" {
		t.Fatalf("access token: got %q", env.AccessToken)
	}
}

func TestClientUploadApplyPackage(t *testing.T) {
	var uploadedBody []byte
	var metadataBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/api/packages/sha256:abc":
			uploadedBody, _ = io.ReadAll(r.Body)
			json.NewEncoder(w).Encode(map[string]any{
				"digest":     "sha256:abc",
				"size_bytes": len(uploadedBody),
				"created_at": "now",
				"visibility": "private",
			})
		case r.Method == http.MethodPatch && r.URL.Path == "/api/packages/sha256:abc":
			if err := json.NewDecoder(r.Body).Decode(&metadataBody); err != nil {
				t.Fatal(err)
			}
			json.NewEncoder(w).Encode(map[string]any{
				"digest":     "sha256:abc",
				"size_bytes": len(uploadedBody),
				"created_at": "now",
				"name":       "auth",
				"visibility": "private",
				"updated_at": "now",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-token")
	uploaded, err := client.UploadApplyPackage("sha256:abc", []byte("package"))
	if err != nil {
		t.Fatalf("UploadApplyPackage: %v", err)
	}
	if uploaded.SizeBytes != len("package") || string(uploadedBody) != "package" {
		t.Fatalf("upload: got %+v body %q", uploaded, uploadedBody)
	}
	patched, err := client.UpdateApplyPackageMetadata("sha256:abc", ApplyPackageMetadata{Name: "auth"})
	if err != nil {
		t.Fatalf("UpdateApplyPackageMetadata: %v", err)
	}
	if patched.Name == nil || *patched.Name != "auth" {
		t.Fatalf("metadata: got %+v", patched)
	}
	if metadataBody["name"] != "auth" || metadataBody["visibility"] != "private" {
		t.Fatalf("metadata body: got %#v", metadataBody)
	}
}

func TestClientApplyEnvironmentSession(t *testing.T) {
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/environments/env_123/sessions/auth" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"operation": "created",
			"session": map[string]any{
				"env_id":         "env_123",
				"name":           "auth",
				"package_digest": "sha256:abc",
				"desired_state":  "running",
				"created_at":     "now",
				"updated_at":     "now",
			},
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-token")
	response, err := client.ApplyEnvironmentSession("env_123", "auth", "sha256:abc")
	if err != nil {
		t.Fatalf("ApplyEnvironmentSession: %v", err)
	}
	if response.Operation != "created" || response.Session.Name != "auth" {
		t.Fatalf("response: got %+v", response)
	}
	if gotBody["package_digest"] != "sha256:abc" {
		t.Fatalf("body: got %#v", gotBody)
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
			"id":             "dep_123",
			"name":           "auth",
			"state":          "provisioning",
			"package_digest": "sha256:abc",
			"created_at":     "now",
			"updated_at":     "now",
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-token")
	deployment, err := client.CreateDeployment("auth", "sha256:abc")
	if err != nil {
		t.Fatalf("CreateDeployment: %v", err)
	}
	if deployment.ID != "dep_123" || deployment.Name != "auth" || deployment.State != "provisioning" {
		t.Fatalf("deployment: got %+v", deployment)
	}
	if gotBody["name"] != "auth" || gotBody["package_digest"] != "sha256:abc" {
		t.Fatalf("body: got %#v", gotBody)
	}
}

func TestClientUpdateDeployment(t *testing.T) {
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/deployments/dep_123" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"id":             "dep_123",
			"name":           "auth",
			"state":          "deploying",
			"package_digest": "sha256:def",
			"created_at":     "then",
			"updated_at":     "now",
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-token")
	deployment, err := client.UpdateDeployment("dep_123", "sha256:def")
	if err != nil {
		t.Fatalf("UpdateDeployment: %v", err)
	}
	if deployment.ID != "dep_123" || deployment.PackageDigest != "sha256:def" || deployment.State != "deploying" {
		t.Fatalf("deployment: got %+v", deployment)
	}
	if gotBody["package_digest"] != "sha256:def" {
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
				"id":             "dep_123",
				"name":           "auth",
				"state":          "healthy",
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
	if len(deployments) != 1 || deployments[0].ID != "dep_123" || deployments[0].Name != "auth" {
		t.Fatalf("deployments: got %+v", deployments)
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

func TestClientApplySessionSpec(t *testing.T) {
	var gotMethod string
	var gotPath string
	var gotBody sessionapi.SessionSpecUpdateRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		json.NewEncoder(w).Encode(sessionapi.SessionSpecUpdateResponse{
			Operation: "updated",
			Session: &sessionapi.Session{
				SessionID: "sess_controller",
				Status:    sessionapi.StatusRunning,
				Runtime:   sessionapi.RuntimeCloud,
			},
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-token")
	response, err := client.ApplySessionSpec("demo", sessionapi.SessionSpecUpdateRequest{
		SpecMarkdown: "---\nversion: v0\nname: demo\n---\n# Demo\n",
	})
	if err != nil {
		t.Fatalf("ApplySessionSpec: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Fatalf("method: got %q", gotMethod)
	}
	if gotPath != "/api/sessions/demo/spec" {
		t.Fatalf("path: got %q", gotPath)
	}
	if !strings.Contains(gotBody.SpecMarkdown, "name: demo") {
		t.Fatalf("body: got %#v", gotBody)
	}
	if response.Operation != "updated" {
		t.Fatalf("operation: got %q", response.Operation)
	}
	if response.Session == nil {
		t.Fatal("missing session")
	}
	if response.Session.SessionID != "sess_controller" {
		t.Fatalf("session: got %#v", response.Session)
	}
}

func TestWaitForEnvironmentRequiresSuccessStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/healthz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	err := waitForEnvironment(srv.URL, 10*time.Millisecond, srv.Client(), time.Millisecond)
	if err == nil {
		t.Fatal("expected readiness error")
	}
	if !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWaitForEnvironmentSucceedsOnSuccessStatus(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if r.URL.Path != "/api/healthz" {
			http.NotFound(w, r)
			return
		}
		if attempts == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := waitForEnvironment(srv.URL, time.Second, srv.Client(), time.Millisecond); err != nil {
		t.Fatalf("WaitForEnvironment: %v", err)
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
			Runtime:   sessionapi.RuntimeCloud,
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
		w.Write([]byte("# Session Transcript\n\nSome content"))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "token")
	text, err := client.GetTranscript("test-session")
	if err != nil {
		t.Fatalf("GetTranscript: %v", err)
	}
	if text != "# Session Transcript\n\nSome content" {
		t.Errorf("transcript: got %q", text)
	}
}

func TestClientStreamEvents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sessions/sess_123/events" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Fatalf("accept header: got %q", r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message\n"))
		_, _ = w.Write([]byte("data: {\"event\":\"game_start\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"event\":\"game_end\",\"data\":{\"result\":\"completed\"}}\n\n"))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "token")
	var events []map[string]any
	err := client.StreamEvents(context.Background(), "sess_123", func(event map[string]any) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("StreamEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events: got %d", len(events))
	}
	if events[0]["event"] != "game_start" || events[1]["event"] != "game_end" {
		t.Fatalf("unexpected events: %#v", events)
	}
}

func TestClientStreamEventsRejectsMalformedData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: not-json\n\n"))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "token")
	err := client.StreamEvents(context.Background(), "sess_bad", func(event map[string]any) error {
		t.Fatalf("unexpected event: %#v", event)
		return nil
	})
	if err == nil {
		t.Fatal("expected malformed stream data to fail")
	}
	if !strings.Contains(err.Error(), "decode event stream payload") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClientStreamEventsHandlesLargeDataLine(t *testing.T) {
	large := strings.Repeat("x", 2<<20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"event":"large","data":{"payload":"` + large + `"}}` + "\n\n"))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "token")
	var got string
	err := client.StreamEvents(context.Background(), "sess_large", func(event map[string]any) error {
		data := event["data"].(map[string]any)
		got = data["payload"].(string)
		return nil
	})
	if err != nil {
		t.Fatalf("StreamEvents: %v", err)
	}
	if len(got) != len(large) {
		t.Fatalf("payload length: got %d, want %d", len(got), len(large))
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
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should contain 'not found': got %q", err.Error())
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

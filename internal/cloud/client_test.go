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

func TestClientGetEnvironmentDoesNotRequireAccess(t *testing.T) {
	var accessRequests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/environments":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"environments": []map[string]any{{
					"id":                         "env_123",
					"env_handle":                 "env-abc.usetelos.ai",
					"state":                      "ready",
					"has_recoverable_env_access": false,
				}},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/environments/env_123/access":
			accessRequests++
			http.Error(w, "access should not be requested", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "token")
	env, err := client.GetEnvironment("env_123")
	if err != nil {
		t.Fatalf("GetEnvironment: %v", err)
	}
	if env.ID != "env_123" || env.Handle != "env-abc.usetelos.ai" {
		t.Fatalf("unexpected environment: %+v", env)
	}
	if env.AccessToken != "" {
		t.Fatalf("metadata-only lookup should not set access token: %+v", env)
	}
	if accessRequests != 0 {
		t.Fatalf("access requests: got %d", accessRequests)
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

func TestClientMintSessionKey(t *testing.T) {
	var gotPath string
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"session_id": "sess-1",
			"base_url":   "https://proxy.example.com/v1",
			"api_key":    "sk-session",
			"transport":  "bifrost_async",
			"kind":       "bifrost",
			"headers":    map[string]string{"x-bf-vk": "sk-bf"},
			"budget_usd": 5.0,
			"key_alias":  "sess-1",
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-token")
	key, err := client.MintSessionKey("sess-1")
	if err != nil {
		t.Fatalf("MintSessionKey: %v", err)
	}
	if gotPath != "/api/billing/session-key" || gotBody["session_id"] != "sess-1" {
		t.Fatalf("request: path=%q body=%v", gotPath, gotBody)
	}
	if key.APIKey != "sk-session" || key.BaseURL != "https://proxy.example.com/v1" || key.Transport != "bifrost_async" || key.Kind != "bifrost" || key.Headers["x-bf-vk"] != "sk-bf" || key.KeyAlias != "sess-1" {
		t.Fatalf("key: %+v", key)
	}
}

func TestClientMintSessionKeyRejectsInvalidSessionID(t *testing.T) {
	tests := []struct {
		name      string
		sessionID string
	}{
		{name: "missing"},
		{name: "mismatch", sessionID: "sess-other"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{
					"session_id": tt.sessionID,
					"base_url":   "https://proxy.example.com/v1",
					"api_key":    "sk-session",
				})
			}))
			defer srv.Close()

			client := NewClient(srv.URL, "test-token")
			if _, err := client.MintSessionKey("sess-1"); err == nil {
				t.Fatal("expected invalid session_id error")
			}
		})
	}
}

func TestClientForwardsUserAuthorization(t *testing.T) {
	markdown := "---\nversion: v0\nname: demo\n---\n# Demo\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sessions" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer env-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get(ForwardedUserAuthorizationHeader) != "Bearer user-token" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sessionapi.Session{
			SessionID: "sess-forwarded",
			Status:    sessionapi.StatusRunning,
			Runtime:   sessionapi.RuntimeCloud,
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "env-token")
	client.ForwardedUserToken = "user-token"
	session, err := client.CreateSession(sessionapi.SessionCreateRequest{SpecMarkdown: &markdown})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if session.SessionID != "sess-forwarded" {
		t.Fatalf("session: %+v", session)
	}
}

func TestClientBalanceAndReconcile(t *testing.T) {
	var sawReconcile bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.RequestURI() {
		case "/api/billing/balance":
			json.NewEncoder(w).Encode(map[string]any{"compute_units": 123.0})
		case "/api/billing/session-key/sess-1/reconcile?terminal=true":
			sawReconcile = true
			json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-token")
	bal, err := client.Balance()
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if bal.ComputeUnits != 123.0 {
		t.Fatalf("balance: %+v", bal)
	}
	if err := client.ReconcileSession("sess-1", true); err != nil {
		t.Fatalf("ReconcileSession: %v", err)
	}
	if !sawReconcile {
		t.Fatal("missing reconcile request")
	}
}

func TestClientPushCatalogSpec(t *testing.T) {
	var uploadedBody []byte
	var pushedBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/api/catalog/packages/sha256:abc":
			uploadedBody, _ = io.ReadAll(r.Body)
			json.NewEncoder(w).Encode(map[string]any{
				"digest":     "sha256:abc",
				"size_bytes": len(uploadedBody),
				"created_at": "now",
			})
		case r.Method == http.MethodPut && r.URL.Path == "/api/catalog/specs/auth":
			if err := json.NewDecoder(r.Body).Decode(&pushedBody); err != nil {
				t.Fatal(err)
			}
			json.NewEncoder(w).Encode(map[string]any{
				"operation": "created",
				"spec": map[string]any{
					"name":           "auth",
					"package_digest": "sha256:abc",
					"visibility":     "private",
					"created_at":     "now",
					"updated_at":     "now",
				},
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
	pushed, err := client.PushCatalogSpec("auth", "sha256:abc")
	if err != nil {
		t.Fatalf("PushCatalogSpec: %v", err)
	}
	if pushed.Operation != "created" || pushed.Spec.Name != "auth" {
		t.Fatalf("push: got %+v", pushed)
	}
	if pushedBody["package_digest"] != "sha256:abc" {
		t.Fatalf("push body: got %#v", pushedBody)
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
	maxCost := 9.0
	maxToolLoops := 55
	agentTimeout := 600
	response, err := client.ApplySessionSpec("demo", sessionapi.SessionSpecUpdateRequest{
		SpecMarkdown:    "---\nversion: v0\nname: demo\n---\n# Demo\n",
		Model:           "sail-research/moonshotai/Kimi-K2.6",
		Thinking:        "high",
		MaxCostUSD:      &maxCost,
		MaxToolLoops:    &maxToolLoops,
		AgentTimeoutSec: &agentTimeout,
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
	if gotBody.MaxToolLoops == nil || *gotBody.MaxToolLoops != maxToolLoops {
		t.Fatalf("max tool loops not sent: %#v", gotBody.MaxToolLoops)
	}
	if gotBody.AgentTimeoutSec == nil || *gotBody.AgentTimeoutSec != agentTimeout {
		t.Fatalf("agent timeout not sent: %#v", gotBody.AgentTimeoutSec)
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

func TestClientGetDiagnostics(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sessions/sess_123/diagnostics" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sessionapi.SessionDiagnosticsResponse{
			SessionID: "sess_123",
			Status:    sessionapi.StatusFailed,
			Runtime:   sessionapi.RuntimeCloud,
			Failures:  map[string]int{"provider": 1},
			Retries: []sessionapi.SessionRetryDiagnostics{{
				SpecName:  "demo",
				TurnID:    "0001-prover",
				ErrorCode: "provider_rate_limited",
			}},
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "token")
	diagnostics, err := client.GetDiagnostics("sess_123")
	if err != nil {
		t.Fatalf("GetDiagnostics: %v", err)
	}
	if diagnostics.SessionID != "sess_123" || diagnostics.Failures["provider"] != 1 {
		t.Fatalf("diagnostics: %#v", diagnostics)
	}
	if len(diagnostics.Retries) != 1 || diagnostics.Retries[0].TurnID != "0001-prover" {
		t.Fatalf("retries: %#v", diagnostics.Retries)
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

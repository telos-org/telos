package cloud

import (
	"context"
	"encoding/base64"
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

func TestClientPublishPackage(t *testing.T) {
	var gotBody struct {
		Scope      string `json:"scope"`
		Name       string `json:"name"`
		Version    string `json:"version"`
		DataBase64 string `json:"data_base64"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/api/packages" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"scope":      "user-abc",
			"name":       "auth",
			"version":    "0.1.0",
			"ref":        "@user-abc/auth:0.1.0",
			"digest":     "sha256:abc",
			"created_at": "now",
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-token")
	uploaded, err := client.PublishPackage("", "auth", "", []byte("package"))
	if err != nil {
		t.Fatalf("PublishPackage: %v", err)
	}
	if uploaded.Ref != "@user-abc/auth:0.1.0" || uploaded.Digest != "sha256:abc" {
		t.Fatalf("upload: got %+v", uploaded)
	}
	if gotBody.Scope != "" || gotBody.Name != "auth" || gotBody.Version != "" {
		t.Fatalf("body: got %#v", gotBody)
	}
	if gotBody.DataBase64 != base64.StdEncoding.EncodeToString([]byte("package")) {
		t.Fatalf("body data: got %#v", gotBody)
	}
}

func TestClientPublishSkillVersion(t *testing.T) {
	var gotBody struct {
		Scope   string `json:"scope"`
		Name    string `json:"name"`
		Version string `json:"version"`
		Files   map[string]struct {
			DataBase64 string `json:"data_base64"`
			Mode       string `json:"mode"`
		} `json:"files"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/api/skills" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"scope":       "telos",
			"name":        "k8s-deploy",
			"version":     "1.0.0",
			"ref":         "@telos/k8s-deploy:1.0.0",
			"digest":      "sha256:abc",
			"description": "Deploy to Kubernetes.",
			"metadata":    map[string]any{"category": "deploy"},
			"file_count":  2,
			"source_ref":  "@telos/k8s-deploy:1.0.0",
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-token")
	record, err := client.PublishSkillVersion(
		"telos",
		"k8s-deploy",
		"1.0.0",
		map[string]SkillFile{
			"SKILL.md":          {Mode: "0644", Data: []byte("skill")},
			"scripts/deploy.sh": {Mode: "0755", Data: []byte("#!/bin/sh\n")},
		},
	)
	if err != nil {
		t.Fatalf("PublishSkillVersion: %v", err)
	}
	if record.Ref != "@telos/k8s-deploy:1.0.0" || record.FileCount != 2 {
		t.Fatalf("record: got %+v", record)
	}
	if gotBody.Scope != "telos" || gotBody.Name != "k8s-deploy" || gotBody.Version != "1.0.0" {
		t.Fatalf("body: got %#v", gotBody)
	}
	if gotBody.Files["SKILL.md"].DataBase64 != base64.StdEncoding.EncodeToString([]byte("skill")) {
		t.Fatalf("skill file base64: got %#v", gotBody.Files["SKILL.md"])
	}
	if gotBody.Files["scripts/deploy.sh"].Mode != "0755" {
		t.Fatalf("script mode: got %#v", gotBody.Files["scripts/deploy.sh"])
	}
}

func TestClientDownloadSkillVersionBundle(t *testing.T) {
	bundle := []byte("skill bundle")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/api/skills/telos/verify-quality/versions/1.2.3/bundle" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(bundle)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-token")
	got, err := client.DownloadSkillVersionBundle("telos", "verify-quality", "1.2.3")
	if err != nil {
		t.Fatalf("DownloadSkillVersionBundle: %v", err)
	}
	if string(got) != string(bundle) {
		t.Fatalf("bundle: got %q", got)
	}
}

func TestClientCreateSession(t *testing.T) {
	var gotBody map[string]any
	var gotOrgID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/deployments" {
			http.NotFound(w, r)
			return
		}
		gotOrgID = r.Header.Get("X-Telos-Org-Id")
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
	client.OrgID = "org_telos"
	timeout := 1800
	session, err := client.CreateSession(SessionCreateOptions{
		Name:            "auth",
		PackageRef:      "@telos/auth:1.2.3",
		AgentModel:      "sail-research/test-model",
		AgentThinking:   "high",
		AgentTimeoutSec: &timeout,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if session.ID != "sess_123" || session.Name != "auth" || session.State != "provisioning" {
		t.Fatalf("session: got %+v", session)
	}
	if gotBody["name"] != "auth" ||
		gotBody["package_ref"] != "@telos/auth:1.2.3" ||
		gotBody["agent_model"] != "sail-research/test-model" ||
		gotBody["agent_thinking"] != "high" ||
		gotBody["agent_timeout_sec"] != float64(1800) {
		t.Fatalf("body: got %#v", gotBody)
	}
	if gotOrgID != "org_telos" {
		t.Fatalf("org header: got %q", gotOrgID)
	}
}

func TestClientUpdateSession(t *testing.T) {
	var gotBody map[string]string
	var gotOrgID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/deployments/sess_123" {
			http.NotFound(w, r)
			return
		}
		gotOrgID = r.Header.Get("X-Telos-Org-Id")
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
	client.OrgID = " org_telos "
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
	if gotOrgID != "org_telos" {
		t.Fatalf("org header: got %q", gotOrgID)
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

func TestClientGetSessionLogs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/deployments/sess_123/logs" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"events":[` +
			`{"event":"agent_progress","data":{"kind":"progress_update","text":"ready"}},` +
			`{"event":"runtime.prepare.started","time":"2026-07-04T15:14:52Z","message":"preparing runtime","metadata":{"stage":"prepare"}}` +
			`]}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-token")
	events, err := client.GetSessionLogs("sess_123")
	if err != nil {
		t.Fatalf("GetSessionLogs: %v", err)
	}
	if len(events) != 2 || events[0].Event != "agent_progress" || events[0].Data["text"] != "ready" {
		t.Fatalf("events: got %#v", events)
	}
	if events[1].Event != "runtime.prepare.started" || events[1].Data["message"] != "preparing runtime" || events[1].Data["stage"] != "prepare" {
		t.Fatalf("control event: got %#v", events[1])
	}
	if events[1].Timestamp == nil || *events[1].Timestamp != "2026-07-04T15:14:52Z" {
		t.Fatalf("control event timestamp: got %#v", events[1].Timestamp)
	}
}

func TestClientStreamSessionLogs(t *testing.T) {
	var gotOrgID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/deployments/sess_123/logs" {
			http.NotFound(w, r)
			return
		}
		gotOrgID = r.Header.Get("X-Telos-Org-Id")
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Fatalf("Accept: got %q", r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"event\":\"runtime.route.succeeded\",\"time\":\"2026-07-04T15:15:00Z\",\"message\":\"service route configured\",\"metadata\":{\"stage\":\"route\"}}\n\n"))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-token")
	client.OrgID = "org_telos"
	var events []sessionapi.SessionEvent
	err := client.StreamSessionLogs(context.Background(), "sess_123", func(event sessionapi.SessionEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("StreamSessionLogs: %v", err)
	}
	if len(events) != 1 || events[0].Event != "runtime.route.succeeded" || events[0].Data["message"] != "service route configured" {
		t.Fatalf("events: got %#v", events)
	}
	if events[0].Data["stage"] != "route" {
		t.Fatalf("event metadata: got %#v", events[0].Data)
	}
	if gotOrgID != "org_telos" {
		t.Fatalf("org header: got %q", gotOrgID)
	}
}

func TestSessionCreateRequestOmitsEmptyRuntimeDefaults(t *testing.T) {
	markdown := "---\nversion: 0.1.0\nname: demo\n---\n# Demo\n"
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

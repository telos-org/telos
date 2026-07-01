package runtimeclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/sessionapi"
)

func TestClientGetSession(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sessions/test-session" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
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

	client := New(srv.URL, "test-token")
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

	client := New(srv.URL, "token")
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
	_, err = client.ListSessions(2, false)
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

func TestClientApplySessionSpec(t *testing.T) {
	var gotMethod string
	var gotPath string
	var gotBody sessionapi.SessionSpecUpdateRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
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

	client := New(srv.URL, "test-token")
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
	if response.Session == nil || response.Session.SessionID != "sess_controller" {
		t.Fatalf("session: got %#v", response.Session)
	}
}

func TestClientStopSession(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
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

	client := New(srv.URL, "token")
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

	client := New(srv.URL, "token")
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

	client := New(srv.URL, "token")
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

	client := New(srv.URL, "token")
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

	client := New(srv.URL, "token")
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
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"detail": "not found"})
	}))
	defer srv.Close()

	client := New(srv.URL, "token")
	_, err := client.GetSession("missing")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should contain 'not found': got %q", err.Error())
	}
}

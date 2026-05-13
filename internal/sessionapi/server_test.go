package sessionapi_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/telos-org/telos-go/internal/sessionapi"
)

// newTestServer returns an httptest.Server backed by a temporary FileStore.
func newTestServer(t *testing.T) (*httptest.Server, *sessionapi.FileStore) {
	t.Helper()
	root := t.TempDir()
	store := sessionapi.NewFileStore(root)
	mux := http.NewServeMux()
	sessionapi.RegisterRoutes(mux, store)
	return httptest.NewServer(mux), store
}

// --------- POST /api/sessions ---------------------------------------------------------------------------------------------------------------------------------------------------------------

func TestCreateSession(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	body := `{
		"spec_path": "/tmp/specs/my-task/SPEC.md",
		"model": "claude-opus-4-6",
		"thinking": "medium",
		"max_rounds": 4,
		"max_cost_usd": 10.0,
		"workspace": "/tmp/workspace"
	}`

	resp, err := http.Post(srv.URL+"/api/sessions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, b)
	}

	var session sessionapi.Session
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// Verify the JSON contract matches the Python Sessions API.
	assertNonEmpty(t, "session_id", session.SessionID)
	assertEqual(t, "status", string(session.Status), "pending")
	assertEqual(t, "runtime", string(session.Runtime), "local")

	if session.SessionKind == nil || *session.SessionKind != sessionapi.KindTask {
		t.Errorf("expected session_kind=task, got %v", session.SessionKind)
	}
	if session.SpecName == nil || *session.SpecName != "my-task" {
		t.Errorf("expected spec_name=my-task, got %v", session.SpecName)
	}
	if session.CreatedAt == nil || *session.CreatedAt == "" {
		t.Error("expected non-empty created_at")
	}

	// Config should reflect the request parameters.
	assertConfigStr(t, session.Config, "model", "claude-opus-4-6")
	assertConfigStr(t, session.Config, "thinking", "medium")
	assertConfigFloat(t, session.Config, "max_rounds", 4)
	assertConfigFloat(t, session.Config, "max_cost_usd", 10.0)
	assertConfigStr(t, session.Config, "workspace", "/tmp/workspace")

	// Provenance should be present (local mode).
	if session.Provenance == nil {
		t.Error("expected non-nil provenance")
	}

	// Specs array.
	if len(session.Specs) != 1 {
		t.Fatalf("expected 1 spec, got %d", len(session.Specs))
	}
	spec := session.Specs[0]
	if spec.Name == nil || *spec.Name != "my-task" {
		t.Errorf("expected spec name=my-task, got %v", spec.Name)
	}
	if spec.EvidencePath == nil || *spec.EvidencePath == "" {
		t.Error("expected non-empty evidence_path")
	}

	// Empty lists should serialize as arrays, not null.
	if session.Epochs == nil {
		t.Error("epochs should be empty array, not nil")
	}
	if session.SpecVersions == nil {
		t.Error("spec_versions should be empty array, not nil")
	}
}

func TestHealthz(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	assertEqual(t, "status_code", "200", itoa(resp.StatusCode))

	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "ok" {
		t.Fatalf("unexpected health body: %#v", body)
	}
}

func TestCreateSessionPersistsSpecMarkdown(t *testing.T) {
	root := t.TempDir()
	store := sessionapi.NewFileStore(root)
	markdown := "---\nversion: v0\nname: markdown-task\nplatform: local\ninterval: 30s\n---\n# Task\n\nDo it."

	session, err := store.Create(sessionapi.SessionCreateRequest{SpecMarkdown: &markdown})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if session.SpecName == nil || *session.SpecName != "markdown-task" {
		t.Fatalf("spec name: got %v", session.SpecName)
	}
	if session.SessionSpecPath == nil || *session.SessionSpecPath == "" {
		t.Fatal("expected top-level session_spec_path")
	}
	if session.Specs[0].SessionSpecPath == nil || *session.Specs[0].SessionSpecPath == "" {
		t.Fatal("expected spec session_spec_path")
	}
	if session.Specs[0].ContentHash == nil || *session.Specs[0].ContentHash == "" {
		t.Fatal("expected content hash")
	}
	if session.Specs[0].IntervalSeconds == nil || *session.Specs[0].IntervalSeconds != 30 {
		t.Fatalf("interval: got %v", session.Specs[0].IntervalSeconds)
	}
	data, err := os.ReadFile(*session.Specs[0].SessionSpecPath)
	if err != nil {
		t.Fatalf("read session spec: %v", err)
	}
	if string(data) != markdown {
		t.Fatalf("session spec was not persisted")
	}
}

func TestCreateSessionJSONShape(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	body := `{"spec_path": "/tmp/specs/test/SPEC.md", "model": "", "thinking": ""}`
	resp, err := http.Post(srv.URL+"/api/sessions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Verify the raw JSON has the expected top-level keys matching the Python contract.
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	requiredKeys := []string{
		"session_id", "status", "runtime", "config", "provenance",
		"specs", "epochs", "spec_versions",
	}
	for _, key := range requiredKeys {
		if _, ok := m[key]; !ok {
			t.Errorf("missing required key %q in response JSON", key)
		}
	}

	// config, provenance, specs, epochs, spec_versions must be objects/arrays.
	assertJSONType(t, m, "config", "map")
	assertJSONType(t, m, "provenance", "map")
	assertJSONType(t, m, "specs", "slice")
	assertJSONType(t, m, "epochs", "slice")
	assertJSONType(t, m, "spec_versions", "slice")
}

func TestCreateSessionRejectsUnknownFields(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	resp, err := http.Post(
		srv.URL+"/api/sessions",
		"application/json",
		strings.NewReader(`{"spec_path":"/tmp/specs/test/SPEC.md","unexpected":true}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	assertEqual(t, "status_code", "400", itoa(resp.StatusCode))
}

func TestCreateSessionRejectsOversizedBody(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	body := `{"spec_markdown":"` + strings.Repeat("x", 4<<20) + `"}`
	resp, err := http.Post(srv.URL+"/api/sessions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	assertEqual(t, "status_code", "400", itoa(resp.StatusCode))
}

// --------- GET /api/sessions ------------------------------------------------------------------------------------------------------------------------------------------------------------------

func TestListSessions(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	// Initially empty.
	resp, err := http.Get(srv.URL + "/api/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	assertEqual(t, "status", "200", itoa(resp.StatusCode))

	var listResp sessionapi.SessionListResponse
	if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
		t.Fatal(err)
	}
	if len(listResp.Sessions) != 0 {
		t.Errorf("expected 0 sessions, got %d", len(listResp.Sessions))
	}

	// Create two sessions, list should return them newest first.
	post(t, srv.URL+"/api/sessions", `{"spec_path":"/tmp/a/SPEC.md","model":"","thinking":""}`)
	post(t, srv.URL+"/api/sessions", `{"spec_path":"/tmp/b/SPEC.md","model":"","thinking":""}`)

	resp2, err := http.Get(srv.URL + "/api/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()

	var listResp2 sessionapi.SessionListResponse
	json.NewDecoder(resp2.Body).Decode(&listResp2)
	if len(listResp2.Sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(listResp2.Sessions))
	}

	// Both must have the full Session shape.
	for _, s := range listResp2.Sessions {
		assertNonEmpty(t, "session_id", s.SessionID)
		assertEqual(t, "runtime", string(s.Runtime), "local")
	}
}

func TestListSessionsJSONShape(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	json.Unmarshal(raw, &m)
	assertJSONType(t, m, "sessions", "slice")
}

// --------- GET /api/sessions/{id} ---------------------------------------------------------------------------------------------------------------------------------------------------

func TestGetSession(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	created := createSession(t, srv.URL, `{"spec_path":"/tmp/x/SPEC.md","model":"m","thinking":"high"}`)

	resp, err := http.Get(srv.URL + "/api/sessions/" + created.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	assertEqual(t, "status_code", "200", itoa(resp.StatusCode))

	var session sessionapi.Session
	json.NewDecoder(resp.Body).Decode(&session)
	assertEqual(t, "session_id", created.SessionID, session.SessionID)
	assertEqual(t, "status", "pending", string(session.Status))
}

func TestGetSessionNotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/sessions/nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	assertEqual(t, "status_code", "404", itoa(resp.StatusCode))
}

// --------- POST /api/sessions/{id}/stop ---------------------------------------------------------------------------------------------------------------------------------

func TestStopSession(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	created := createSession(t, srv.URL, `{"spec_path":"/tmp/s/SPEC.md","model":"","thinking":""}`)

	req, _ := http.NewRequest("POST", srv.URL+"/api/sessions/"+created.SessionID+"/stop", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	assertEqual(t, "status_code", "200", itoa(resp.StatusCode))

	var session sessionapi.Session
	json.NewDecoder(resp.Body).Decode(&session)
	assertEqual(t, "status", "stopped", string(session.Status))

	if session.Error == nil || *session.Error != "stopped by operator" {
		t.Errorf("expected error='stopped by operator', got %v", session.Error)
	}
}

func TestStopAlreadyStopped(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	created := createSession(t, srv.URL, `{"spec_path":"/tmp/s2/SPEC.md","model":"","thinking":""}`)

	// Stop twice - second should be idempotent.
	stopSession(t, srv.URL, created.SessionID)
	session := stopSession(t, srv.URL, created.SessionID)
	assertEqual(t, "status", "stopped", string(session.Status))
}

func TestStopSessionNotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	req, _ := http.NewRequest("POST", srv.URL+"/api/sessions/nonexistent/stop", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	assertEqual(t, "status_code", "404", itoa(resp.StatusCode))
}

// --------- GET /api/sessions/{id}/transcript ------------------------------------------------------------------------------------------------------------------

func TestTranscriptNotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	created := createSession(t, srv.URL, `{"spec_path":"/tmp/t/SPEC.md","model":"","thinking":""}`)

	resp, err := http.Get(srv.URL + "/api/sessions/" + created.SessionID + "/transcript")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	assertEqual(t, "status_code", "404", itoa(resp.StatusCode))
}

func TestTranscriptPresent(t *testing.T) {
	srv, store := newTestServer(t)
	defer srv.Close()

	created := createSession(t, srv.URL, `{"spec_path":"/tmp/tp/SPEC.md","model":"","thinking":""}`)

	// Write a transcript file in the expected location.
	transcriptPath := filepath.Join(store.Root, created.SessionID, "specs", "tp",
		"pvg-transcript-"+created.SessionID+".md")
	os.MkdirAll(filepath.Dir(transcriptPath), 0o755)
	os.WriteFile(transcriptPath, []byte("# Transcript\nHello"), 0o644)

	resp, err := http.Get(srv.URL + "/api/sessions/" + created.SessionID + "/transcript")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	assertEqual(t, "status_code", "200", itoa(resp.StatusCode))
	assertEqual(t, "content-type", "text/plain; charset=utf-8", resp.Header.Get("Content-Type"))

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "# Transcript\nHello" {
		t.Errorf("unexpected transcript body: %q", body)
	}
}

// --------- GET /api/sessions/{id}/events ------------------------------------------------------------------------------------------------------------------------------

func TestEventsEmpty(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	created := createSession(t, srv.URL, `{"spec_path":"/tmp/e/SPEC.md","model":"","thinking":""}`)

	resp, err := http.Get(srv.URL + "/api/sessions/" + created.SessionID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	assertEqual(t, "status_code", "200", itoa(resp.StatusCode))

	var evResp sessionapi.SessionEventsResponse
	json.NewDecoder(resp.Body).Decode(&evResp)
	if len(evResp.Events) != 0 {
		t.Errorf("expected 0 events, got %d", len(evResp.Events))
	}
}

func TestEventsPresent(t *testing.T) {
	srv, store := newTestServer(t)
	defer srv.Close()

	created := createSession(t, srv.URL, `{"spec_path":"/tmp/ep/SPEC.md","model":"","thinking":""}`)

	// Write evidence JSONL.
	evidencePath := filepath.Join(store.Root, created.SessionID, "specs", "ep", "evidence.jsonl")
	os.MkdirAll(filepath.Dir(evidencePath), 0o755)
	lines := []string{
		`{"event":"agent_complete","data":{"cost_usd":0.5,"role":"prover"}}`,
		`{"event":"game_end","data":{"game_result":"accepted"}}`,
	}
	os.WriteFile(evidencePath, []byte(strings.Join(lines, "\n")+"\n"), 0o644)

	resp, err := http.Get(srv.URL + "/api/sessions/" + created.SessionID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	assertEqual(t, "status_code", "200", itoa(resp.StatusCode))

	var evResp sessionapi.SessionEventsResponse
	json.NewDecoder(resp.Body).Decode(&evResp)

	if len(evResp.Events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(evResp.Events))
	}

	// Verify event shape matches the Python contract.
	assertEqual(t, "event[0].event", "agent_complete", evResp.Events[0].Event)
	if evResp.Events[0].SpecName == nil || *evResp.Events[0].SpecName != "ep" {
		t.Errorf("expected spec_name=ep, got %v", evResp.Events[0].SpecName)
	}
	if evResp.Events[0].Data == nil {
		t.Fatal("expected non-nil data")
	}
	if evResp.Events[0].Data["cost_usd"] != 0.5 {
		t.Errorf("expected cost_usd=0.5, got %v", evResp.Events[0].Data["cost_usd"])
	}

	assertEqual(t, "event[1].event", "game_end", evResp.Events[1].Event)
}

func TestEventsJSONShape(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	created := createSession(t, srv.URL, `{"spec_path":"/tmp/ejs/SPEC.md","model":"","thinking":""}`)

	resp, err := http.Get(srv.URL + "/api/sessions/" + created.SessionID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	json.Unmarshal(raw, &m)
	assertJSONType(t, m, "events", "slice")
}

func TestEventsSSE(t *testing.T) {
	srv, store := newTestServer(t)
	defer srv.Close()

	created := createSession(t, srv.URL, `{"spec_path":"/tmp/es/SPEC.md","model":"","thinking":""}`)
	evidencePath := filepath.Join(store.Root, created.SessionID, "specs", "es", "evidence.jsonl")
	os.MkdirAll(filepath.Dir(evidencePath), 0o755)
	os.WriteFile(evidencePath, []byte(`{"event":"game_end","data":{"game_result":"success"}}`+"\n"), 0o644)
	stopSession(t, srv.URL, created.SessionID)

	req, err := http.NewRequest("GET", srv.URL+"/api/sessions/"+created.SessionID+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	assertEqual(t, "status_code", "200", itoa(resp.StatusCode))
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("content-type: got %q", resp.Header.Get("Content-Type"))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `data: {"event":"game_end"`) {
		t.Fatalf("unexpected SSE body: %s", body)
	}
}

// --------- GET /api/sessions/{id}/workspace/{spec} ------------------------------------------------------------------------------------------------

func TestWorkspaceNotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	created := createSession(t, srv.URL, `{"spec_path":"/tmp/w/SPEC.md","model":"","thinking":""}`)

	resp, err := http.Get(srv.URL + "/api/sessions/" + created.SessionID + "/workspace/w")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	assertEqual(t, "status_code", "404", itoa(resp.StatusCode))
}

func TestWorkspacePresent(t *testing.T) {
	srv, store := newTestServer(t)
	defer srv.Close()

	created := createSession(t, srv.URL, `{"spec_path":"/tmp/wp/SPEC.md","model":"","thinking":""}`)

	// Create the workspace archive.
	workspacePath := filepath.Join(store.Root, created.SessionID, "specs", "wp", "workspace.tar.gz")
	os.MkdirAll(filepath.Dir(workspacePath), 0o755)
	os.WriteFile(workspacePath, []byte("fake-archive-content"), 0o644)

	// Update the manifest to include workspace_path.
	mpath := filepath.Join(store.Root, created.SessionID, "session.json")
	raw, _ := os.ReadFile(mpath)
	var m map[string]any
	json.Unmarshal(raw, &m)
	specs := m["specs"].([]any)
	spec0 := specs[0].(map[string]any)
	spec0["workspace_path"] = workspacePath
	updated, _ := json.MarshalIndent(m, "", "  ")
	os.WriteFile(mpath, updated, 0o644)

	resp, err := http.Get(srv.URL + "/api/sessions/" + created.SessionID + "/workspace/wp")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	assertEqual(t, "status_code", "200", itoa(resp.StatusCode))

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "fake-archive-content" {
		t.Errorf("unexpected workspace body: %q", body)
	}
}

// --------- Session lifecycle ------------------------------------------------------------------------------------------------------------------------------------------------------------------

func TestSessionLifecycleStatus(t *testing.T) {
	srv, store := newTestServer(t)
	defer srv.Close()

	created := createSession(t, srv.URL, `{"spec_path":"/tmp/lc/SPEC.md","model":"","thinking":""}`)
	assertEqual(t, "initial status", "pending", string(created.Status))

	// Simulate an open epoch (running).
	mpath := filepath.Join(store.Root, created.SessionID, "session.json")
	raw, _ := os.ReadFile(mpath)
	var m map[string]any
	json.Unmarshal(raw, &m)
	m["epochs"] = []any{
		map[string]any{
			"id":          1,
			"started_at":  "2026-01-01T00:00:00.000Z",
			"finished_at": nil,
			"result":      nil,
			"error":       nil,
			"runner":      nil,
		},
	}
	updated, _ := json.MarshalIndent(m, "", "  ")
	os.WriteFile(mpath, updated, 0o644)

	session := getSession(t, srv.URL, created.SessionID)
	assertEqual(t, "running status", "running", string(session.Status))

	// Stop it.
	stopped := stopSession(t, srv.URL, created.SessionID)
	assertEqual(t, "stopped status", "stopped", string(stopped.Status))
}

func TestSessionStatusCompleted(t *testing.T) {
	srv, store := newTestServer(t)
	defer srv.Close()

	created := createSession(t, srv.URL, `{"spec_path":"/tmp/comp/SPEC.md","model":"","thinking":""}`)

	// Simulate completed epoch.
	mpath := filepath.Join(store.Root, created.SessionID, "session.json")
	raw, _ := os.ReadFile(mpath)
	var m map[string]any
	json.Unmarshal(raw, &m)
	finished := "2026-01-01T00:01:00.000Z"
	result := "completed"
	m["epochs"] = []any{
		map[string]any{
			"id":          1,
			"started_at":  "2026-01-01T00:00:00.000Z",
			"finished_at": finished,
			"result":      result,
			"error":       nil,
			"runner":      nil,
		},
	}
	updated, _ := json.MarshalIndent(m, "", "  ")
	os.WriteFile(mpath, updated, 0o644)

	session := getSession(t, srv.URL, created.SessionID)
	assertEqual(t, "completed status", "completed", string(session.Status))
}

func TestSessionStatusFailed(t *testing.T) {
	srv, store := newTestServer(t)
	defer srv.Close()

	created := createSession(t, srv.URL, `{"spec_path":"/tmp/fail/SPEC.md","model":"","thinking":""}`)

	mpath := filepath.Join(store.Root, created.SessionID, "session.json")
	raw, _ := os.ReadFile(mpath)
	var m map[string]any
	json.Unmarshal(raw, &m)
	finished := "2026-01-01T00:01:00.000Z"
	result := "failed"
	errMsg := "some error"
	m["epochs"] = []any{
		map[string]any{
			"id":          1,
			"started_at":  "2026-01-01T00:00:00.000Z",
			"finished_at": finished,
			"result":      result,
			"error":       errMsg,
			"runner":      nil,
		},
	}
	updated, _ := json.MarshalIndent(m, "", "  ")
	os.WriteFile(mpath, updated, 0o644)

	session := getSession(t, srv.URL, created.SessionID)
	assertEqual(t, "failed status", "failed", string(session.Status))
	if session.Error == nil || *session.Error != "some error" {
		t.Errorf("expected error='some error', got %v", session.Error)
	}
}

// --------- Python API JSON compatibility ------------------------------------------------------------------------------------------------------------------------------

func TestPythonManifestCompat(t *testing.T) {
	// Verify that a manifest written in the Python format can be read back
	// and produces the expected Session shape.
	root := t.TempDir()
	store := sessionapi.NewFileStore(root)

	id := "local_20260510_131841_00"
	dir := filepath.Join(root, id)
	os.MkdirAll(dir, 0o755)

	specDir := filepath.Join(dir, "specs", "my-spec")
	os.MkdirAll(specDir, 0o755)

	evidencePath := filepath.Join(specDir, "evidence.jsonl")
	transcriptPath := filepath.Join(specDir, "pvg-transcript-"+id+".md")

	os.WriteFile(transcriptPath, []byte("# Test transcript"), 0o644)
	os.WriteFile(evidencePath, []byte(`{"event":"agent_complete","data":{"cost_usd":1.23}}`+"\n"), 0o644)

	// Write a manifest in the Python format.
	manifest := map[string]any{
		"session_id":        id,
		"session_kind":      "task",
		"created_at":        "2026-05-10T20:18:41.680Z",
		"launcher":          "local",
		"parent_session_id": nil,
		"source_spec_path":  "/tmp/my-spec/SPEC.md",
		"session_spec_path": filepath.Join(specDir, "spec.md"),
		"spec_name":         "my-spec",
		"config": map[string]any{
			"model":      "claude-opus-4-6",
			"max_rounds": 8,
			"thinking":   "medium",
		},
		"provenance": map[string]any{"mode": "local"},
		"specs": []any{
			map[string]any{
				"index":            0,
				"name":             "my-spec",
				"dir_name":         "my-spec",
				"evidence_path":    evidencePath,
				"transcript_path":  transcriptPath,
				"workspace_path":   nil,
				"interval_seconds": nil,
			},
		},
		"epochs": []any{
			map[string]any{
				"id":          1,
				"started_at":  "2026-05-10T20:18:41.682Z",
				"finished_at": "2026-05-10T20:24:55.834Z",
				"result":      "completed",
				"error":       nil,
				"runner": map[string]any{
					"kind": "local-subprocess",
					"pid":  87080,
				},
			},
		},
	}

	data, _ := json.MarshalIndent(manifest, "", "  ")
	os.WriteFile(filepath.Join(dir, "session.json"), data, 0o644)

	// Read it back via the store.
	session, err := store.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	assertEqual(t, "session_id", id, session.SessionID)
	assertEqual(t, "status", "completed", string(session.Status))
	assertEqual(t, "runtime", "local", string(session.Runtime))

	if session.SessionKind == nil || *session.SessionKind != sessionapi.KindTask {
		t.Errorf("expected session_kind=task, got %v", session.SessionKind)
	}
	if session.SpecName == nil || *session.SpecName != "my-spec" {
		t.Errorf("expected spec_name=my-spec, got %v", session.SpecName)
	}

	// Config roundtrip.
	assertConfigStr(t, session.Config, "model", "claude-opus-4-6")

	// Spec shape.
	if len(session.Specs) != 1 {
		t.Fatalf("expected 1 spec, got %d", len(session.Specs))
	}
	spec := session.Specs[0]
	if spec.EvidenceExists == nil || !*spec.EvidenceExists {
		t.Error("expected evidence_exists=true")
	}
	if spec.TranscriptExists == nil || !*spec.TranscriptExists {
		t.Error("expected transcript_exists=true")
	}

	// Transcript.
	text, err := store.Transcript(id)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "transcript", "# Test transcript", text)

	// Events.
	events, err := store.Events(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	assertEqual(t, "event", "agent_complete", events[0].Event)
}

// --------- Test helpers ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------

func createSession(t *testing.T, baseURL string, body string) sessionapi.Session {
	t.Helper()
	resp, err := http.Post(baseURL+"/api/sessions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("create session: expected 201, got %d: %s", resp.StatusCode, b)
	}
	var s sessionapi.Session
	json.NewDecoder(resp.Body).Decode(&s)
	return s
}

func getSession(t *testing.T, baseURL string, id string) sessionapi.Session {
	t.Helper()
	resp, err := http.Get(baseURL + "/api/sessions/" + id)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var s sessionapi.Session
	json.NewDecoder(resp.Body).Decode(&s)
	return s
}

func stopSession(t *testing.T, baseURL string, id string) sessionapi.Session {
	t.Helper()
	req, _ := http.NewRequest("POST", baseURL+"/api/sessions/"+id+"/stop", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var s sessionapi.Session
	json.NewDecoder(resp.Body).Decode(&s)
	return s
}

func post(t *testing.T, url string, body string) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

func assertEqual(t *testing.T, label string, expected, actual string) {
	t.Helper()
	if expected != actual {
		t.Errorf("%s: expected %q, got %q", label, expected, actual)
	}
}

func assertNonEmpty(t *testing.T, label string, value string) {
	t.Helper()
	if value == "" {
		t.Errorf("%s: expected non-empty string", label)
	}
}

func assertConfigStr(t *testing.T, config map[string]any, key string, expected string) {
	t.Helper()
	v, ok := config[key]
	if !ok {
		t.Errorf("config missing key %q", key)
		return
	}
	if s, ok := v.(string); !ok || s != expected {
		t.Errorf("config[%q]: expected %q, got %v", key, expected, v)
	}
}

func assertConfigFloat(t *testing.T, config map[string]any, key string, expected float64) {
	t.Helper()
	v, ok := config[key]
	if !ok {
		t.Errorf("config missing key %q", key)
		return
	}
	if f, ok := v.(float64); !ok || f != expected {
		t.Errorf("config[%q]: expected %v, got %v", key, expected, v)
	}
}

func assertJSONType(t *testing.T, m map[string]any, key string, kind string) {
	t.Helper()
	v, ok := m[key]
	if !ok {
		t.Errorf("missing key %q", key)
		return
	}
	switch kind {
	case "map":
		if _, ok := v.(map[string]any); !ok {
			t.Errorf("%q: expected object, got %T", key, v)
		}
	case "slice":
		if _, ok := v.([]any); !ok {
			t.Errorf("%q: expected array, got %T", key, v)
		}
	}
}

func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}

package sessionapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func testInferenceConnection() *InferenceConnection {
	return &InferenceConnection{ID: "grant-new", Provider: "provider", BaseURL: "https://models.example/v1", ProxyURL: "http://172.31.255.1:20000", APIKey: "telos-proxy-" + strings.Repeat("a", 43)}
}

func TestInferenceConnectionIsDurableAndRedacted(t *testing.T) {
	store, path := inferenceStore(t)
	setInferencePi(t, true)
	model := "provider/new"
	connection := testInferenceConnection()
	definition := json.RawMessage(`{"id":"new","name":"New","api":"openai-responses","reasoning":true,"input":["text"],"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0},"contextWindow":128000,"maxTokens":8192}`)
	req := InferenceUpdateRequest{RequestID: "connection", Model: &model, ModelDefinition: definition, Connection: connection}
	state, err := store.UpdateInference("session", req)
	if err != nil || !state.ConnectionSwitching || state.Settings.ConnectionID != "" || state.Update.Connection.APIKey != "" {
		t.Fatalf("pending response: %+v %v", state, err)
	}
	encoded, _ := json.Marshal(state)
	if strings.Contains(string(encoded), connection.APIKey) {
		t.Fatal("response exposed the prepared placeholder")
	}
	if _, err := store.UpdateInference("session", req); err != nil {
		t.Fatalf("redacted response broke durable retry: %v", err)
	}
	different := *connection
	different.APIKey = "telos-proxy-" + strings.Repeat("b", 43)
	other := req
	other.Connection = &different
	if _, err := store.UpdateInference("session", other); !errors.Is(err, ErrConflict) {
		t.Fatalf("reusing request id changed credentials: %v", err)
	}
	update, err := ClaimInferenceUpdate(path, "attempt", "receipt.json", InferenceSettings{})
	if err != nil || update.Connection.APIKey != connection.APIKey || update.Settings.ConnectionID != connection.ID {
		t.Fatalf("claim lost prepared connection: %+v %v", update, err)
	}
	if err := FinishInferenceUpdate(path, req.RequestID, "applied", "", update.Settings); err != nil {
		t.Fatal(err)
	}
	thinking := "high"
	if _, err := store.UpdateInference("session", InferenceUpdateRequest{RequestID: "thinking", ExpectedRevision: 1, Thinking: &thinking}); err != nil {
		t.Fatal(err)
	}
	update, err = ClaimInferenceUpdate(path, "next-attempt", "next.json", InferenceSettings{})
	if err != nil || update.Settings.ConnectionID != connection.ID {
		t.Fatalf("thinking change lost connection: %+v %v", update, err)
	}
	if err := FinishInferenceUpdate(path, "thinking", "applied", "", update.Settings); err != nil {
		t.Fatal(err)
	}
	saved, err := ReadManifest(path)
	if err != nil || saved.InferenceConnection == nil || *saved.InferenceConnection != *connection {
		t.Fatalf("restart lost connection: %+v %v", saved, err)
	}
	state, err = NewFileStore(store.Root, RuntimeLocal).Inference("session")
	if err != nil || state.Settings.ConnectionID != connection.ID || state.Settings.Thinking != thinking {
		t.Fatalf("confirmed connection: %+v %v", state, err)
	}
}

func setInferencePi(t *testing.T, supported bool) {
	t.Helper()
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nexit 78\n"
	if supported {
		script = "#!/bin/sh\nprintf 'TELOS_PI_CONNECTION_SWITCHING\\n'\n"
	}
	if err := os.WriteFile(filepath.Join(bin, "pi"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
}

func TestInferenceConnectionCapabilityGatesNewRequests(t *testing.T) {
	store, path := inferenceStore(t)
	model, thinking := "provider/new", "high"
	req := InferenceUpdateRequest{
		RequestID: "connection", Model: &model, Thinking: &thinking,
		Connection:      testInferenceConnection(),
		ModelDefinition: json.RawMessage(`{"id":"new","name":"New","api":"openai-responses","reasoning":true,"input":["text"],"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0},"contextWindow":128000,"maxTokens":8192}`),
	}
	setInferencePi(t, false)
	state, err := store.Inference("session")
	if err != nil || state.ConnectionSwitching {
		t.Fatalf("unsupported Pi advertised connection switching: %+v %v", state, err)
	}
	before, _ := os.ReadFile(path)
	if _, err := store.UpdateInference("session", req); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("unsupported Pi accepted connection: %v", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("rejected connection changed the session")
	}
	// A Pi upgrade is visible to the same store without a runtime restart.
	setInferencePi(t, true)
	state, err = store.UpdateInference("session", req)
	if err != nil || !state.ConnectionSwitching || state.Update.Status != "pending" {
		t.Fatalf("supported Pi rejected connection: %+v %v", state, err)
	}
	claimed, err := ClaimInferenceUpdate(path, "attempt", "receipt.json", InferenceSettings{})
	if err != nil {
		t.Fatal(err)
	}
	if err := FinishInferenceUpdate(path, req.RequestID, "applied", "", claimed.Settings); err != nil {
		t.Fatal(err)
	}
	setInferencePi(t, false)
	// Replaying a completed request is still safe after a downgrade.
	state, err = store.UpdateInference("session", req)
	if err != nil || state.ConnectionSwitching || state.Update.Status != "applied" {
		t.Fatalf("capability change broke replay: %+v %v", state, err)
	}
	// Thinking-only changes still use the confirmed connection at startup.
	before, _ = os.ReadFile(path)
	if _, err := store.UpdateInference("session", InferenceUpdateRequest{RequestID: "thinking", ExpectedRevision: 1, Thinking: &thinking}); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("unsupported Pi accepted change on confirmed connection: %v", err)
	}
	after, _ = os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("rejected thinking change changed the session")
	}
}

func TestInferenceLegacyPiCanStillQueueModelAndThinking(t *testing.T) {
	store, _ := inferenceStore(t)
	setInferencePi(t, false)
	model, thinking := "provider/new", "high"
	state, err := store.UpdateInference("session", InferenceUpdateRequest{RequestID: "settings", Model: &model, Thinking: &thinking})
	if err != nil || state.ConnectionSwitching || state.Update.Status != "pending" {
		t.Fatalf("legacy model/thinking update: %+v %v", state, err)
	}
}

func TestInferenceConnectionValidation(t *testing.T) {
	model := "provider/new"
	definition := json.RawMessage(`{"id":"new","name":"New","api":"openai-responses","reasoning":true,"input":["text"],"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0},"contextWindow":128000,"maxTokens":8192}`)
	for _, name := range []string{"valid", "codex", "secret", "command", "environment", "bad_codex", "http", "url_auth", "url_query", "proxy_host", "proxy_port", "proxy_suffix", "proxy_range", "provider", "id", "no_model", "no_definition", "partial_definition"} {
		t.Run(name, func(t *testing.T) {
			req := InferenceUpdateRequest{RequestID: "request", Model: &model, ModelDefinition: definition, Connection: testInferenceConnection()}
			switch name {
			case "codex":
				req.Connection.APIKey = "telos-proxy.eyJodHRwczovL2FwaS5vcGVuYWkuY29tL2F1dGgiOnsiY2hhdGdwdF9hY2NvdW50X2lkIjoidGVsb3MtcHJveHkifX0." + strings.Repeat("b", 43)
			case "secret":
				req.Connection.APIKey = "sk-real-secret"
			case "command":
				req.Connection.APIKey = "!cat /etc/passwd"
			case "environment":
				req.Connection.APIKey = "$ANTHROPIC_API_KEY"
			case "bad_codex":
				req.Connection.APIKey = "telos-proxy.e30." + strings.Repeat("a", 43)
			case "http":
				req.Connection.BaseURL = "http://models.example/v1"
			case "url_auth":
				req.Connection.BaseURL = "https://user:secret@models.example/v1"
			case "url_query":
				req.Connection.BaseURL = "https://models.example/v1?api_key=secret"
			case "proxy_host":
				req.Connection.ProxyURL = "http://127.0.0.1:20000"
			case "proxy_port":
				req.Connection.ProxyURL = "http://172.31.255.1:20001"
			case "proxy_suffix":
				req.Connection.ProxyURL = "http://172.31.255.1:20000/"
			case "proxy_range":
				req.Connection.ProxyURL = "http://172.31.255.1:20256"
			case "provider":
				req.Connection.Provider = "other"
			case "id":
				req.Connection.ID = "../grant"
			case "no_model":
				req.Model = nil
			case "no_definition":
				req.ModelDefinition = nil
			case "partial_definition":
				req.ModelDefinition = json.RawMessage(`{"id":"new"}`)
			}
			err := req.Validate()
			if (err == nil) != (name == "valid" || name == "codex") {
				t.Fatalf("validation: %v", err)
			}
		})
	}
}

func inferenceStore(t *testing.T) (*FileStore, string) {
	t.Helper()
	store := NewFileStore(t.TempDir(), RuntimeLocal)
	path := filepath.Join(store.Root, "session", "session.json")
	m := &Manifest{SessionID: "session", SessionKind: KindController, Config: SessionConfig{Model: "provider/old", Thinking: "medium"}}
	if err := WriteManifest(path, m); err != nil {
		t.Fatal(err)
	}
	return store, path
}

func TestInferenceCompareAndSwapAndDurableReplay(t *testing.T) {
	store, path := inferenceStore(t)
	model := "provider/new"
	req := InferenceUpdateRequest{RequestID: "change-1", Model: &model}
	response, err := store.UpdateInference("session", req)
	if err != nil || response.Update.Status != "pending" || response.Settings.Model != "provider/old" {
		t.Fatalf("accepted: %+v %v", response, err)
	}
	if _, err := NewFileStore(store.Root, RuntimeLocal).UpdateInference("session", req); err != nil {
		t.Fatal(err)
	}
	other := "provider/different"
	if _, err := store.UpdateInference("session", InferenceUpdateRequest{RequestID: req.RequestID, Model: &other}); !errors.Is(err, ErrConflict) {
		t.Fatalf("reused ID: %v", err)
	}
	if _, err := store.UpdateInference("session", InferenceUpdateRequest{RequestID: "change-2", ExpectedRevision: 1, Model: &other}); !errors.Is(err, ErrConflict) {
		t.Fatalf("concurrent change: %v", err)
	}
	claimed, err := ClaimInferenceUpdate(path, "attempt", "receipt.json", InferenceSettings{})
	if err != nil || claimed == nil {
		t.Fatal(err)
	}
	if again, err := ClaimInferenceUpdate(path, "attempt", "receipt.json", InferenceSettings{}); err != nil || again != nil {
		t.Fatalf("duplicate claim: %+v %v", again, err)
	}
	confirmed := &InferenceSettings{Model: model, Thinking: "low"}
	if err := FinishInferenceUpdate(path, req.RequestID, "applied", "", confirmed); err != nil {
		t.Fatal(err)
	}
	response, err = store.UpdateInference("session", req)
	if err != nil || response.Update.Status != "applied" || response.Settings != *confirmed {
		t.Fatalf("replayed confirmation: %+v %v", response, err)
	}
	if _, err := store.UpdateInference("session", InferenceUpdateRequest{RequestID: "stale", Model: &other}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale revision: %v", err)
	}
}

func TestInferenceModelDefinitionSurvivesSameModelAndThinkingChanges(t *testing.T) {
	store, path := inferenceStore(t)
	model, thinking := "provider/new", "high"
	definition := json.RawMessage(`{"id":"new","api":"openai-responses"}`)
	requests := []InferenceUpdateRequest{
		{RequestID: "model", Model: &model, ModelDefinition: definition},
		{RequestID: "same", ExpectedRevision: 1, Model: &model},
		{RequestID: "thinking", ExpectedRevision: 2, Thinking: &thinking},
	}
	for _, req := range requests {
		if _, err := store.UpdateInference("session", req); err != nil {
			t.Fatal(err)
		}
		if _, err := ClaimInferenceUpdate(path, "attempt", "receipt.json", InferenceSettings{}); err != nil {
			t.Fatal(err)
		}
		if err := FinishInferenceUpdate(path, req.RequestID, "applied", "", &InferenceSettings{Model: model, Thinking: thinking}); err != nil {
			t.Fatal(err)
		}
		m, err := ReadManifest(path)
		if err != nil || !sameJSON(m.InferenceModelDefinition, definition) {
			t.Fatalf("%s lost model definition: %v", req.RequestID, err)
		}
	}
}

func TestInferenceConcurrentWritersHaveOneWinner(t *testing.T) {
	store, _ := inferenceStore(t)
	model := "provider/new"
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, id := range []string{"a", "b"} {
		wg.Go(func() {
			_, err := NewFileStore(store.Root, RuntimeLocal).UpdateInference("session", InferenceUpdateRequest{RequestID: id, Model: &model})
			results <- err
		})
	}
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		} else if !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
	}
	if wins != 1 {
		t.Fatalf("winners: %d", wins)
	}
}

func TestInferenceCrashAndStopOutcomes(t *testing.T) {
	for _, phase := range []string{"pending", "applying", "crash"} {
		t.Run(phase, func(t *testing.T) {
			store, path := inferenceStore(t)
			level := "high"
			if _, err := store.UpdateInference("session", InferenceUpdateRequest{RequestID: "change", Thinking: &level}); err != nil {
				t.Fatal(err)
			}
			if phase != "pending" {
				if _, err := ClaimInferenceUpdate(path, "attempt", "receipt.json", InferenceSettings{}); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "crash" {
				if err := RecoverInferenceUpdate(path); err != nil {
					t.Fatal(err)
				}
			} else if _, err := store.Stop("session"); err != nil {
				t.Fatal(err)
			}
			response, err := store.Inference("session")
			if err != nil {
				t.Fatal(err)
			}
			want := "unknown"
			if phase == "pending" {
				want = "rejected"
			}
			if response.Update.Status != want || response.Settings.Thinking != "medium" {
				t.Fatalf("outcome: %+v", response)
			}
			if claimed, err := ClaimInferenceUpdate(path, "attempt", "receipt.json", InferenceSettings{}); err != nil || claimed != nil {
				t.Fatalf("replayed uncertain command: %+v %v", claimed, err)
			}
		})
	}
}

func TestInferenceRoutesValidateAndAuthorize(t *testing.T) {
	store, path := inferenceStore(t)
	_, err := MutateManifest(path, func(m *Manifest) error {
		m.Access = &ScopedToken{APIToken: "agent-token", SubjectSessionID: "session", Scopes: []string{string(ScopeSessionsRead), string(ScopeSessionsApply)}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	RegisterRoutes(mux, store, NewBearerAuthorizer(store, "operator-token"), RuntimeIdentity{})
	for _, tc := range []struct {
		token, body string
		want        int
	}{
		{"", `{"request_id":"x","thinking":"high"}`, 401},
		{"agent-token", `{"request_id":"x","thinking":"high"}`, 403},
		{"operator-token", `{"request_id":"x"}`, 400},
		{"operator-token", `{"request_id":"x","model":"p/m","model_definition":{"id":"m","apiKey":"secret"}}`, 400},
		{"operator-token", `{"request_id":"x","thinking":"high","model":"p/m"}`, 202},
	} {
		request := httptest.NewRequest("PUT", "/api/sessions/session/inference", bytes.NewBufferString(tc.body))
		request.Header.Set("Authorization", "Bearer "+tc.token)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != tc.want {
			t.Fatalf("%s: %d %s", tc.token, response.Code, response.Body.String())
		}
	}
	request := httptest.NewRequest("GET", "/api/sessions/session/inference", nil)
	request.Header.Set("Authorization", "Bearer agent-token")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	var state InferenceResponse
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &state) != nil || state.Update.Status != "pending" {
		t.Fatalf("read outcome: %d %s", response.Code, response.Body.String())
	}
}

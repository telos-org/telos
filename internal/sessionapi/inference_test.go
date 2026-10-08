package sessionapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
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
	if err != nil || state.ConnectionSwitching == nil || !*state.ConnectionSwitching || state.Settings.ConnectionID != "" || state.Update.Connection.APIKey != "" {
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

func setInferencePi(t *testing.T, supported bool) string {
	t.Helper()
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf("#!/bin/sh\nprintf '{\"connection_switching\":%t,\"executable\":\"%%s\"}\\n' \"$0\"\n", supported)
	path := filepath.Join(bin, "pi")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	return path
}

func TestInferenceConnectionCancellationFencesPreparedAndDelayedHandoffs(t *testing.T) {
	for _, queued := range []bool{false, true} {
		t.Run(fmt.Sprintf("queued=%t", queued), func(t *testing.T) {
			store, path := inferenceStore(t)
			binary := setInferencePi(t, true)
			model := "provider/new"
			req := InferenceUpdateRequest{
				RequestID: "connection", Model: &model, Connection: testInferenceConnection(),
				ModelDefinition: json.RawMessage(`{"id":"new","name":"New","api":"openai-responses","reasoning":true,"input":["text"],"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0},"contextWindow":128000,"maxTokens":8192}`),
			}
			if queued {
				if _, err := store.UpdateInference("session", req); err != nil {
					t.Fatal(err)
				}
			}
			// Removing Pi must not prevent cancelling a saved or delayed handoff.
			if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 78\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			cancelRequest := req
			if !queued {
				// Cloud may cancel before credential preparation has completed.
				cancelRequest.Connection = nil
			}
			state, err := store.CancelInference("session", cancelRequest)
			if err != nil || state.Update.Status != "rejected" || state.Settings.ConnectionID != "" {
				t.Fatalf("cancel connection: %+v %v", state, err)
			}
			if state.Update.Connection != nil && state.Update.Connection.APIKey != "" {
				t.Fatal("cancel response exposed the prepared placeholder")
			}
			late, err := store.UpdateInference("session", req)
			if queued && (err != nil || late.Update.Status != "rejected") {
				t.Fatalf("prepared replay escaped cancellation: %+v %v", late, err)
			}
			if !queued && !errors.Is(err, ErrConflict) {
				t.Fatalf("late preparation escaped the cancellation tombstone: %+v %v", late, err)
			}
			if claimed, err := ClaimInferenceUpdate(path, "attempt", "receipt", InferenceSettings{}); err != nil || claimed != nil {
				t.Fatalf("cancelled connection was claimed: %+v %v", claimed, err)
			}
		})
	}
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
	if err != nil || state.ConnectionSwitching == nil || *state.ConnectionSwitching {
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
	if err != nil || state.ConnectionSwitching == nil || !*state.ConnectionSwitching || state.Update.Status != "pending" {
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
	if err != nil || state.ConnectionSwitching != nil || state.Update.Status != "applied" {
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
	if err != nil || state.ConnectionSwitching != nil || state.Update.Status != "pending" {
		t.Fatalf("legacy model/thinking update: %+v %v", state, err)
	}
}

func TestInferenceCapabilityFailureIsRetryable(t *testing.T) {
	store, path := inferenceStore(t)
	binary := setInferencePi(t, true)
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho private-probe-error >&2\nexit 78\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	model := "provider/new"
	req := InferenceUpdateRequest{
		RequestID: "connection", Model: &model, Connection: testInferenceConnection(),
		ModelDefinition: json.RawMessage(`{"id":"new","name":"New","api":"openai-responses","reasoning":true,"input":["text"],"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0},"contextWindow":128000,"maxTokens":8192}`),
	}
	before, _ := os.ReadFile(path)
	mux := http.NewServeMux()
	RegisterRoutes(mux, store, NewBearerAuthorizer(store, "operator-token"), RuntimeIdentity{})
	for _, method := range []string{"GET", "PUT"} {
		data, _ := json.Marshal(req)
		request := httptest.NewRequest(method, "/api/sessions/session/inference", bytes.NewReader(data))
		request.Header.Set("Authorization", "Bearer operator-token")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "private-probe-error") {
			t.Fatalf("failed probe must be retryable: %d %s", response.Code, response.Body.String())
		}
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("failed probe consumed or rejected the change request")
	}
	setInferencePi(t, true)
	state, err := store.UpdateInference("session", req)
	if err != nil || state.Update.Status != "pending" || state.Revision != 1 {
		t.Fatalf("probe recovery did not accept the same request: %+v %v", state, err)
	}
}

func TestInferenceReplayAndPlainSettingsDoNotProbe(t *testing.T) {
	for _, connection := range []bool{false, true} {
		t.Run(fmt.Sprintf("connection=%t", connection), func(t *testing.T) {
			store, path := inferenceStore(t)
			binary := setInferencePi(t, true)
			model, thinking := "provider/new", "high"
			req := InferenceUpdateRequest{RequestID: "initial", Model: &model}
			if connection {
				req.Connection = testInferenceConnection()
				req.ModelDefinition = json.RawMessage(`{"id":"new","name":"New","api":"openai-responses","reasoning":true,"input":["text"],"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0},"contextWindow":128000,"maxTokens":8192}`)
			}
			if _, err := store.UpdateInference("session", req); err != nil {
				t.Fatal(err)
			}
			probed := filepath.Join(t.TempDir(), "probed")
			failure := fmt.Sprintf("#!/bin/sh\necho checked > %q\nexit 1\n", probed)
			if err := os.WriteFile(binary, []byte(failure), 0o755); err != nil {
				t.Fatal(err)
			}
			for _, phase := range []string{"pending", "applied"} {
				if phase == "applied" {
					claimed, err := ClaimInferenceUpdate(path, "attempt", "receipt.json", InferenceSettings{})
					if err != nil {
						t.Fatal(err)
					}
					if err := FinishInferenceUpdate(path, req.RequestID, "applied", "", claimed.Settings); err != nil {
						t.Fatal(err)
					}
				}
				state, err := store.UpdateInference("session", req)
				if err != nil || state.Update.Status != phase || state.ConnectionSwitching != nil {
					t.Fatalf("probe outage broke %s replay: %+v %v", phase, state, err)
				}
				data, _ := json.Marshal(state)
				if bytes.Contains(data, []byte("connection_switching")) {
					t.Fatal("skipped check reported an unsupported capability")
				}
			}
			if _, err := os.Stat(probed); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("exact replay ran the capability probe: %v", err)
			}
			before, _ := os.ReadFile(path)
			state, err := store.UpdateInference("session", InferenceUpdateRequest{RequestID: "thinking", ExpectedRevision: 1, Thinking: &thinking})
			if connection {
				if !errors.Is(err, ErrInferenceUnavailable) {
					t.Fatalf("confirmed connection needs a successful check: %+v %v", state, err)
				}
				after, _ := os.ReadFile(path)
				if !bytes.Equal(before, after) {
					t.Fatal("retryable error changed confirmed settings or request state")
				}
			} else {
				if err != nil || state.Update.Status != "pending" || state.ConnectionSwitching != nil {
					t.Fatalf("plain settings unnecessarily required capability: %+v %v", state, err)
				}
				if _, err := os.Stat(probed); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("plain settings ran the capability probe: %v", err)
				}
			}
		})
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
	setInferencePi(t, false)
	store := NewFileStore(t.TempDir(), RuntimeLocal)
	path := filepath.Join(store.Root, "session", "session.json")
	m := &Manifest{SessionID: "session", SessionKind: KindController, Config: SessionConfig{Model: "provider/old", Thinking: "medium"}}
	if err := WriteManifest(path, m); err != nil {
		t.Fatal(err)
	}
	return store, path
}

func TestInferenceRejectsIncompatibleSessionWorker(t *testing.T) {
	for _, location := range []string{"runner", "open_epoch"} {
		t.Run(location, func(t *testing.T) {
			store, path := inferenceStore(t)
			if _, err := MutateManifest(path, func(m *Manifest) error {
				runner := &Runner{Kind: "local-subprocess", PID: 12345}
				if location == "runner" {
					m.Runner = runner
				} else {
					m.Epochs = []Epoch{{ID: 1, Runner: runner}}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			thinking := "high"
			if _, err := store.UpdateInference("session", InferenceUpdateRequest{RequestID: "change", Thinking: &thinking}); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "restart it with the updated telosd") {
				t.Fatalf("old worker accepted a change: %v", err)
			}
			m, err := ReadManifest(path)
			if err != nil || m.InferenceUpdate != nil || m.Config.Thinking != "medium" {
				t.Fatalf("rejection changed saved state: %+v %v", m, err)
			}
		})
	}
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

func TestInferenceCancellationFencesDelayedSubmission(t *testing.T) {
	for _, queued := range []bool{false, true} {
		t.Run(fmt.Sprint(queued), func(t *testing.T) {
			store, path := inferenceStore(t)
			model := "provider/new"
			req := InferenceUpdateRequest{RequestID: "cancel-me", Model: &model}
			if queued {
				if _, err := store.UpdateInference("session", req); err != nil {
					t.Fatal(err)
				}
			}
			cancelled, err := store.CancelInference("session", req)
			if err != nil || cancelled.Revision != 1 || cancelled.Update.Status != "rejected" || !strings.Contains(cancelled.Update.Error, "cancelled") || cancelled.Settings.Model != "provider/old" {
				t.Fatalf("cancellation = %+v, %v", cancelled, err)
			}
			// A process restart and a late PUT cannot reactivate the cancelled work.
			store = NewFileStore(store.Root, RuntimeLocal)
			late, err := store.UpdateInference("session", req)
			if err != nil || late.Update.Status != "rejected" || late.Revision != 1 {
				t.Fatalf("late request escaped cancellation: %+v, %v", late, err)
			}
			if claimed, err := ClaimInferenceUpdate(path, "attempt", "receipt.json", InferenceSettings{}); err != nil || claimed != nil {
				t.Fatalf("cancelled request was claimed: %+v, %v", claimed, err)
			}
			if again, err := store.CancelInference("session", req); err != nil || again.Revision != 1 {
				t.Fatalf("cancel replay = %+v, %v", again, err)
			}
			req.RequestID, req.ExpectedRevision = "next", 1
			if next, err := store.UpdateInference("session", req); err != nil || next.Update.Status != "pending" || next.Revision != 2 {
				t.Fatalf("cancelled request blocked next change: %+v, %v", next, err)
			}
		})
	}
}

func TestInferenceCancellationDoesNotOverrideStartedOrDifferentRequest(t *testing.T) {
	for _, status := range []string{"applying", "applied", "unknown", "different_id", "different_settings", "different_revision"} {
		t.Run(status, func(t *testing.T) {
			store, path := inferenceStore(t)
			level := "high"
			req := InferenceUpdateRequest{RequestID: "change", Thinking: &level}
			if _, err := store.UpdateInference("session", req); err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(status, "different") {
				switch status {
				case "different_id":
					req.RequestID, req.ExpectedRevision = "other", 1
				case "different_settings":
					other := "low"
					req.Thinking = &other
				case "different_revision":
					req.ExpectedRevision = 1
				}
			} else if _, err := MutateManifest(path, func(m *Manifest) error {
				m.InferenceUpdate.Status = status
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.CancelInference("session", req); !errors.Is(err, ErrConflict) {
				t.Fatalf("unsafe cancel succeeded: %v", err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("failed cancellation changed saved state: %v", err)
			}
		})
	}
}

func TestInferenceCancellationSerializesWithTurnStartup(t *testing.T) {
	for range 20 {
		store, path := inferenceStore(t)
		level := "high"
		req := InferenceUpdateRequest{RequestID: "change", Thinking: &level}
		if _, err := store.UpdateInference("session", req); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		var claimed *InferenceUpdate
		var claimErr, cancelErr error
		wg.Go(func() {
			<-start
			claimed, claimErr = ClaimInferenceUpdate(path, "attempt", "receipt.json", InferenceSettings{})
		})
		wg.Go(func() {
			<-start
			_, cancelErr = store.CancelInference("session", req)
		})
		close(start)
		wg.Wait()
		if claimErr != nil {
			t.Fatal(claimErr)
		}
		state, err := store.Inference("session")
		if err != nil {
			t.Fatal(err)
		}
		if claimed != nil {
			if !errors.Is(cancelErr, ErrConflict) || state.Update.Status != "applying" {
				t.Fatalf("cancelled after startup claim: %+v %v", state.Update, cancelErr)
			}
		} else if cancelErr != nil || state.Update.Status != "rejected" {
			t.Fatalf("cancellation did not fence startup: %+v %v", state.Update, cancelErr)
		}
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
	for _, token := range []string{"", "agent-token", "operator-token"} {
		request := httptest.NewRequest("POST", "/api/sessions/session/inference/cancel", bytes.NewBufferString(`{"request_id":"x","thinking":"high","model":"p/m"}`))
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		want := map[string]int{"": 401, "agent-token": 403, "operator-token": 200}[token]
		if response.Code != want {
			t.Fatalf("cancel auth %q: %d %s", token, response.Code, response.Body.String())
		}
	}
}

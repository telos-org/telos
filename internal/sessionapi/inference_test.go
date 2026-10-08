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

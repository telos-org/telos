package sessionapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestInferenceReceiptsSurviveNewerCommandsAndRestart(t *testing.T) {
	store, path := inferenceStore(t)
	model := "provider/new"
	req := InferenceUpdateRequest{RequestID: "first", Model: &model, ModelDefinition: json.RawMessage(`{"id":"new","api":"openai-responses"}`)}
	if _, err := store.UpdateInference("session", req); err != nil {
		t.Fatal(err)
	}
	if _, err := ClaimInferenceUpdate(path, "attempt", "receipt.json", InferenceSettings{}); err != nil {
		t.Fatal(err)
	}
	wanted := InferenceSettings{Model: model, Thinking: "low"}
	if err := FinishInferenceUpdate(path, req.RequestID, "applied", "", &wanted); err != nil {
		t.Fatal(err)
	}
	first, err := store.InferenceRequest("session", req.RequestID)
	if err != nil || !first.OperationReceipts || first.Operation.Settings != wanted {
		t.Fatalf("first operation: %+v %v", first, err)
	}
	level := "high"
	if _, err := store.UpdateInference("session", InferenceUpdateRequest{RequestID: "second", ExpectedRevision: 1, Thinking: &level}); err != nil {
		t.Fatal(err)
	}
	if _, err := ClaimInferenceUpdate(path, "attempt2", "receipt2.json", InferenceSettings{}); err != nil {
		t.Fatal(err)
	}
	if err := FinishInferenceUpdate(path, "second", "applied", "", &InferenceSettings{Model: model, Thinking: level}); err != nil {
		t.Fatal(err)
	}
	// A historical reply remains available even when current admission is closed.
	if _, err := MutateManifest(path, func(m *Manifest) error {
		m.DesiredStatus, m.Runner = DesiredStatusStopped, &Runner{}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	store = NewFileStore(store.Root, RuntimeLocal)
	req.ModelDefinition = json.RawMessage(`{ "api": "openai-responses", "id": "new" }`)
	for _, lookup := range []func() (*InferenceResponse, error){
		func() (*InferenceResponse, error) { return store.InferenceRequest("session", req.RequestID) },
		func() (*InferenceResponse, error) { return store.UpdateInference("session", req) },
		func() (*InferenceResponse, error) { return store.CancelInference("session", req) },
	} {
		result, err := lookup()
		if err != nil || !reflect.DeepEqual(result.Operation, first.Operation) || result.Revision != 2 || result.Settings.Thinking != level {
			t.Fatalf("historical result changed: %+v %v", result, err)
		}
	}
	req.ExpectedRevision = 1
	if _, err := store.UpdateInference("session", req); !errors.Is(err, ErrConflict) {
		t.Fatalf("historical ID allowed another payload: %v", err)
	}
	m, err := ReadManifest(path)
	if err != nil || len(m.InferenceReceipts) != 2 {
		t.Fatalf("receipts not retained: %+v %v", m, err)
	}
	data, err := json.Marshal(m.InferenceReceipts)
	if err != nil || strings.Contains(string(data), "openai-responses") {
		t.Fatalf("receipt duplicated the model definition: %s %v", data, err)
	}
}

func TestInferenceTerminalReceiptsRetainCommittedDefaults(t *testing.T) {
	for _, outcome := range []string{"rejected", "unknown", "recover", "stop_pending", "stop_applying"} {
		t.Run(outcome, func(t *testing.T) {
			store, path := inferenceStore(t)
			model := "provider/new"
			req := InferenceUpdateRequest{RequestID: "first", Model: &model}
			if _, err := store.UpdateInference("session", req); err != nil {
				t.Fatal(err)
			}
			if outcome != "stop_pending" {
				if _, err := ClaimInferenceUpdate(path, "attempt", "receipt.json", InferenceSettings{}); err != nil {
					t.Fatal(err)
				}
			}
			var err error
			switch outcome {
			case "recover":
				err = RecoverInferenceUpdate(path)
			case "stop_pending", "stop_applying":
				err = SettleInferenceUpdate(path, "worker ended")
			default:
				err = FinishInferenceUpdate(path, req.RequestID, outcome, "startup failed", nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			result, err := store.InferenceRequest("session", req.RequestID)
			if err != nil || result.Operation.Settings != (InferenceSettings{Model: "provider/old", Thinking: "medium"}) {
				t.Fatalf("unconfirmed defaults were promoted: %+v %v", result, err)
			}
			m, err := ReadManifest(path)
			if err != nil || m.InferenceReceipts[req.RequestID].Settings != result.Operation.Settings {
				t.Fatalf("terminal result not persisted: %+v %v", m, err)
			}
		})
	}
}

func TestInferenceAdmissionRejectionHasDurableReceipt(t *testing.T) {
	store, _ := inferenceStore(t)
	level := "high"
	first := InferenceUpdateRequest{RequestID: "first", Thinking: &level}
	second := InferenceUpdateRequest{RequestID: "second", ExpectedRevision: 1, Thinking: &level}
	if _, err := store.UpdateInference("session", first); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateInference("session", second); !errors.Is(err, ErrConflict) {
		t.Fatalf("busy runtime accepted another command: %v", err)
	}
	store = NewFileStore(store.Root, RuntimeLocal)
	result, err := store.InferenceRequest("session", second.RequestID)
	if err != nil || result.Operation.Status != "rejected" || result.Update.RequestID != first.RequestID {
		t.Fatalf("rejection lost or replaced current command: %+v %v", result, err)
	}
	replayed, err := store.UpdateInference("session", second)
	if err != nil || !reflect.DeepEqual(replayed.Operation, result.Operation) {
		t.Fatalf("lost rejection reply cannot replay: %+v %v", replayed, err)
	}
}

func TestInferenceCancellationTombstonePreservesOtherCommand(t *testing.T) {
	for _, revision := range []int{0, 1} {
		store, path := inferenceStore(t)
		level := "high"
		if _, err := store.UpdateInference("session", InferenceUpdateRequest{RequestID: "active", Thinking: &level}); err != nil {
			t.Fatal(err)
		}
		req := InferenceUpdateRequest{RequestID: "delayed", ExpectedRevision: revision, Thinking: &level}
		before, err := store.Inference("session")
		if err != nil {
			t.Fatal(err)
		}
		cancelled, err := store.CancelInference("session", req)
		if err != nil || !reflect.DeepEqual(cancelled.Update, before.Update) || cancelled.Revision != before.Revision {
			t.Fatalf("cancellation replaced unrelated work: %+v %v", cancelled, err)
		}
		wantStatus := "rejected"
		if revision == 0 {
			wantStatus = "unknown"
		}
		if cancelled.Operation.Status != wantStatus {
			t.Fatalf("missing prior history claimed certainty: %+v", cancelled.Operation)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, replay := range []func(string, InferenceUpdateRequest) (*InferenceResponse, error){store.CancelInference, store.UpdateInference} {
			result, err := replay("session", req)
			if err != nil || !reflect.DeepEqual(result.Operation, cancelled.Operation) {
				t.Fatalf("tombstone replay failed: %+v %v", result, err)
			}
		}
		after, err := os.ReadFile(path)
		if err != nil || string(data) != string(after) {
			t.Fatalf("replay changed durable state: %v", err)
		}
	}
}

func TestInferenceRequestLookupRequiresAuthorizationAndDistinguishesAbsence(t *testing.T) {
	store, _ := inferenceStore(t)
	mux := http.NewServeMux()
	RegisterRoutes(mux, store, NewBearerAuthorizer(store, "operator-token"), RuntimeIdentity{})
	for _, token := range []string{"", "operator-token"} {
		req := httptest.NewRequest("GET", "/api/sessions/session/inference/requests/missing", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, req)
		if token == "" {
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("unauthorized lookup: %d %s", response.Code, response.Body)
			}
		} else if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), "inference request not found") {
			t.Fatalf("missing receipt was not distinct: %d %s", response.Code, response.Body)
		}
	}
}

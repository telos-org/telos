package sessionapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
)

func retirementStore(t *testing.T) (*FileStore, string) {
	t.Helper()
	store, path := inferenceStore(t)
	if _, err := MutateManifest(path, func(m *Manifest) error {
		m.Runner = &Runner{InferenceUpdates: true, InferenceRetirement: true, InferenceReceipts: true}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return store, path
}

func TestRetirementFencesCachedCreationAndSurvivesRestart(t *testing.T) {
	store, _ := retirementStore(t)
	connection := testInferenceConnection()
	childPath := store.manifestPath("delayed-child")
	if err := os.MkdirAll(filepath.Dir(childPath), 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := store.RetireInferenceConnection("session", connection.ID)
	if err != nil || !result.Retired || result.ConnectionID != connection.ID {
		t.Fatalf("retire: %+v %v", result, err)
	}
	restarted := NewFileStore(store.Root, RuntimeCloud)
	if _, err := restarted.RetireInferenceConnection("session", connection.ID); err != nil {
		t.Fatal(err)
	}
	if err := writeInferenceManifest(childPath, &Manifest{SessionID: "delayed-child", SessionKind: KindTask, InferenceConnection: connection}); !errors.Is(err, ErrConflict) {
		t.Fatalf("cached child connection revived: %v", err)
	}
	if _, err := os.Stat(childPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("published rejected child: %v", err)
	}
	// A fresh ID may reuse the same proxy slot after the old identity is fenced.
	fresh := *connection
	fresh.ID = "next-generation"
	if err := writeInferenceManifest(childPath, &Manifest{SessionID: "delayed-child", SessionKind: KindTask, InferenceConnection: &fresh}); err != nil {
		t.Fatal(err)
	}
}

func TestRetirementPreservesRootChildrenAndUncertainHandoffs(t *testing.T) {
	for _, usage := range []string{"root", "child", "pending", "applying", "unknown"} {
		t.Run(usage, func(t *testing.T) {
			store, path := retirementStore(t)
			connection := testInferenceConnection()
			if usage == "child" {
				parent := "session"
				path = store.manifestPath("child")
				if err := WriteManifest(path, &Manifest{SessionID: "child", SessionKind: KindTask, ParentSessionID: &parent, InferenceConnection: connection}); err != nil {
					t.Fatal(err)
				}
			} else if _, err := MutateManifest(path, func(m *Manifest) error {
				if usage == "root" {
					m.InferenceConnection = connection
				} else {
					m.InferenceUpdate = &InferenceUpdate{Status: usage, InferenceUpdateRequest: InferenceUpdateRequest{Connection: connection}}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.RetireInferenceConnection("session", connection.ID); !errors.Is(err, ErrConflict) {
				t.Fatalf("retired %s use: %v", usage, err)
			}
			if _, err := MutateManifest(path, func(m *Manifest) error { m.InferenceConnection = nil; m.InferenceUpdate = nil; return nil }); err != nil {
				t.Fatal(err)
			}
			if _, err := store.RetireInferenceConnection("session", connection.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRetirementWaitsForStoppedWorkerAndFencesItsRestart(t *testing.T) {
	store, _ := retirementStore(t)
	connection := testInferenceConnection()
	path := store.manifestPath("stopped-child")
	child := &Manifest{SessionID: "stopped-child", SessionKind: KindTask, DesiredStatus: DesiredStatusStopped, InferenceConnection: connection}
	if err := WriteManifest(path, child); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(filepath.Join(filepath.Dir(path), "runner.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetireInferenceConnection("session", connection.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("retired draining worker: %v", err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetireInferenceConnection("session", connection.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := MutateInferenceManifest(path, func(m *Manifest) error { m.DesiredStatus = DesiredStatusRunning; return nil }); !errors.Is(err, ErrConflict) {
		t.Fatalf("revived retired worker: %v", err)
	}
}

func TestConcurrentRetirementAndPublicationHaveOneWinner(t *testing.T) {
	for i := 0; i < 30; i++ {
		store, _ := retirementStore(t)
		connection := testInferenceConnection()
		var createErr, retireErr error
		var wg sync.WaitGroup
		wg.Add(2)
		start := make(chan struct{})
		go func() {
			defer wg.Done()
			<-start
			createErr = writeInferenceManifest(store.manifestPath("child"), &Manifest{SessionID: "child", SessionKind: KindTask, InferenceConnection: connection})
		}()
		go func() {
			defer wg.Done()
			<-start
			_, retireErr = store.RetireInferenceConnection("session", connection.ID)
		}()
		close(start)
		wg.Wait()
		if (createErr == nil) == (retireErr == nil) {
			t.Fatalf("publication=%v retirement=%v", createErr, retireErr)
		}
		if createErr != nil && !errors.Is(createErr, ErrConflict) {
			t.Fatal(createErr)
		}
		if retireErr != nil && !errors.Is(retireErr, ErrConflict) {
			t.Fatal(retireErr)
		}
	}
}

func TestRetirementRejectsDelayedInferenceAndUnreadableManifests(t *testing.T) {
	store, _ := retirementStore(t)
	setInferencePi(t, true)
	connection := testInferenceConnection()
	path := store.manifestPath("unreadable")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetireInferenceConnection("session", connection.ID); err == nil {
		t.Fatal("unreadable manifest treated as unused")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetireInferenceConnection("session", connection.ID); err != nil {
		t.Fatal(err)
	}
	model := "provider/new"
	req := InferenceUpdateRequest{RequestID: "delayed", Model: &model, Connection: connection, ModelDefinition: []byte(`{"id":"new","name":"New","api":"openai-responses","reasoning":true,"input":["text"],"cost":{},"contextWindow":128000,"maxTokens":8192}`)}
	if _, err := store.UpdateInference("session", req); !errors.Is(err, ErrConflict) {
		t.Fatalf("delayed PUT revived retired connection: %v", err)
	}
}

func TestRetirementRouteRequiresOperator(t *testing.T) {
	store, _ := retirementStore(t)
	mux := http.NewServeMux()
	RegisterRoutes(mux, store, NewBearerAuthorizer(store, "operator"), RuntimeIdentity{})
	request := httptest.NewRequest("POST", "/api/sessions/session/inference/connections/unused/retire", nil)
	reply := httptest.NewRecorder()
	mux.ServeHTTP(reply, request)
	if reply.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized retirement: %d %s", reply.Code, reply.Body.String())
	}
	scoped, err := NewScopedToken("session", KindController)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MutateManifest(store.manifestPath("session"), func(m *Manifest) error { m.Access = scoped; return nil }); err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+scoped.APIToken)
	reply = httptest.NewRecorder()
	mux.ServeHTTP(reply, request)
	if reply.Code != http.StatusForbidden {
		t.Fatalf("agent retired connection: %d %s", reply.Code, reply.Body.String())
	}
	request.Header.Set("Authorization", "Bearer operator")
	reply = httptest.NewRecorder()
	mux.ServeHTTP(reply, request)
	if reply.Code != http.StatusOK {
		t.Fatalf("authorized retirement: %d %s", reply.Code, reply.Body.String())
	}
}

func TestOlderWorkerDoesNotAdvertiseOrAcknowledgeRetirement(t *testing.T) {
	store, path := retirementStore(t)
	if _, err := MutateManifest(path, func(m *Manifest) error { m.Runner.InferenceRetirement = false; return nil }); err != nil {
		t.Fatal(err)
	}
	state, err := store.Inference("session")
	if err != nil || state.ConnectionRetirement {
		t.Fatalf("old worker advertised retirement: %+v %v", state, err)
	}
	if _, err := store.RetireInferenceConnection("session", "unused"); !errors.Is(err, ErrInferenceUnavailable) {
		t.Fatalf("old worker retirement acknowledged: %v", err)
	}
}

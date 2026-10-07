package telosd

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/telos-org/telos/internal/sessionapi"
	"github.com/telos-org/telos/internal/sessionworker"
)

func TestInferenceNotificationsRecoverAfterServerRestart(t *testing.T) {
	for _, mode := range []Mode{ModeLocal, ModeCloud} {
		for _, phase := range []string{"controller", "child-task", "finished-task", "stopped", "applied"} {
			t.Run(string(mode)+"/"+phase, func(t *testing.T) {
				cfg := Config{Mode: mode, Root: t.TempDir()}
				store := storeForConfig(cfg)
				if mode == ModeCloud {
					_ = newControllerReconciler(store, newLocalProcessSubstrate(), nil, cloudControllerDefaults())
				}
				dir := filepath.Join(store.Root, "session")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				model, parent := "provider/new", "parent"
				request := sessionapi.InferenceUpdateRequest{RequestID: "saved-before-crash", Model: &model}
				m := &sessionapi.Manifest{SessionID: "session", SessionKind: sessionapi.KindController, InferenceUpdate: &sessionapi.InferenceUpdate{InferenceUpdateRequest: request, Revision: 1, Status: "pending"}}
				switch phase {
				case "child-task":
					m.SessionKind, m.ParentSessionID = sessionapi.KindTask, &parent
				case "finished-task":
					finished := "2026-10-07T12:00:00Z"
					m.SessionKind = sessionapi.KindTask
					m.Epochs = []sessionapi.Epoch{{ID: 1, FinishedAt: &finished}}
				case "stopped":
					m.DesiredStatus = sessionapi.DesiredStatusStopped
				case "applied":
					m.InferenceUpdate.Status = "applied"
				}
				if err := sessionapi.WriteManifest(filepath.Join(dir, "session.json"), m); err != nil {
					t.Fatal(err)
				}
				owner, err := sessionworker.AcquireOwnership(dir, "")
				if err != nil {
					t.Fatal(err)
				}
				defer owner.Release()
				if err := recoverInferenceNotifications(store); err != nil {
					t.Fatal(err)
				}
				pending := phase == "controller" || phase == "child-task"
				select {
				case <-owner.Inference:
					if !pending {
						t.Fatal("recovered a terminal change or session")
					}
				case <-time.After(100 * time.Millisecond):
					if pending {
						t.Fatal("lost saved request on server restart")
					}
				}
				if pending {
					// Exercise the same hook used by the local and Cloud PUT routes.
					if _, err := store.UpdateInference("session", request); err != nil {
						t.Fatal(err)
					}
					select {
					case <-owner.Inference:
					case <-time.After(time.Second):
						t.Fatal("API replay failed to notify")
					}
				}
			})
		}
	}
}

func TestWithCloudCORSAllowsControlPlaneOrigins(t *testing.T) {
	handler := withCloudCORS(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	req.Header.Set("Origin", "https://app.usetelos.ai")
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status: got %d", res.Code)
	}
	if got := res.Header().Get("Access-Control-Allow-Origin"); got != "https://app.usetelos.ai" {
		t.Fatalf("allow origin: got %q", got)
	}
	if got := res.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("allow credentials: got %q", got)
	}
	if got := res.Header().Get("Vary"); got != "Origin" {
		t.Fatalf("vary: got %q", got)
	}
}

func TestWithCloudCORSAllowsLocalhostOrigins(t *testing.T) {
	handler := withCloudCORS(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, req)

	if got := res.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Fatalf("allow origin: got %q", got)
	}
}

func TestWithCloudCORSRejectsUnknownOrigins(t *testing.T) {
	handler := withCloudCORS(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	req.Header.Set("Origin", "https://example.com")
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, req)

	if got := res.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("allow origin: got %q", got)
	}
}

func TestWithCloudCORSHandlesPreflight(t *testing.T) {
	handler := withCloudCORS(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})

	req := httptest.NewRequest(http.MethodOptions, "/api/sessions", nil)
	req.Header.Set("Origin", "https://env-test.usetelos.ai")
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, req)

	if res.Code != http.StatusNoContent {
		t.Fatalf("status: got %d", res.Code)
	}
	if got := res.Header().Get("Access-Control-Allow-Methods"); got != "GET, POST, PUT, OPTIONS" {
		t.Fatalf("allow methods: got %q", got)
	}
}

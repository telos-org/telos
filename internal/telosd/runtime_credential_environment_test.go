package telosd

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/runtimeenv"
	"github.com/telos-org/telos/internal/sessionapi"
	"github.com/telos-org/telos/internal/sessionworker"
)

func TestRuntimeCredentialEnvironmentRequiresOperator(t *testing.T) {
	mux, sessionsRoot := runtimeCredentialEnvironmentTestMux(t, "operator-token")

	request := httptest.NewRequest(http.MethodGet, "/api/runtime/credential-environment", nil)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("missing auth: got %d want 401", response.Code)
	}

	for _, test := range []struct {
		name string
		kind sessionapi.SessionKind
	}{
		{name: "controller", kind: sessionapi.KindController},
		{name: "task", kind: sessionapi.KindTask},
	} {
		t.Run(test.name, func(t *testing.T) {
			token := writeRuntimeCredentialEnvironmentSessionToken(t, sessionsRoot, "sess-"+test.name, test.kind)
			request := httptest.NewRequest(http.MethodGet, "/api/runtime/credential-environment", nil)
			request.Header.Set("Authorization", "Bearer "+token)
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden {
				t.Fatalf("scoped auth: got %d want 403", response.Code)
			}
		})
	}

	request = httptest.NewRequest(http.MethodGet, "/api/runtime/credential-environment", nil)
	request.Header.Set("Authorization", "Bearer operator-token")
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("operator auth: got %d want 200: %s", response.Code, response.Body.String())
	}
	var status runtimeenv.Status
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if !status.Supported || status.Seeded || status.Generation != 0 || status.Count != 0 {
		t.Fatalf("unexpected status: %#v", status)
	}
	if status.EnvironmentSHA256 != runtimeenv.EnvironmentSHA256(map[string]string{}) {
		t.Fatalf("unexpected empty digest: %q", status.EnvironmentSHA256)
	}
}

func TestInitializeRuntimeCredentialEnvironmentSeedsGenerationZero(t *testing.T) {
	root := t.TempDir()
	store, err := initializeRuntimeCredentialEnvironment(root)
	if err != nil {
		t.Fatal(err)
	}
	status, err := store.Get()
	if err != nil {
		t.Fatal(err)
	}
	if !status.Supported || !status.Seeded || status.Generation != 0 || status.Count != 0 {
		t.Fatalf("unexpected initialized status: %#v", status)
	}
	if status.EnvironmentSHA256 != runtimeenv.EnvironmentSHA256(map[string]string{}) {
		t.Fatalf("unexpected initialized digest: %q", status.EnvironmentSHA256)
	}
	if _, err := os.Stat(runtimeenv.Path(root)); err != nil {
		t.Fatalf("initialized state file: %v", err)
	}
}

func TestRuntimeCredentialEnvironmentPutAndStatusDoNotExposeValues(t *testing.T) {
	mux, _ := runtimeCredentialEnvironmentTestMux(t, "operator-token")
	payload := `{"generation":7,"environment":{"MODAL_TOKEN_ID":"opaque-value"}}`

	response := serveRuntimeCredentialEnvironmentRequest(t, mux, http.MethodPut, payload)
	if response.Code != http.StatusOK {
		t.Fatalf("put: got %d want 200: %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "MODAL") || strings.Contains(response.Body.String(), "opaque") {
		t.Fatalf("put response exposed environment data: %s", response.Body.String())
	}
	var status runtimeenv.Status
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if !status.Seeded || status.Generation != 7 || status.Count != 1 {
		t.Fatalf("unexpected status: %#v", status)
	}

	response = serveRuntimeCredentialEnvironmentRequest(t, mux, http.MethodGet, "")
	if response.Code != http.StatusOK {
		t.Fatalf("get: got %d want 200: %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "MODAL") || strings.Contains(response.Body.String(), "opaque") {
		t.Fatalf("get response exposed environment data: %s", response.Body.String())
	}
}

func TestRuntimeCredentialEnvironmentPutValidationAndConflicts(t *testing.T) {
	mux, _ := runtimeCredentialEnvironmentTestMux(t, "operator-token")
	tests := []struct {
		name       string
		payload    string
		wantStatus int
	}{
		{name: "unknown field", payload: `{"generation":1,"environment":{},"extra":true}`, wantStatus: http.StatusBadRequest},
		{name: "reserved name", payload: `{"generation":1,"environment":{"telos_api_token":"opaque"}}`, wantStatus: http.StatusBadRequest},
		{name: "generation zero values", payload: `{"generation":0,"environment":{"TOKEN":"opaque"}}`, wantStatus: http.StatusBadRequest},
		{name: "generation zero empty", payload: `{"generation":0,"environment":{}}`, wantStatus: http.StatusOK},
		{name: "initial", payload: `{"generation":2,"environment":{"TOKEN":"one"}}`, wantStatus: http.StatusOK},
		{name: "idempotent", payload: `{"generation":2,"environment":{"TOKEN":"one"}}`, wantStatus: http.StatusOK},
		{name: "same generation differs", payload: `{"generation":2,"environment":{"TOKEN":"two"}}`, wantStatus: http.StatusConflict},
		{name: "stale", payload: `{"generation":1,"environment":{"TOKEN":"one"}}`, wantStatus: http.StatusConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := serveRuntimeCredentialEnvironmentRequest(t, mux, http.MethodPut, test.payload)
			if response.Code != test.wantStatus {
				t.Fatalf("status: got %d want %d: %s", response.Code, test.wantStatus, response.Body.String())
			}
		})
	}
}

func TestRuntimeCredentialEnvironmentCorruptStateReturnsServerError(t *testing.T) {
	root := t.TempDir()
	path := runtimeenv.Path(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	sessions := sessionapi.NewFileStore(filepath.Join(root, "sessions"), sessionapi.RuntimeCloud)
	mux := http.NewServeMux()
	registerRuntimeCredentialEnvironmentRoutes(
		mux,
		runtimeenv.NewStore(path),
		sessionapi.NewBearerAuthorizer(sessions, "operator-token"),
		func() (bool, error) {
			return runtimeCredentialEnvironmentWorkersSupported(sessions)
		},
	)

	response := serveRuntimeCredentialEnvironmentRequest(t, mux, http.MethodGet, "")
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d want 500: %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "not-json") || strings.Contains(response.Body.String(), path) {
		t.Fatalf("error response exposed state contents or path: %s", response.Body.String())
	}
}

func TestRuntimeCredentialEnvironmentRejectsLiveLegacyWorkerWithoutMutation(t *testing.T) {
	root := t.TempDir()
	sessions := sessionapi.NewFileStore(filepath.Join(root, "sessions"), sessionapi.RuntimeCloud)
	stateStore, err := initializeRuntimeCredentialEnvironment(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(runtimeenv.PathEnvironmentVariable, "")
	owner := acquireRuntimeCredentialEnvironmentTestWorker(t, sessions.Root, "sess-legacy", sessionapi.KindController)
	defer owner.Release()

	mux := runtimeCredentialEnvironmentMux(stateStore, sessions, "operator-token")
	response := serveRuntimeCredentialEnvironmentRequest(t, mux, http.MethodGet, "")
	if response.Code != http.StatusOK {
		t.Fatalf("get: got %d want 200: %s", response.Code, response.Body.String())
	}
	var status runtimeenv.Status
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status.Supported {
		t.Fatalf("legacy live worker reported supported: %#v", status)
	}

	response = serveRuntimeCredentialEnvironmentRequest(
		t,
		mux,
		http.MethodPut,
		`{"generation":1,"environment":{"TOKEN":"opaque"}}`,
	)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("put: got %d want 503: %s", response.Code, response.Body.String())
	}
	status, err = stateStore.Get()
	if err != nil {
		t.Fatal(err)
	}
	if status.Generation != 0 || status.Count != 0 {
		t.Fatalf("rejected update mutated state: %#v", status)
	}
}

func TestRuntimeCredentialEnvironmentAcceptsLiveCurrentWorker(t *testing.T) {
	root := t.TempDir()
	sessions := sessionapi.NewFileStore(filepath.Join(root, "sessions"), sessionapi.RuntimeCloud)
	stateStore, err := initializeRuntimeCredentialEnvironment(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(runtimeenv.PathEnvironmentVariable, runtimeenv.Path(root))
	owner := acquireRuntimeCredentialEnvironmentTestWorker(t, sessions.Root, "sess-current", sessionapi.KindController)
	defer owner.Release()

	mux := runtimeCredentialEnvironmentMux(stateStore, sessions, "operator-token")
	response := serveRuntimeCredentialEnvironmentRequest(t, mux, http.MethodGet, "")
	if response.Code != http.StatusOK {
		t.Fatalf("get: got %d want 200: %s", response.Code, response.Body.String())
	}
	var status runtimeenv.Status
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if !status.Supported {
		t.Fatalf("current live worker reported unsupported: %#v", status)
	}

	response = serveRuntimeCredentialEnvironmentRequest(
		t,
		mux,
		http.MethodPut,
		`{"generation":1,"environment":{"TOKEN":"opaque"}}`,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("put: got %d want 200: %s", response.Code, response.Body.String())
	}
}

func TestRuntimeCredentialEnvironmentRejectsLiveLegacyTaskWorker(t *testing.T) {
	root := t.TempDir()
	sessions := sessionapi.NewFileStore(filepath.Join(root, "sessions"), sessionapi.RuntimeCloud)
	t.Setenv(runtimeenv.PathEnvironmentVariable, "")
	owner := acquireRuntimeCredentialEnvironmentTestWorker(t, sessions.Root, "sess-task", sessionapi.KindTask)
	defer owner.Release()

	supported, err := runtimeCredentialEnvironmentWorkersSupported(sessions)
	if err != nil {
		t.Fatal(err)
	}
	if supported {
		t.Fatal("live legacy task worker reported supported")
	}
}

func TestRuntimeCredentialEnvironmentFailsClosedForUnreadableLiveWorker(t *testing.T) {
	root := t.TempDir()
	sessions := sessionapi.NewFileStore(filepath.Join(root, "sessions"), sessionapi.RuntimeCloud)
	stateStore, err := initializeRuntimeCredentialEnvironment(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(runtimeenv.PathEnvironmentVariable, runtimeenv.Path(root))
	owner := acquireRuntimeCredentialEnvironmentTestWorker(t, sessions.Root, "sess-unreadable", sessionapi.KindController)
	defer owner.Release()
	manifestPath := filepath.Join(sessions.Root, "sess-unreadable", "session.json")
	if err := os.WriteFile(manifestPath, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}

	mux := runtimeCredentialEnvironmentMux(stateStore, sessions, "operator-token")
	response := serveRuntimeCredentialEnvironmentRequest(t, mux, http.MethodGet, "")
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("get: got %d want 500: %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "not-json") || strings.Contains(response.Body.String(), manifestPath) {
		t.Fatalf("readiness error exposed manifest contents or path: %s", response.Body.String())
	}
	response = serveRuntimeCredentialEnvironmentRequest(
		t,
		mux,
		http.MethodPut,
		`{"generation":1,"environment":{"TOKEN":"opaque"}}`,
	)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("put: got %d want 500: %s", response.Code, response.Body.String())
	}
	status, err := stateStore.Get()
	if err != nil {
		t.Fatal(err)
	}
	if status.Generation != 0 || status.Count != 0 {
		t.Fatalf("failed readiness check mutated state: %#v", status)
	}
}

func TestRuntimeCredentialEnvironmentReturnsUnavailableDuringWorkerHandoff(t *testing.T) {
	root := t.TempDir()
	sessions := sessionapi.NewFileStore(filepath.Join(root, "sessions"), sessionapi.RuntimeCloud)
	stateStore, err := initializeRuntimeCredentialEnvironment(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(runtimeenv.PathEnvironmentVariable, runtimeenv.Path(root))
	owner := acquireRuntimeCredentialEnvironmentTestWorker(t, sessions.Root, "sess-handoff", sessionapi.KindController)
	defer owner.Release()
	manifestPath := filepath.Join(sessions.Root, "sess-handoff", "session.json")
	if _, err := sessionapi.MutateManifest(manifestPath, func(manifest *sessionapi.Manifest) error {
		manifest.Runner = nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	mux := runtimeCredentialEnvironmentMux(stateStore, sessions, "operator-token")
	response := serveRuntimeCredentialEnvironmentRequest(t, mux, http.MethodGet, "")
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("get: got %d want 503: %s", response.Code, response.Body.String())
	}
	response = serveRuntimeCredentialEnvironmentRequest(
		t,
		mux,
		http.MethodPut,
		`{"generation":1,"environment":{"TOKEN":"opaque"}}`,
	)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("put: got %d want 503: %s", response.Code, response.Body.String())
	}
	status, err := stateStore.Get()
	if err != nil {
		t.Fatal(err)
	}
	if status.Generation != 0 || status.Count != 0 {
		t.Fatalf("transient readiness mutated state: %#v", status)
	}
}

func runtimeCredentialEnvironmentTestMux(t *testing.T, operatorToken string) (*http.ServeMux, string) {
	t.Helper()
	root := t.TempDir()
	sessionsRoot := filepath.Join(root, "sessions")
	store := sessionapi.NewFileStore(sessionsRoot, sessionapi.RuntimeCloud)
	mux := http.NewServeMux()
	registerRuntimeCredentialEnvironmentRoutes(
		mux,
		runtimeenv.NewStore(runtimeenv.Path(root)),
		sessionapi.NewBearerAuthorizer(store, operatorToken),
		func() (bool, error) {
			return runtimeCredentialEnvironmentWorkersSupported(store)
		},
	)
	return mux, sessionsRoot
}

func runtimeCredentialEnvironmentMux(
	stateStore *runtimeenv.Store,
	sessions *sessionapi.FileStore,
	operatorToken string,
) *http.ServeMux {
	mux := http.NewServeMux()
	registerRuntimeCredentialEnvironmentRoutes(
		mux,
		stateStore,
		sessionapi.NewBearerAuthorizer(sessions, operatorToken),
		func() (bool, error) {
			return runtimeCredentialEnvironmentWorkersSupported(sessions)
		},
	)
	return mux
}

func acquireRuntimeCredentialEnvironmentTestWorker(
	t *testing.T,
	sessionsRoot string,
	sessionID string,
	kind sessionapi.SessionKind,
) *sessionworker.Ownership {
	t.Helper()
	manifest := sessionapi.ManifestFromInitial(sessionapi.InitialManifest{
		SessionID:   sessionID,
		SessionKind: kind,
	})
	sessionDir := filepath.Join(sessionsRoot, sessionID)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := sessionapi.WriteManifest(filepath.Join(sessionDir, "session.json"), &manifest); err != nil {
		t.Fatal(err)
	}
	owner, err := sessionworker.AcquireOwnership(sessionDir, "")
	if err != nil {
		t.Fatal(err)
	}
	return owner
}

func serveRuntimeCredentialEnvironmentRequest(
	t *testing.T,
	handler http.Handler,
	method string,
	payload string,
) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if payload != "" {
		body = bytes.NewBufferString(payload)
	}
	request := httptest.NewRequest(method, "/api/runtime/credential-environment", body)
	request.Header.Set("Authorization", "Bearer operator-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func writeRuntimeCredentialEnvironmentSessionToken(
	t *testing.T,
	root string,
	sessionID string,
	kind sessionapi.SessionKind,
) string {
	t.Helper()
	access, err := sessionapi.NewScopedToken(sessionID, kind)
	if err != nil {
		t.Fatal(err)
	}
	manifest := sessionapi.ManifestFromInitial(sessionapi.InitialManifest{
		SessionID:   sessionID,
		SessionKind: kind,
		Access:      access,
	})
	dir := filepath.Join(root, sessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := sessionapi.WriteManifest(filepath.Join(dir, "session.json"), &manifest); err != nil {
		t.Fatal(err)
	}
	return access.APIToken
}

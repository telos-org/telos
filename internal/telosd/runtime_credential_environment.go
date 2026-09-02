package telosd

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/telos-org/telos/internal/runtimeenv"
	"github.com/telos-org/telos/internal/sessionapi"
	"github.com/telos-org/telos/internal/sessionworker"
)

const maxRuntimeCredentialEnvironmentRequestBytes = 1 << 20

type runtimeCredentialEnvironmentHandler struct {
	store            *runtimeenv.Store
	authorizer       sessionapi.Authorizer
	workersSupported func() (bool, error)
}

type runtimeCredentialEnvironmentUpdateRequest struct {
	Generation  uint64            `json:"generation"`
	Environment map[string]string `json:"environment"`
}

func initializeRuntimeCredentialEnvironment(root string) (*runtimeenv.Store, error) {
	store := runtimeenv.NewStore(runtimeenv.Path(root))
	if _, err := store.Initialize(); err != nil {
		return nil, err
	}
	return store, nil
}

func registerRuntimeCredentialEnvironmentRoutes(
	mux *http.ServeMux,
	store *runtimeenv.Store,
	authorizer sessionapi.Authorizer,
	workersSupported func() (bool, error),
) {
	handler := &runtimeCredentialEnvironmentHandler{
		store:            store,
		authorizer:       authorizer,
		workersSupported: workersSupported,
	}
	mux.HandleFunc("GET /api/runtime/credential-environment", handler.get)
	mux.HandleFunc("PUT /api/runtime/credential-environment", handler.put)
}

func (h *runtimeCredentialEnvironmentHandler) get(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	status, err := h.store.Get()
	if err != nil {
		writeRuntimeCredentialEnvironmentError(
			w,
			http.StatusInternalServerError,
			"runtime credential environment state is unavailable",
		)
		return
	}
	status.Supported, err = h.workersSupported()
	if err != nil {
		writeRuntimeCredentialEnvironmentReadinessError(w, err)
		return
	}
	writeRuntimeCredentialEnvironmentJSON(w, http.StatusOK, status)
}

func (h *runtimeCredentialEnvironmentHandler) put(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	supported, err := h.workersSupported()
	if err != nil {
		writeRuntimeCredentialEnvironmentReadinessError(w, err)
		return
	}
	if !supported {
		writeRuntimeCredentialEnvironmentError(
			w,
			http.StatusServiceUnavailable,
			"runtime credential environment is not supported by every live session worker",
		)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRuntimeCredentialEnvironmentRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request runtimeCredentialEnvironmentUpdateRequest
	if err := decoder.Decode(&request); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeRuntimeCredentialEnvironmentError(w, http.StatusRequestEntityTooLarge, "request body is too large")
			return
		}
		writeRuntimeCredentialEnvironmentError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := requireRuntimeCredentialEnvironmentJSONEOF(decoder); err != nil {
		writeRuntimeCredentialEnvironmentError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	status, err := h.store.Put(request.Generation, request.Environment)
	if err != nil {
		switch {
		case errors.Is(err, runtimeenv.ErrInvalidEnvironment):
			writeRuntimeCredentialEnvironmentError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, runtimeenv.ErrStaleGeneration):
			writeRuntimeCredentialEnvironmentError(w, http.StatusConflict, "runtime credential environment generation is stale")
		case errors.Is(err, runtimeenv.ErrGenerationConflict):
			writeRuntimeCredentialEnvironmentError(w, http.StatusConflict, "runtime credential environment generation already has different contents")
		default:
			writeRuntimeCredentialEnvironmentError(
				w,
				http.StatusInternalServerError,
				"runtime credential environment state is unavailable",
			)
		}
		return
	}
	writeRuntimeCredentialEnvironmentJSON(w, http.StatusOK, status)
}

func runtimeCredentialEnvironmentWorkersSupported(store *sessionapi.FileStore) (bool, error) {
	// Include live one-shot task workers as well as durable roots: either can
	// launch another agent turn after a credential snapshot changes.
	entries, err := os.ReadDir(store.Root)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		sessionDir := filepath.Join(store.Root, entry.Name())
		live, supported, err := sessionworker.RuntimeCredentialEnvironmentCapabilityStatus(sessionDir)
		if err != nil {
			return false, err
		}
		if live && !supported {
			return false, nil
		}
	}
	return true, nil
}

func writeRuntimeCredentialEnvironmentReadinessError(w http.ResponseWriter, err error) {
	if errors.Is(err, sessionworker.ErrWorkerReadinessTransient) {
		writeRuntimeCredentialEnvironmentError(
			w,
			http.StatusServiceUnavailable,
			"runtime credential environment worker readiness is pending",
		)
		return
	}
	writeRuntimeCredentialEnvironmentError(
		w,
		http.StatusInternalServerError,
		"runtime credential environment worker readiness is unavailable",
	)
}

func (h *runtimeCredentialEnvironmentHandler) authorize(w http.ResponseWriter, r *http.Request) bool {
	_, err := h.authorizer.Caller(r, sessionapi.AccessRequest{
		Action: sessionapi.ActionManageRuntimeCredentialEnvironment,
	})
	if err == nil {
		return true
	}
	if status, detail, ok := sessionapi.AuthHTTPError(err); ok {
		writeRuntimeCredentialEnvironmentError(w, status, detail)
		return false
	}
	writeRuntimeCredentialEnvironmentError(w, http.StatusForbidden, err.Error())
	return false
}

func requireRuntimeCredentialEnvironmentJSONEOF(decoder *json.Decoder) error {
	var extra json.RawMessage
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("multiple JSON values")
	}
	return err
}

func writeRuntimeCredentialEnvironmentJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
}

func writeRuntimeCredentialEnvironmentError(w http.ResponseWriter, status int, detail string) {
	writeRuntimeCredentialEnvironmentJSON(w, status, map[string]string{"detail": detail})
}

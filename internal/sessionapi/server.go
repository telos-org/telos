package sessionapi

import (
	"encoding/json"
	"errors"
	"net/http"
)

// RegisterRoutes mounts the canonical Sessions API routes onto the given mux.
//
// Routes:
//
//	POST /api/sessions
//	GET  /api/sessions
//	GET  /api/sessions/{id}
//	POST /api/sessions/{id}/stop
//	GET  /api/sessions/{id}/transcript
//	GET  /api/sessions/{id}/events
//	GET  /api/sessions/{id}/workspace/{spec}
func RegisterRoutes(mux *http.ServeMux, store Store) {
	h := &handler{store: store}

	mux.HandleFunc("POST /api/sessions", h.createSession)
	mux.HandleFunc("GET /api/sessions", h.listSessions)
	mux.HandleFunc("GET /api/sessions/{id}", h.getSession)
	mux.HandleFunc("POST /api/sessions/{id}/stop", h.stopSession)
	mux.HandleFunc("GET /api/sessions/{id}/transcript", h.getTranscript)
	mux.HandleFunc("GET /api/sessions/{id}/events", h.getEvents)
	mux.HandleFunc("GET /api/sessions/{id}/workspace/{spec}", h.getWorkspace)
}

type handler struct {
	store Store
}

func (h *handler) createSession(w http.ResponseWriter, r *http.Request) {
	var req SessionCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	session, err := h.store.Create(req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, session)
}

func (h *handler) listSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := h.store.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, SessionListResponse{Sessions: sessions})
}

func (h *handler) getSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	session, err := h.store.Get(id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, session)
}

func (h *handler) stopSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	session, err := h.store.Stop(id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, session)
}

func (h *handler) getTranscript(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	text, err := h.store.Transcript(id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(text))
}

func (h *handler) getEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	events, err := h.store.Events(id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if events == nil {
		events = []SessionEvent{}
	}
	writeJSON(w, http.StatusOK, SessionEventsResponse{Events: events})
}

func (h *handler) getWorkspace(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	spec := r.PathValue("spec")
	path, err := h.store.WorkspacePath(id, spec)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	http.ServeFile(w, r, path)
}

// --------- JSON helpers ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, detail string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"detail": detail})
}

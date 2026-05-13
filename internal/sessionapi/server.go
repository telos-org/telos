package sessionapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const maxSessionRequestBytes = 4 << 20

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
//	GET  /api/healthz
func RegisterRoutes(mux *http.ServeMux, store Store) {
	h := &handler{store: store}

	mux.HandleFunc("GET /api/healthz", h.healthz)
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

func (h *handler) healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *handler) createSession(w http.ResponseWriter, r *http.Request) {
	var req SessionCreateRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxSessionRequestBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
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
	if strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/event-stream") {
		h.streamEvents(w, r, id)
		return
	}
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

func (h *handler) streamEvents(w http.ResponseWriter, r *http.Request, id string) {
	if _, err := h.store.Get(id); err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// File-backed evidence is append-only; sent indexes are stable for one stream.
	sent := 0
	emitAvailable := func() bool {
		events, err := h.store.Events(id)
		if err != nil {
			return false
		}
		for sent < len(events) {
			data, err := json.Marshal(events[sent])
			if err != nil {
				sent++
				continue
			}
			_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
			sent++
		}
		return true
	}
	poll := time.NewTicker(time.Second)
	defer poll.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		if !emitAvailable() {
			return
		}
		session, err := h.store.Get(id)
		if err == nil && session.Status.IsTerminal() {
			emitAvailable()
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			_, _ = fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		case <-poll.C:
		}
	}
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

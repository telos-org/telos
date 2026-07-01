package sessionapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
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
//	GET  /api/sessions/{id}/spec
//	PUT  /api/sessions/{name}/spec
//	POST /api/sessions/{id}/stop
//	GET  /api/sessions/{id}/transcript
//	GET  /api/sessions/{id}/events
//	GET  /api/healthz
func RegisterRoutes(mux *http.ServeMux, store Store, authorizer Authorizer) {
	if authorizer == nil {
		panic("sessionapi.RegisterRoutes requires an authorizer")
	}
	h := &handler{store: store, authorizer: authorizer}

	mux.HandleFunc("GET /api/healthz", h.healthz)
	mux.HandleFunc("POST /api/sessions", h.createSession)
	mux.HandleFunc("GET /api/sessions", h.listSessions)
	mux.HandleFunc("GET /api/sessions/{id}", h.getSession)
	mux.HandleFunc("GET /api/sessions/{id}/spec", h.getSpec)
	mux.HandleFunc("PUT /api/sessions/{name}/spec", h.updateSpec)
	mux.HandleFunc("POST /api/sessions/{id}/stop", h.stopSession)
	mux.HandleFunc("GET /api/sessions/{id}/transcript", h.getTranscript)
	mux.HandleFunc("GET /api/sessions/{id}/events", h.getEvents)
}

type handler struct {
	store      Store
	authorizer Authorizer
}

func (h *handler) healthz(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorize(w, r, AccessRequest{Action: ActionHealth}); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
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
	if _, ok := h.authorize(w, r, AccessRequest{Action: ActionCreateSession, CreateRequest: &req}); !ok {
		return
	}

	session, err := h.store.Create(req)
	if err != nil {
		switch {
		case errors.Is(err, ErrConflict):
			writeError(w, http.StatusConflict, err.Error())
		case errors.Is(err, ErrInvalidSession):
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusCreated, session)
}

func (h *handler) getSpec(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.authorize(w, r, AccessRequest{Action: ActionReadSession, SessionID: id}); !ok {
		return
	}
	spec, err := h.store.Spec(id)
	if err != nil {
		switch {
		case errors.Is(err, ErrNotFound):
			writeError(w, http.StatusNotFound, err.Error())
		case errors.Is(err, ErrInvalidSession):
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, spec)
}

func (h *handler) updateSpec(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var req SessionSpecUpdateRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxSessionRequestBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if _, ok := h.authorize(w, r, AccessRequest{Action: ActionUpdateSessionSpec}); !ok {
		return
	}
	response, err := h.store.UpdateSpec(name, req)
	if err != nil {
		switch {
		case errors.Is(err, ErrNotFound):
			writeError(w, http.StatusNotFound, err.Error())
		case errors.Is(err, ErrConflict):
			writeError(w, http.StatusConflict, err.Error())
		case errors.Is(err, ErrInvalidSession):
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *handler) listSessions(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.authorize(w, r, AccessRequest{Action: ActionListSessions})
	if !ok {
		return
	}
	limit, err := listLimit(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	includeChildren, err := listIncludeChildren(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sessions, err := h.store.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	sessions = h.authorizer.VisibleSessions(caller, sessions)
	if !includeChildren {
		sessions = topLevelSessions(sessions)
	}
	if limit > 0 && len(sessions) > limit {
		sessions = sessions[:limit]
	}
	writeJSON(w, http.StatusOK, SessionListResponse{Sessions: SessionListItems(sessions)})
}

func listLimit(r *http.Request) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return 0, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 0 {
		return 0, fmt.Errorf("limit must be a non-negative integer")
	}
	return limit, nil
}

func listIncludeChildren(r *http.Request) (bool, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("include_children"))
	if raw == "" {
		return false, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("include_children must be a boolean")
	}
	return value, nil
}

func topLevelSessions(sessions []Session) []Session {
	topLevel := make([]Session, 0, len(sessions))
	for _, session := range sessions {
		if session.ParentSessionID == nil || *session.ParentSessionID == "" {
			topLevel = append(topLevel, session)
		}
	}
	return topLevel
}

func (h *handler) getSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.authorize(w, r, AccessRequest{Action: ActionReadSession, SessionID: id}); !ok {
		return
	}
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
	if _, ok := h.authorize(w, r, AccessRequest{Action: ActionStopSession, SessionID: id}); !ok {
		return
	}
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
	if _, ok := h.authorize(w, r, AccessRequest{Action: ActionReadSession, SessionID: id}); !ok {
		return
	}
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
	if _, ok := h.authorize(w, r, AccessRequest{Action: ActionReadSession, SessionID: id}); !ok {
		return
	}
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

// --------- JSON helpers ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------

func (h *handler) authorize(w http.ResponseWriter, r *http.Request, req AccessRequest) (Caller, bool) {
	caller, err := h.authorizer.Caller(r, req)
	if err == nil {
		return caller, true
	}
	if status, detail, ok := AuthHTTPError(err); ok {
		writeError(w, status, detail)
		return Caller{}, false
	}
	writeError(w, http.StatusForbidden, err.Error())
	return Caller{}, false
}

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

package sessionapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"

	"github.com/telos-org/telos/internal/evidence"
)

// ActivityPage wraps GET /api/sessions/{id}/activity: display rows oldest
// first, and the newest row's event_seq (null before the first row).
type ActivityPage struct {
	Events       []map[string]any `json:"events"`
	HeadEventSeq *int64           `json:"head_event_seq"`
}

// activityEvidence is the evidence log of a session with display rows. Sessions
// an older runtime began, and older multi-spec sessions, serve raw events only.
func activityEvidence(m *Manifest) string {
	if len(m.Specs) != 1 || m.Specs[0].EvidencePath == nil || !evidence.HasActivity(*m.Specs[0].EvidencePath) {
		return ""
	}
	return *m.Specs[0].EvidencePath
}

// Activity reads the session's display rows backwards from the newest. It
// never reads hidden events, so its cost does not depend on how many the
// agent produced; an older page also reads the rows newer than it.
func (fs *FileStore) Activity(id string, window EventWindow) (ActivityPage, error) {
	page := ActivityPage{Events: []map[string]any{}}
	manifest, err := ReadManifest(fs.manifestPath(id))
	if err != nil {
		if os.IsNotExist(err) {
			err = fmt.Errorf("session %s: %w", id, ErrNotFound)
		}
		return page, err
	}
	source := activityEvidence(manifest)
	if source == "" || !evidence.SyncActivity(source) {
		return page, fmt.Errorf("session %s has no activity log: %w", id, ErrNotFound)
	}
	file, err := os.Open(evidence.ActivityPath(source))
	if errors.Is(err, os.ErrNotExist) {
		return page, nil // no events yet
	}
	if err != nil {
		return page, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return page, err
	}
	shown := int64(0)
	err = evidence.VisitLinesReverse(file, info.Size(), func(row map[string]any) bool {
		seq := ptrOr(eventSeq(row), 0)
		if seq == 0 {
			return false // not a row this log wrote
		}
		if page.HeadEventSeq == nil || seq > *page.HeadEventSeq {
			page.HeadEventSeq = &seq
		}
		if seq <= window.After {
			return true
		}
		if window.Before > 0 && seq >= window.Before {
			return false
		}
		page.Events = append(page.Events, row)
		if row["kind"] != evidence.TurnEnded {
			shown++
		}
		return window.Tail > 0 && shown >= window.Tail
	})
	slices.Reverse(page.Events)
	return page, err
}

func (h *handler) getActivity(w http.ResponseWriter, r *http.Request) {
	req, ok := h.windowRequest(w, r)
	if !ok {
		return
	}
	store, ok := h.store.(interface {
		Activity(string, EventWindow) (ActivityPage, error)
	})
	if !ok {
		writeError(w, http.StatusNotFound, "session activity is not available")
		return
	}
	if !req.stream {
		page, err := store.Activity(req.id, req.window)
		writePage(w, page, err)
		return
	}
	// A session without rows answers like its pages rather than an empty stream.
	if _, err := store.Activity(req.id, EventWindow{Tail: 1}); err != nil {
		writeStoreError(w, err)
		return
	}
	h.stream(w, r, req.id, req.window.After, func(out io.Writer, after *int64) bool {
		page, err := store.Activity(req.id, EventWindow{After: *after})
		if err != nil {
			return false
		}
		for _, row := range page.Events {
			data, _ := json.Marshal(row)
			_, _ = fmt.Fprintf(out, "id: %d\ndata: %s\n\n", ptrOr(eventSeq(row), 0), data)
		}
		*after = max(*after, ptrOr(page.HeadEventSeq, 0))
		return true
	})
}

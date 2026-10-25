package sessionapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/telos-org/telos/internal/activity"
)

const activityPageBytes = 2 * 1024 * 1024

type ActivityPage struct {
	Entries      []activity.Entry `json:"entries"`
	HeadEventSeq *int64           `json:"head_event_seq"`
	NextBefore   *int64           `json:"next_before"`
	History      string           `json:"history"`
}

func (h *handler) getActivity(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.authorize(w, r, AccessRequest{Action: ActionReadSession, SessionID: id}); !ok {
		return
	}
	limit, err := nonNegativeIntParam(r, "limit")
	if err != nil || limit > 1000 || (queryParamSet(r, "limit") && limit == 0) {
		writeError(w, 400, "limit must be between 1 and 1000")
		return
	}
	if limit == 0 {
		limit = 50
	}
	before, err := nonNegativeIntParam(r, "before")
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	after, err := nonNegativeIntParam(r, "after")
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	page, err := h.readEvents(id, EventWindow{After: after, Before: before, Tail: limit + 1, Activity: true, Costs: r.URL.Query().Get("costs") != "false"})
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, 404, err.Error())
		} else {
			writeError(w, 500, err.Error())
		}
		return
	}
	result := ActivityPage{Entries: []activity.Entry{}, HeadEventSeq: page.HeadEventSeq, History: "paged"}
	if page.HeadEventSeq == nil {
		result.History = "recent"
	}
	size := 4096
	for _, entry := range page.ActivityEntries {
		entrySize := activityEntrySize(&entry)
		if int64(len(result.Entries)) >= limit || size+entrySize > activityPageBytes {
			if len(result.Entries) > 0 && result.History == "paged" {
				result.NextBefore = result.Entries[len(result.Entries)-1].EventSeq
			}
			break
		}
		result.Entries = append(result.Entries, entry)
		size += entrySize + 1
	}
	writeJSON(w, 200, result)
}

// Preserve the bookmark even when one display record exceeds the response budget.
func activityEntrySize(entry *activity.Entry) int {
	encoded, _ := json.Marshal(entry)
	if len(encoded) > 512*1024 {
		entry.Headline = "This activity entry was too large to display"
		entry.Body = nil
		entry.Tone = "warning"
		encoded, _ = json.Marshal(entry)
	}
	return len(encoded)
}

// Opt-in envelopes keep the shared cloud reader's raw replay and both display
// permissions together. Ordinary /events responses and streams are unchanged.
type activityEventEnvelope struct {
	Event       map[string]any  `json:"event"`
	Display     *activity.Entry `json:"display"`
	DisplaySafe *activity.Entry `json:"display_safe"`
}

func activityEvent(event SessionEvent, fallback string) activityEventEnvelope {
	encoded, _ := json.Marshal(event)
	var raw map[string]any
	_ = json.Unmarshal(encoded, &raw)
	full := activity.Project(raw, fallback, true)
	safe := activity.Project(raw, fallback, false)
	for _, entry := range []*activity.Entry{full, safe} {
		if entry != nil {
			activityEntrySize(entry)
		}
	}
	if len(encoded) > 512*1024 {
		raw = map[string]any{"event": event.Event, "event_seq": event.EventSeq, "ts": event.Timestamp, "activity_truncated": true}
	}
	return activityEventEnvelope{raw, full, safe}
}

// Sequence-less evidence is already read in full by the compatibility store.
// Retain its visible tail independently of the raw tail requested by the reader.
func activitySnapshot(events []SessionEvent) []activityEventEnvelope {
	snapshot := []activityEventEnvelope{}
	size := 4096
	for i := len(events) - 1; i >= 0; i-- {
		event := activityEvent(events[i], fmt.Sprintf("snapshot:%d", i))
		if event.Display == nil && event.DisplaySafe == nil {
			continue
		}
		event.Event = map[string]any{}
		for _, entry := range []*activity.Entry{event.Display, event.DisplaySafe} {
			if entry != nil {
				entry.EventSeq = nil
				entry.ID = fmt.Sprintf("snapshot:%d", i)
			}
		}
		encoded, _ := json.Marshal(event)
		if size+len(encoded)+1 > activityPageBytes {
			break
		}
		snapshot = append(snapshot, event)
		size += len(encoded) + 1
		if len(snapshot) == 50 {
			break
		}
	}
	slices.Reverse(snapshot)
	return snapshot
}

func activityEventPage(page SessionEventsResponse) any {
	events := []activityEventEnvelope{}
	snapshot, _ := json.Marshal(page.ActivitySnapshot)
	size := 4096 + len(snapshot)
	for i := len(page.Events) - 1; i >= 0; i-- {
		event := activityEvent(page.Events[i], fmt.Sprintf("snapshot:%d", i))
		encoded, _ := json.Marshal(event)
		if size+len(encoded)+1 > activityPageBytes {
			break
		}
		events = append(events, event)
		size += len(encoded) + 1
	}
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
	return struct {
		Events        []activityEventEnvelope `json:"events"`
		Head          *int64                  `json:"head_event_seq"`
		DisplayEvents []activityEventEnvelope `json:"display_events,omitempty"`
	}{events, page.HeadEventSeq, page.ActivitySnapshot}
}

package sessionapi

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestActivityPagesCountDisplayEntriesAndRetainRawEvidence(t *testing.T) {
	var body strings.Builder
	for seq := 1; seq <= 1642; seq++ {
		line := writerLine(seq, "Repeated update")
		if seq%23 != 0 {
			line = strings.Replace(line, `"data":{`, `"data":{"audience":"agent",`, 1)
		}
		body.WriteString(line)
	}
	store, _ := eventFixture(t, body.String())
	h := &handler{store: store, authorizer: AllowAllAuthorizer{}}
	var seen []int64
	before := ""
	for {
		req := httptest.NewRequest("GET", "/activity?limit=50"+before, nil)
		req.SetPathValue("id", eventSessionID)
		w := httptest.NewRecorder()
		h.getActivity(w, req)
		if w.Code != http.StatusOK {
			t.Fatal(w.Body.String())
		}
		var page ActivityPage
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if *page.HeadEventSeq != 1642 {
			t.Fatalf("lost hidden source head: %+v", page)
		}
		for _, entry := range page.Entries {
			seen = append(seen, *entry.EventSeq)
		}
		if before == "" && (len(page.Entries) != 50 || page.NextBefore == nil) {
			t.Fatalf("first page is not 50 display rows: %+v", page)
		}
		if page.NextBefore == nil {
			break
		}
		before = fmt.Sprintf("&before=%d", *page.NextBefore)
	}
	if len(seen) != 71 {
		t.Fatalf("got %d rows, want 71", len(seen))
	}
	for i, seq := range seen {
		if seq != int64((71-i)*23) {
			t.Fatalf("missing/duplicated event at %d: %d", i, seq)
		}
	}
	raw, err := store.EventsPage(eventSessionID, EventWindow{Tail: 200})
	if err != nil || len(raw.Events) != 200 {
		t.Fatalf("raw API changed: %d %v", len(raw.Events), err)
	}
}

func TestActivityByteBoundContinuesAndKeepsOversizedBookmark(t *testing.T) {
	var body strings.Builder
	for seq := 1; seq <= 10; seq++ {
		body.WriteString(writerLine(seq, strings.Repeat("x", 400*1024)))
	}
	body.WriteString(writerLine(11, strings.Repeat("x", 600*1024)))
	store, _ := eventFixture(t, body.String())
	h := &handler{store: store, authorizer: AllowAllAuthorizer{}}
	req := httptest.NewRequest("GET", "/activity?limit=50", nil)
	req.SetPathValue("id", eventSessionID)
	w := httptest.NewRecorder()
	h.getActivity(w, req)
	var page ActivityPage
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(w.Body.Bytes()) > activityPageBytes || page.NextBefore == nil {
		t.Fatalf("not bounded/continuable: bytes=%d page=%+v", w.Body.Len(), page)
	}
	if page.Entries[0].ID != "rt:11" || page.Entries[0].Headline != "This activity entry was too large to display" {
		t.Fatalf("lost oversized entry: %+v", page.Entries[0])
	}
	if len(page.Entries) >= 50 || len(page.Entries) < 2 {
		t.Fatalf("unexpected bounded page length %d", len(page.Entries))
	}
}

func TestActivityRequiresSessionReadAccess(t *testing.T) {
	store, _ := eventFixture(t, writerHistory(2))
	h := &handler{store: store, authorizer: NewBearerAuthorizer(store, "secret")}
	req := httptest.NewRequest("GET", "/activity", nil)
	req.SetPathValue("id", eventSessionID)
	w := httptest.NewRecorder()
	h.getActivity(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized activity: %d %s", w.Code, w.Body.String())
	}
}

func TestNativeEventEnvelopeProjectsBeforeBoundingRawMetadata(t *testing.T) {
	line := strings.Replace(writerLine(1, "Still working"), `"data":{`, `"data":{"audience":"user","unused":"`+strings.Repeat("x", 600*1024)+`",`, 1)
	line += strings.Replace(writerLine(2, "Internal tool"), `"data":{`, `"data":{"audience":"agent",`, 1)
	store, _ := eventFixture(t, line)
	h := &handler{store: store, authorizer: AllowAllAuthorizer{}}
	req := httptest.NewRequest("GET", "/events?tail=200&view=activity", nil)
	req.SetPathValue("id", eventSessionID)
	w := httptest.NewRecorder()
	h.getEvents(w, req)
	var page struct {
		Events []activityEventEnvelope `json:"events"`
		Head   int64                   `json:"head_event_seq"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Head != 2 || len(page.Events) != 2 {
		t.Fatalf("lost raw coverage: %s", w.Body.String())
	}
	entry := page.Events[0]
	if entry.Display == nil || entry.Display.Headline != "Still working" || entry.Event["activity_truncated"] != true {
		t.Fatalf("raw size changed display projection: %+v", entry)
	}
	if page.Events[1].Display != nil || page.Events[1].DisplaySafe != nil {
		t.Fatal("hidden event became visible")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("id", eventSessionID)
		h.getEvents(w, r)
	}))
	defer server.Close()
	streamReq, _ := http.NewRequest("GET", server.URL+"?view=activity&after=0", nil)
	streamReq.Header.Set("Accept", "text/event-stream")
	response, err := server.Client().Do(streamReq)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 4096), activityPageBytes)
	found := false
	for scanner.Scan() {
		if data, ok := strings.CutPrefix(scanner.Text(), "data: "); ok {
			found = true
			var streamed activityEventEnvelope
			if err := json.Unmarshal([]byte(data), &streamed); err != nil {
				t.Fatal(err)
			}
			if streamed.Display == nil || streamed.Display.Headline != entry.Display.Headline {
				t.Fatal("history and stream diverged")
			}
			break
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("stream ended without an activity envelope")
	}
	rawReq := httptest.NewRequest("GET", "/events?tail=200", nil)
	rawReq.SetPathValue("id", eventSessionID)
	raw := httptest.NewRecorder()
	h.getEvents(raw, rawReq)
	if !strings.Contains(raw.Body.String(), strings.Repeat("x", 600*1024)) {
		t.Fatal("raw endpoint lost original evidence")
	}
}

func TestNativeRecentSnapshotRetainsVisibleHistoryBeyondRawTail(t *testing.T) {
	var body strings.Builder
	for i := 0; i < 1250; i++ {
		event := `{"event":"heartbeat"}`
		if i < 50 {
			event = `{"event":"agent_progress","data":{"audience":"user","text":"Repeated update"}}`
		}
		fmt.Fprintln(&body, event)
	}
	store, _ := eventFixture(t, body.String())
	h := &handler{store: store, authorizer: AllowAllAuthorizer{}}
	request := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.SetPathValue("id", eventSessionID)
		w := httptest.NewRecorder()
		if strings.HasPrefix(path, "/activity") {
			h.getActivity(w, r)
		} else {
			h.getEvents(w, r)
		}
		if w.Code != http.StatusOK {
			t.Fatal(w.Body.String())
		}
		return w
	}
	var history ActivityPage
	if err := json.Unmarshal(request("/activity?limit=50").Body.Bytes(), &history); err != nil {
		t.Fatal(err)
	}
	var replay struct {
		Events  []activityEventEnvelope `json:"events"`
		Display []activityEventEnvelope `json:"display_events"`
		Head    *int64                  `json:"head_event_seq"`
	}
	response := request("/events?tail=1000&view=activity")
	if err := json.Unmarshal(response.Body.Bytes(), &replay); err != nil {
		t.Fatal(err)
	}
	if history.History != "recent" || replay.Head != nil || len(history.Entries) != 50 || len(replay.Display) != 50 || len(replay.Events) != 1000 || response.Body.Len() > activityPageBytes {
		t.Fatalf("inconsistent recent history: history=%d snapshot=%d raw=%d", len(history.Entries), len(replay.Display), len(replay.Events))
	}
	for i, entry := range history.Entries {
		other := replay.Display[49-i].DisplaySafe
		if other == nil || other.ID != entry.ID || other.Headline != entry.Headline || other.EventSeq != nil {
			t.Fatalf("snapshot differs at %d: %#v / %#v", i, entry, other)
		}
	}
	var raw SessionEventsResponse
	if err := json.Unmarshal(request("/events?tail=1000").Body.Bytes(), &raw); err != nil || len(raw.Events) != 1000 {
		t.Fatalf("raw tail changed: %d %v", len(raw.Events), err)
	}
}

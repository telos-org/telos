package sessionapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestVisibleEventsMatchSharedFixture(t *testing.T) {
	body, err := os.ReadFile("testdata/visible_events.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Event   SessionEvent `json:"event"`
		Visible bool         `json:"visible"`
	}
	if err := json.Unmarshal(body, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		if visibleEvent(c.Event) != c.Visible {
			t.Errorf("%s %v: visible = %v, want %v", c.Event.Event, c.Event.Data, !c.Visible, c.Visible)
		}
	}
}

func TestVisibleEventPagesCountOnlyShownEvents(t *testing.T) {
	hidden := func(seq int) string {
		return fmt.Sprintf("{\"schema\":\"telos.evidence.v2\",\"session_id\":%q,\"event_seq\":%d,\"event\":\"agent_progress\",\"data\":{\"audience\":\"agent\",\"text\":\"tool\"}}\n", eventSessionID, seq)
	}
	body := writerLine(1, "first") + hidden(2) + writerLine(3, "second") + hidden(4) + hidden(5) + writerLine(6, "third") + hidden(7)
	queries := map[string][]string{
		"visible=true&tail=2":          {"second", "third"},
		"visible=true&before=3&tail=2": {"first"},
		"visible=true":                 {"first", "second", "third"},
	}
	// A log without sequence numbers at its start takes the full-projection
	// path, which has no durable cursors.
	for name, prefix := range map[string]string{"bounded": "", "compatibility": "{\"event\":\"legacy\"}\n"} {
		t.Run(name, func(t *testing.T) {
			store, _ := eventFixture(t, prefix+body)
			mux := http.NewServeMux()
			RegisterRoutes(mux, store, AllowAllAuthorizer{}, RuntimeIdentity{})
			for query, want := range queries {
				if name == "compatibility" && strings.Contains(query, "before") {
					continue
				}
				recorder := httptest.NewRecorder()
				mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/sessions/"+eventSessionID+"/events?"+query, nil))
				var page SessionEventsResponse
				if recorder.Code != http.StatusOK || json.Unmarshal(recorder.Body.Bytes(), &page) != nil {
					t.Fatalf("%s: status %d: %s", query, recorder.Code, recorder.Body)
				}
				var texts []string
				for _, event := range page.Events {
					texts = append(texts, event.Data["text"].(string))
				}
				if !reflect.DeepEqual(texts, want) {
					t.Errorf("%s: events %v, want %v", query, texts, want)
				}
				if name == "bounded" && ptrOr(page.HeadEventSeq, 0) != 7 {
					t.Errorf("%s: head %v, want the newest event of any kind", query, page.HeadEventSeq)
				}
			}
		})
	}
}

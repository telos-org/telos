package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/sessionapi"
)

func progressEvent(position, text string) string {
	return fmt.Sprintf(`{"event":"agent_progress",%s,"ts":"2026-10-09T10:00:00Z","data":{"audience":"user","kind":"progress_update","text":%q}}`, position, text)
}

func followTestPage(t *testing.T, session string, events ...string) *cloud.SessionLogPage {
	t.Helper()
	page := &cloud.SessionLogPage{Cursors: &cloud.SessionLogCursors{Session: &session}}
	for _, raw := range events {
		var event sessionapi.SessionEvent
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			t.Fatal(err)
		}
		page.RawEvents = append(page.RawEvents, json.RawMessage(raw))
		page.Events = append(page.Events, event)
	}
	return page
}

func eventTexts(events []sessionapi.SessionEvent) []string {
	texts := make([]string, 0, len(events))
	for _, event := range events {
		texts = append(texts, eventDataString(event, "text"))
	}
	return texts
}

func TestCloudLogFollowerReturnsOnlyNewEvents(t *testing.T) {
	follower := newCloudLogFollower(followTestPage(t, "runtime_1",
		progressEvent(`"event_seq":1`, "one"),
		progressEvent(`"event_seq":2`, "two"),
		progressEvent(`"seq":5`, "cloud five"),
	))

	steps := []struct {
		page *cloud.SessionLogPage
		want []string
	}{
		{followTestPage(t, "runtime_1",
			progressEvent(`"event_seq":2`, "two"),
			progressEvent(`"event_seq":3`, "three"),
			progressEvent(`"seq":5`, "cloud five"),
			progressEvent(`"seq":6`, "cloud six"),
		), []string{"three", "cloud six"}},
		{followTestPage(t, "runtime_1",
			progressEvent(`"event_seq":3`, "three"),
			progressEvent(`"seq":6`, "cloud six"),
		), []string{}},
		// A restarted runtime numbers its events from 1 again.
		{followTestPage(t, "runtime_2",
			progressEvent(`"event_seq":1`, "after restart"),
		), []string{"after restart"}},
		{followTestPage(t, "runtime_2",
			progressEvent(`"event_seq":1`, "after restart"),
			`{"event":"agent_progress","data":{"audience":"user","text":"unnumbered"}}`,
		), []string{"unnumbered"}},
		{followTestPage(t, "runtime_2",
			`{"event":"agent_progress","data":{"audience":"user","text":"unnumbered"}}`,
		), []string{}},
	}
	for index, step := range steps {
		got := eventTexts(follower.fresh(step.page))
		if strings.Join(got, "|") != strings.Join(step.want, "|") {
			t.Fatalf("step %d: fresh = %q, want %q", index, got, step.want)
		}
	}
}

func TestFollowCloudLogsPrintsNewActivityUntilTheGoalStops(t *testing.T) {
	previous := logFollowInterval
	logFollowInterval = 0
	t.Cleanup(func() { logFollowInterval = previous })

	first := progressEvent(`"event_seq":1`, "Working on the spec")
	second := progressEvent(`"event_seq":2`, "Stood up the API")
	third := progressEvent(`"event_seq":3`, "Checking the result")
	// The third event arrives after the first poll.
	history := []string{first, second, third}
	statuses := []string{"working", "stopped"}

	for _, jsonOutput := range []bool{false, true} {
		t.Run(fmt.Sprintf("json=%t", jsonOutput), func(t *testing.T) {
			goalCalls := 0
			var queries []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/deployments/goal_1/logs":
					query := r.URL.Query()
					after, _ := strconv.Atoi(query.Get("after_rt"))
					before, err := strconv.Atoi(query.Get("before_rt"))
					if err != nil {
						queries = append(queries, query.Get("after_rt"))
						before = len(history) + 1
					}
					var events []string
					for seq := after + 1; seq < before && seq <= 2+goalCalls; seq++ {
						events = append(events, history[seq-1])
					}
					_, _ = fmt.Fprintf(w, `{"events":[%s],"cursors":{"session":"runtime_1"}}`, strings.Join(events, ","))
				case "/api/deployments/goal_1":
					_, _ = fmt.Fprintf(w, `{"id":"goal_1","name":"demo","state":"running","status":%q}`, statuses[goalCalls])
					goalCalls++
				default:
					t.Errorf("unexpected request: %s", r.URL)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			initial := followTestPage(t, "runtime_1", first)
			var out bytes.Buffer
			printer := newLogFollowPrinter(&out, jsonOutput, "personal", initial.Events)
			err := followCloudLogs(cloud.NewClient(server.URL, "token"), "goal_1", newCloudLogFollower(initial), printer)
			if err != nil {
				t.Fatal(err)
			}
			text := out.String()
			if strings.Contains(text, "Working on the spec") ||
				strings.Count(text, "Stood up the API") != 1 ||
				strings.Count(text, "Checking the result") != 1 {
				t.Fatalf("followed output:\n%s", text)
			}
			if jsonOutput && strings.Count(text, `"context":"personal"`) != 2 {
				t.Fatalf("followed JSON lost its context:\n%s", text)
			}
			if goalCalls != 2 {
				t.Fatalf("follow checked the Goal %d times, want 2", goalCalls)
			}
			// Each poll starts after the newest printed event.
			if strings.Join(queries, ",") != "1,2" {
				t.Fatalf("polled after %v, want 1 then 2", queries)
			}
		})
	}
}

func TestFollowCloudLogsPrintsEveryPendingEvent(t *testing.T) {
	previous := logFollowInterval
	logFollowInterval = 0
	t.Cleanup(func() { logFollowInterval = previous })

	// The first view printed runtime_1's event 1. By the next poll the
	// serving runtime has events 1 through 1201.
	for name, test := range map[string]struct {
		pageLimit   int64  // Cloud's size limit, in events; 0 means tail
		marker      bool   // the newest page is a position-only marker
		session     string // the runtime serving after the first request
		firstServed string // the runtime serving the first request
		want        string // the runtime whose events should print
		first       int64  // the first event that should print
	}{
		"burst":                {firstServed: "runtime_1", session: "runtime_1", want: "runtime_1", first: 2},
		"short pages":          {pageLimit: 300, firstServed: "runtime_1", session: "runtime_1", want: "runtime_1", first: 2},
		"position marker":      {marker: true, firstServed: "runtime_1", session: "runtime_1", want: "runtime_1", first: 2},
		"restart":              {firstServed: "runtime_2", session: "runtime_2", want: "runtime_2", first: 1},
		"restart while paging": {firstServed: "runtime_1", session: "runtime_2", want: "runtime_2", first: 1},
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/deployments/goal_1" {
					_, _ = fmt.Fprint(w, `{"id":"goal_1","name":"demo","state":"running","status":"stopped"}`)
					return
				}
				calls++
				session := test.session
				if calls == 1 {
					session = test.firstServed
				}
				query := r.URL.Query()
				bound := func(name string, fallback int64) int64 {
					if value, err := strconv.ParseInt(query.Get(name), 10, 64); err == nil {
						return value
					}
					return fallback
				}
				after, before, limit := bound("after_rt", 0), bound("before_rt", 1202), bound("tail", 0)
				if test.pageLimit > 0 {
					limit = min(limit, test.pageLimit)
				}
				var events []string
				if test.marker && !query.Has("before_rt") {
					events = append(events, `{"event_seq":1201}`)
				} else {
					for seq := max(after+1, before-limit); seq < before; seq++ {
						events = append(events, progressEvent(fmt.Sprintf(`"event_seq":%d`, seq), fmt.Sprintf("%s update %d", session, seq)))
					}
				}
				_, _ = fmt.Fprintf(w, `{"events":[%s],"cursors":{"session":%q}}`, strings.Join(events, ","), session)
			}))
			defer server.Close()

			initial := followTestPage(t, "runtime_1", progressEvent(`"event_seq":1`, "already printed"))
			var out bytes.Buffer
			printer := newLogFollowPrinter(&out, true, "personal", initial.Events)
			if err := followCloudLogs(cloud.NewClient(server.URL, "token"), "goal_1", newCloudLogFollower(initial), printer); err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(out.String()), "\n")
			last := int64(1201)
			if test.marker {
				last = 1200 // The marker covers 1201 but isn't activity.
			}
			if int64(len(lines)) != last-test.first+1 ||
				!strings.Contains(lines[0], fmt.Sprintf(`"%s update %d"`, test.want, test.first)) ||
				!strings.Contains(lines[len(lines)-1], fmt.Sprintf(`"%s update %d"`, test.want, last)) {
				t.Fatalf("printed %d events, from %.120s to %.120s", len(lines), lines[0], lines[len(lines)-1])
			}
		})
	}
}

func TestFollowCloudLogsSkipsEventsOlderThanTheFirstView(t *testing.T) {
	previous := logFollowInterval
	logFollowInterval = 0
	t.Cleanup(func() { logFollowInterval = previous })

	// The first view's tail held only runtime events, but Cloud's own
	// events had reached seq 5 by then.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/deployments/goal_1" {
			_, _ = fmt.Fprint(w, `{"id":"goal_1","name":"demo","state":"running","status":"stopped"}`)
			return
		}
		events := ""
		if after, _ := strconv.Atoi(r.URL.Query().Get("after_cp")); after < 5 && !r.URL.Query().Has("before_cp") {
			events = `{"event":"deployment.accepted","seq":5,"message":"Older Cloud event"}`
		}
		_, _ = fmt.Fprintf(w, `{"events":[%s],"cursors":{"session":"runtime_1"}}`, events)
	}))
	defer server.Close()

	initial := followTestPage(t, "runtime_1", progressEvent(`"event_seq":2000`, "Newest update"))
	runtime, control := int64(2000), int64(5)
	initial.Cursors.Runtime, initial.Cursors.Control = &runtime, &control
	var out bytes.Buffer
	printer := newLogFollowPrinter(&out, false, "personal", initial.Events)
	if err := followCloudLogs(cloud.NewClient(server.URL, "token"), "goal_1", newCloudLogFollower(initial), printer); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "Older Cloud event") {
		t.Fatalf("replayed an event older than the first view:\n%s", out.String())
	}
}

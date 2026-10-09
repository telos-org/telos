package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	pages := []string{
		`{"events":[` + first + `,` + second + `],"cursors":{"session":"runtime_1"}}`,
		`{"events":[` + first + `,` + second + `,` + third + `],"cursors":{"session":"runtime_1"}}`,
	}
	statuses := []string{"working", "stopped"}

	for _, jsonOutput := range []bool{false, true} {
		t.Run(fmt.Sprintf("json=%t", jsonOutput), func(t *testing.T) {
			logCalls, goalCalls := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/deployments/goal_1/logs":
					_, _ = fmt.Fprint(w, pages[logCalls])
					logCalls++
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
		})
	}
}

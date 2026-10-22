package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/telos-org/telos/internal/cloud"
)

func TestCloudHumanLogsPagePastHiddenEventsAndRetainAbsentSourceCursor(t *testing.T) {
	pages := []string{
		logPage(`{"event":"agent_progress","event_seq":100,"data":{"kind":"tool","text":"read"}},`+
			`{"event":"agent_progress","seq":900,"data":{"audience":"agent","text":"technical"}}`, `"runtime_1"`),
		logPage(`{"event":"agent_progress","event_seq":98,"data":{"audience":"user","kind":"progress_update","text":"Checking the strategy's risk limits."}},`+
			`{"event":"agent_progress","event_seq":99,"data":{"kind":"tool","text":"read"}}`, `"runtime_1"`),
		logPage(`{"event":"agent_progress","seq":899,"data":{"audience":"user","kind":"progress_update","text":"Defining recovery after a restart."}}`, `"runtime_1"`),
	}
	queries := []string{"tail=2", "before_cp=900&before_rt=100&tail=1000", "before_cp=900&before_rt=98&tail=1000"}
	client, calls := logPagingServer(t, pages, queries)
	first, err := client.GetSessionLogPage("session_1", 2)
	if err != nil {
		t.Fatal(err)
	}
	events, err := expandCloudLogs(client, "session_1", first, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	if *calls != 3 || len(renderLogRows(events)) != 2 || len(events) != 2 {
		t.Fatalf("calls=%d events=%d rows=%#v", *calls, len(events), renderLogRows(events))
	}
}

func TestCloudHumanLogsDeduplicateOverlappingPages(t *testing.T) {
	tool := `{"event":"agent_progress","event_seq":10,"data":{"kind":"tool","text":"read"}}`
	pages := []string{
		logPage(tool, `"runtime_1"`),
		logPage(`{"event":"agent_progress","event_seq":8,"data":{"audience":"user","kind":"progress_update","text":"Checking risk limits."}},`+tool, `"runtime_1"`),
		logPage(`{"event":"agent_progress","event_seq":7,"data":{"audience":"user","kind":"progress_update","text":"Checking restart recovery."}},`+
			`{"event":"agent_progress","event_seq":8,"data":{"audience":"user","kind":"progress_update","text":"Checking risk limits."}}`, `"runtime_1"`),
	}
	client, calls := logPagingServer(t, pages, []string{"tail=2", "before_cp=901&before_rt=10&tail=1000", "before_cp=901&before_rt=8&tail=1000"})
	first, err := client.GetSessionLogPage("session_1", 2)
	if err != nil {
		t.Fatal(err)
	}
	events, err := expandCloudLogs(client, "session_1", first, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	if *calls != 3 || len(events) != 2 {
		t.Fatalf("calls=%d page=%#v", *calls, events)
	}
}

func TestCloudHumanLogsMergeSourcesByReceiptTime(t *testing.T) {
	client, _ := logPagingServer(t, []string{
		logPage(`{"event":"agent_progress","seq":20,"time":"2026-09-16T12:01:00Z","data":{"audience":"user","kind":"progress_update","text":"Earlier control update."}},`+
			`{"event":"agent_progress","event_seq":30,"received_at":"2026-09-16T12:01:40Z","data":{"audience":"user","kind":"progress_update","text":"Latest runtime update."}}`, `"runtime_1"`),
		logPage(`{"event":"agent_progress","event_seq":29,"received_at":"2026-09-16T12:01:30Z","data":{"audience":"user","kind":"progress_update","text":"Middle runtime update."}}`, `"runtime_1"`),
	}, []string{"tail=3", "before_cp=20&before_rt=30&tail=1000"})
	first, err := client.GetSessionLogPage("session_1", 3)
	if err != nil {
		t.Fatal(err)
	}
	events, err := expandCloudLogs(client, "session_1", first, 3, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Earlier control update.", "Middle runtime update.", "Latest runtime update."}
	for index, text := range want {
		if events[index].Data["text"] != text {
			t.Fatalf("event ordering at %d: %#v", index, events[index])
		}
	}
}

func TestCloudHumanLogsKeepSnapshotWhenOlderHistoryIsUnavailable(t *testing.T) {
	initial := logPage(`{"event":"agent_progress","event_seq":10,"data":{"audience":"user","kind":"progress_update","text":"Checking risk limits."}}`, `"runtime_1"`)
	for _, test := range []struct {
		name string
		next string
		want string
	}{
		{"unreachable", logPage(`{"event":"deployment.status","seq":1,"message":"partial"}`, "null"), "runtime could not be reached"},
		{"session changed", logPage(`{"event":"agent_progress","event_seq":1,"data":{"audience":"user","kind":"progress_update","text":"New session."}}`, `"runtime_2"`), "Goal restarted"},
		{"server ignored cursor", initial, "older activity page"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, calls := logPagingServer(t, []string{initial, test.next}, []string{"tail=2", "before_cp=901&before_rt=10&tail=1000"})
			first, err := client.GetSessionLogPage("session_1", 2)
			if err != nil {
				t.Fatal(err)
			}
			events, err := expandCloudLogs(client, "session_1", first, 2, false)
			if err == nil || !strings.Contains(err.Error(), test.want) || *calls != 2 {
				t.Fatalf("calls=%d err=%v", *calls, err)
			}
			if len(events) != 1 || events[0].Data["text"] != "Checking risk limits." {
				t.Fatalf("partial or different session was merged: %#v", events)
			}
		})
	}
}

func TestCloudHumanLogsStopAtEmptyReachablePage(t *testing.T) {
	client, calls := logPagingServer(t, []string{
		logPage(`{"event":"agent_progress","event_seq":1,"data":{"kind":"tool","text":"read"}}`, `"runtime_1"`),
		`{"events":[],"cursors":{"rt":null,"cp":null,"session":"runtime_1"}}`,
	}, []string{"tail=2", "before_cp=901&before_rt=1&tail=1000"})
	first, err := client.GetSessionLogPage("session_1", 2)
	if err != nil {
		t.Fatal(err)
	}
	events, err := expandCloudLogs(client, "session_1", first, 2, false)
	if err != nil || *calls != 2 || len(events) != 0 {
		t.Fatalf("calls=%d page=%#v err=%v", *calls, events, err)
	}
}

func TestCloudHumanLogsUseOneLegacyFallback(t *testing.T) {
	initialEvents := `{"event":"agent_progress","data":{"kind":"tool","text":"read"}},` +
		`{"event":"agent_progress","data":{"kind":"tool","text":"write"}}`
	for _, envelope := range []string{"", `,"cursors":{"rt":null,"cp":null,"session":"runtime_1"}`} {
		t.Run(envelope, func(t *testing.T) {
			initial := `{"events":[` + initialEvents + `]` + envelope + `}`
			full := `{"events":[{"event":"agent_progress","data":{"audience":"user","kind":"progress_update","text":"Checking risk limits."}}]}`
			client, calls := logPagingServer(t, []string{initial, full}, []string{"tail=2", ""})
			first, err := client.GetSessionLogPage("session_1", 2)
			if err != nil {
				t.Fatal(err)
			}
			events, err := expandCloudLogs(client, "session_1", first, 2, false)
			if err != nil || *calls != 2 || len(renderLogRows(events)) != 1 {
				t.Fatalf("calls=%d page=%#v err=%v", *calls, events, err)
			}
		})
	}
}

func TestCloudHumanLogsDoNotReplaceSnapshotWithAnotherSessionDuringLegacyFallback(t *testing.T) {
	client, calls := logPagingServer(t, []string{
		logPage(`{"event":"agent_progress","event_seq":10,"data":{"audience":"user","kind":"progress_update","text":"Original session."}}`, `"runtime_1"`),
		logPage(`{"event":"agent_progress","data":{"kind":"tool","text":"legacy"}}`, `"runtime_1"`),
		logPage(`{"event":"agent_progress","data":{"audience":"user","kind":"progress_update","text":"New session."}}`, `"runtime_2"`),
	}, []string{"tail=2", "before_cp=901&before_rt=10&tail=1000", ""})
	first, err := client.GetSessionLogPage("session_1", 2)
	if err != nil {
		t.Fatal(err)
	}
	events, err := expandCloudLogs(client, "session_1", first, 2, false)
	if err == nil || !strings.Contains(err.Error(), "Goal restarted") || *calls != 3 || events[0].Data["text"] != "Original session." {
		t.Fatalf("calls=%d page=%#v err=%v", *calls, events, err)
	}
}

func TestCloudLogsPreserveSingleRequestModes(t *testing.T) {
	for _, test := range []struct {
		name  string
		tail  int
		all   bool
		raw   bool
		json  bool
		query string
	}{
		{"default", defaultLogTail, false, false, false, "tail=50"},
		{"small JSON", 2, false, false, true, "tail=2"},
		{"page-sized JSON", 1000, false, false, true, "tail=1000"},
		{"all", 1500, true, false, false, ""},
		{"all JSON", 1500, true, false, true, ""},
		{"raw", 1500, false, true, false, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			page := `{"events":[{"event":"agent_progress","event_seq":1,"data":{"audience":"user","text":"Visible activity"},"future_field":"preserved"},` +
				`{"event":"agent_progress","event_seq":2,"data":{"audience":"agent","text":"technical evidence"}}]}`
			client, calls := logPagingServer(t, []string{page}, []string{test.query})
			configureCloudTest(t, client.Endpoint)
			t.Setenv("TELOS_CONTEXT", "")
			out := captureStdout(t, func() {
				printCloudSessionLogs(&cloud.SessionRecord{ID: "session_1", State: "healthy"}, logViewOptions{Tail: test.tail, All: test.all}, test.json, test.raw, false, "")
			})
			if *calls != 1 || !strings.Contains(out, "Visible activity") || test.raw && !strings.Contains(out, "future_field") {
				t.Fatalf("calls=%d output=%s", *calls, out)
			}
			if strings.Contains(out, "technical evidence") != (test.json || test.raw) {
				t.Fatalf("technical event visibility changed: %s", out)
			}
		})
	}
}

func TestCloudHumanLogsDoNotCallUnreachableRuntimeQuiet(t *testing.T) {
	client, _ := logPagingServer(t, []string{
		logPage(`{"event":"agent_progress","event_seq":10,"received_at":"2020-01-01T12:01:40Z","data":{"audience":"user","kind":"progress_update","text":"Checking risk limits."}}`, `"runtime_1"`),
		logPage("", "null"),
	}, []string{"tail=2", "before_cp=901&before_rt=10&tail=1000"})
	configureCloudTest(t, client.Endpoint)
	t.Setenv("TELOS_CONTEXT", "")
	out := captureStdout(t, func() {
		printCloudSessionLogs(&cloud.SessionRecord{ID: "session_1", State: "working"}, logViewOptions{Tail: 2}, false, false, false, "")
	})
	if !strings.Contains(out, "Checking risk limits.") || strings.Contains(out, "No new progress update") {
		t.Fatalf("unreachable runtime misreported as quiet: %s", out)
	}
}

func TestCloudHumanLogsUseAuthoritativeStatusForQuietMessage(t *testing.T) {
	for _, test := range []struct {
		status string
		quiet  bool
	}{
		{"ready", false},
		{"needs_attention", false},
		{"in_progress", true},
		{"", true},
	} {
		t.Run(test.status, func(t *testing.T) {
			client, _ := logPagingServer(t, []string{
				logPage(`{"event":"agent_progress","event_seq":10,"received_at":"2020-01-01T12:01:40Z","data":{"audience":"user","kind":"progress_update","text":"Checking risk limits."}}`, `"runtime_1"`),
			}, []string{"tail=1"})
			configureCloudTest(t, client.Endpoint)
			t.Setenv("TELOS_CONTEXT", "")
			out := captureStdout(t, func() {
				printCloudSessionLogs(&cloud.SessionRecord{ID: "session_1", State: "running", Status: test.status}, logViewOptions{Tail: 1}, false, false, false, "")
			})
			if strings.Contains(out, "No new progress update") != test.quiet {
				t.Fatalf("status=%q output=%s", test.status, out)
			}
		})
	}
}

func TestCloudHumanLogsKeepAvailableRowsWhenInitiallyUnreachable(t *testing.T) {
	client, calls := logPagingServer(t, []string{
		logPage(`{"event":"deployment.status","seq":1,"message":"Waiting for the runtime"}`, "null"),
	}, []string{"tail=2"})
	first, err := client.GetSessionLogPage("session_1", 2)
	if err != nil {
		t.Fatal(err)
	}
	events, err := expandCloudLogs(client, "session_1", first, 2, false)
	if err == nil || *calls != 1 || len(events) != 1 {
		t.Fatalf("available history lost: page=%#v err=%v calls=%d", events, err, *calls)
	}
}

func TestCloudHumanLogsRetainTurnEndingsForQuietNotice(t *testing.T) {
	client, _ := logPagingServer(t, []string{
		logPage(`{"event":"agent_complete","event_seq":10,"received_at":"2020-01-01T12:02:00Z","data":{"audience":"agent"}}`, `"runtime_1"`),
		logPage(`{"event":"agent_progress","event_seq":9,"received_at":"2020-01-01T12:01:00Z","data":{"audience":"user","kind":"progress_update","text":"Checking recovery."}}`, `"runtime_1"`),
	}, []string{"tail=1", "before_cp=901&before_rt=10&tail=1000"})
	first, err := client.GetSessionLogPage("session_1", 1)
	if err != nil {
		t.Fatal(err)
	}
	events, err := expandCloudLogs(client, "session_1", first, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	printStructuredLogs(&output, events, logViewOptions{Tail: 1, Active: true})
	if !strings.Contains(output.String(), "Checking recovery.") || strings.Contains(output.String(), "No new progress update") {
		t.Fatalf("turn ending lost: %s", output.String())
	}
}

func TestCloudLargeTailsPageAndFollow(t *testing.T) {
	previous := logFollowInterval
	logFollowInterval = 0
	t.Cleanup(func() { logFollowInterval = previous })
	page := func(first, last int) string {
		return `{"events":[` + logHistoryEvents(first, last) + `],"cursors":{"rt":4000,"cp":null,"session":"runtime_1"}}`
	}
	for _, jsonOutput := range []bool{false, true} {
		for _, follow := range []bool{false, true} {
			t.Run(fmt.Sprintf("json=%t/follow=%t", jsonOutput, follow), func(t *testing.T) {
				// Each older page overlaps the previous one; odd events are hidden in readable output.
				pages := []string{page(3001, 4000), page(2002, 3001)}
				queries := []string{"tail=1000", "before_rt=3001&tail=1000"}
				if !jsonOutput {
					pages = append(pages, page(1003, 2002), page(4, 1003))
					queries = append(queries, "before_rt=2002&tail=1000", "before_rt=1003&tail=1000")
				}
				if follow {
					pages = append(pages, page(4000, 4002))
					queries = append(queries, "tail=1000")
				}
				calls, goalCalls := 0, 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/api/deployments/session_1" {
						goalCalls++
						fmt.Fprint(w, `{"id":"session_1","state":"stopped"}`)
						return
					}
					if calls >= len(pages) {
						t.Errorf("unexpected extra request: %s", r.URL)
						http.NotFound(w, r)
						return
					}
					if r.URL.Path != "/api/deployments/session_1/logs" || r.URL.RawQuery != queries[calls] {
						t.Errorf("request %d = %s, want query %s", calls, r.URL, queries[calls])
					}
					fmt.Fprint(w, pages[calls])
					calls++
				}))
				defer server.Close()
				configureCloudTest(t, server.URL)
				t.Setenv("TELOS_CONTEXT", "")
				// A file avoids filling captureStdout's pipe with thousands of rows.
				output, err := os.CreateTemp(t.TempDir(), "logs")
				if err != nil {
					t.Fatal(err)
				}
				previousStdout := os.Stdout
				os.Stdout = output
				defer func() { os.Stdout = previousStdout }()
				printCloudSessionLogs(&cloud.SessionRecord{ID: "session_1", State: "healthy"}, logViewOptions{Tail: 1500}, jsonOutput, false, follow, "")
				if err := output.Close(); err != nil {
					t.Fatal(err)
				}
				body, err := os.ReadFile(output.Name())
				if err != nil {
					t.Fatal(err)
				}
				first, step, last := 1002, 2, 4000
				if jsonOutput {
					first, step = 2501, 1
				}
				if follow {
					last = 4002
				}
				lines := strings.Split(strings.TrimSpace(string(body)), "\n")
				if calls != len(pages) || len(lines) != (last-first)/step+1 || (goalCalls == 1) != follow {
					t.Fatalf("calls=%d goalCalls=%d lines=%d", calls, goalCalls, len(lines))
				}
				for index, line := range lines {
					want := fmt.Sprintf("event_%04d", first+index*step)
					if !strings.Contains(line, want) {
						t.Fatalf("line %d = %s, want %s", index, line, want)
					}
					if jsonOutput && (!json.Valid([]byte(line)) || !strings.Contains(line, `"context":`)) {
						t.Fatalf("JSON event lost its shape or context: %s", line)
					}
				}
			})
		}
	}
}

func TestCloudLargeTailsUseOneLegacyFallback(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		t.Run(fmt.Sprintf("json=%t", jsonOutput), func(t *testing.T) {
			client, calls := logPagingServer(t, []string{
				`{"events":[` + logHistoryEvents(501, 1500) + `]}`,
				`{"events":[` + logHistoryEvents(1, 1500) + `]}`,
			}, []string{"tail=1000", ""})
			first, err := client.GetSessionLogPage("session_1", 1000)
			if err != nil {
				t.Fatal(err)
			}
			events, err := expandCloudLogs(client, "session_1", first, 2000, jsonOutput)
			if err != nil || *calls != 2 || len(events) != 1500 || *events[0].EventSeq != 1 {
				t.Fatalf("calls=%d events=%d err=%v", *calls, len(events), err)
			}
		})
	}
}

func logHistoryEvents(first, last int) string {
	events := make([]string, 0, last-first+1)
	for seq := first; seq <= last; seq++ {
		audience := "agent"
		if seq%2 == 0 {
			audience = "user"
		}
		events = append(events, fmt.Sprintf(`{"event":"agent_progress","event_seq":%d,"ts":%q,"data":{"audience":%q,"text":"event_%04d"}}`, seq, time.Unix(int64(seq), 0).UTC().Format(time.RFC3339), audience, seq))
	}
	return strings.Join(events, ",")
}

func logPage(events, session string) string {
	return `{"events":[` + events + `],"cursors":{"rt":100,"cp":900,"session":` + session + `}}`
}

func logPagingServer(t *testing.T, pages, queries []string) (*cloud.Client, *int) {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index := calls
		calls++
		if index >= len(pages) {
			t.Errorf("unexpected extra page request: %s", r.URL)
			http.Error(w, "unexpected page", http.StatusInternalServerError)
			return
		}
		if r.URL.Path != "/api/deployments/session_1/logs" || r.URL.RawQuery != queries[index] {
			t.Errorf("request %d = %s, want query %q", index, r.URL, queries[index])
		}
		if !json.Valid([]byte(pages[index])) {
			t.Errorf("invalid fixture: %s", pages[index])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, pages[index])
	}))
	t.Cleanup(srv.Close)
	return cloud.NewClient(srv.URL, "test-token"), &calls
}

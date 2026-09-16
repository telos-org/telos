package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/cloud"
)

func TestCloudHumanLogsPagePastHiddenEventsAndRetainAbsentSourceCursor(t *testing.T) {
	pages := []string{
		logPage(`{"event":"agent_progress","event_seq":100,"data":{"kind":"tool","text":"read"}},`+
			`{"event":"agent_progress","seq":900,"data":{"audience":"agent","text":"technical"}}`, `"runtime_1"`),
		logPage(`{"event":"agent_progress","event_seq":98,"data":{"audience":"user","kind":"user_update","text":"Checking the strategy's risk limits."}},`+
			`{"event":"agent_progress","event_seq":99,"data":{"kind":"tool","text":"read"}}`, `"runtime_1"`),
		logPage(`{"event":"agent_progress","seq":899,"data":{"audience":"user","kind":"user_update","text":"Defining recovery after a restart."}}`, `"runtime_1"`),
	}
	queries := []string{"tail=2", "before_cp=900&before_rt=100&tail=1000", "before_cp=900&before_rt=98&tail=1000"}
	client, calls := logPagingServer(t, pages, queries)
	first, err := client.GetSessionLogPage("session_1", 2)
	if err != nil {
		t.Fatal(err)
	}
	page, err := expandCloudHumanLogs(client, "session_1", first, 2)
	if err != nil {
		t.Fatal(err)
	}
	if *calls != 3 || len(renderLogRows(page.Events)) != 2 || len(page.Events) != 5 {
		t.Fatalf("calls=%d events=%d rows=%#v", *calls, len(page.Events), renderLogRows(page.Events))
	}
}

func TestCloudHumanLogsDeduplicateOverlappingPages(t *testing.T) {
	tool := `{"event":"agent_progress","event_seq":10,"data":{"kind":"tool","text":"read"}}`
	pages := []string{
		logPage(tool, `"runtime_1"`),
		logPage(`{"event":"agent_progress","event_seq":8,"data":{"audience":"user","kind":"user_update","text":"Checking risk limits."}},`+tool, `"runtime_1"`),
		logPage(`{"event":"agent_progress","event_seq":7,"data":{"audience":"user","kind":"user_update","text":"Checking restart recovery."}}`, `"runtime_1"`),
	}
	client, calls := logPagingServer(t, pages, []string{"tail=2", "before_cp=901&before_rt=10&tail=1000", "before_cp=901&before_rt=8&tail=1000"})
	first, err := client.GetSessionLogPage("session_1", 2)
	if err != nil {
		t.Fatal(err)
	}
	page, err := expandCloudHumanLogs(client, "session_1", first, 2)
	if err != nil {
		t.Fatal(err)
	}
	if *calls != 3 || len(page.Events) != 3 || len(page.RawEvents) != 3 {
		t.Fatalf("calls=%d page=%#v", *calls, page)
	}
}

func TestCloudHumanLogsMergeSourcesByReceiptTime(t *testing.T) {
	client, _ := logPagingServer(t, []string{
		logPage(`{"event":"agent_progress","seq":20,"time":"2026-09-16T12:01:00Z","data":{"audience":"user","kind":"user_update","text":"Earlier control update."}},`+
			`{"event":"agent_progress","event_seq":30,"received_at":"2026-09-16T12:01:40Z","data":{"audience":"user","kind":"user_update","text":"Latest runtime update."}}`, `"runtime_1"`),
		logPage(`{"event":"agent_progress","event_seq":29,"received_at":"2026-09-16T12:01:30Z","data":{"audience":"user","kind":"user_update","text":"Middle runtime update."}}`, `"runtime_1"`),
	}, []string{"tail=3", "before_cp=20&before_rt=30&tail=1000"})
	first, err := client.GetSessionLogPage("session_1", 3)
	if err != nil {
		t.Fatal(err)
	}
	page, err := expandCloudHumanLogs(client, "session_1", first, 3)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Earlier control update.", "Middle runtime update.", "Latest runtime update."}
	for index, text := range want {
		if page.Events[index].Data["text"] != text || !strings.Contains(string(page.RawEvents[index]), text) {
			t.Fatalf("event/raw pairing or ordering at %d: %#v %s", index, page.Events[index], page.RawEvents[index])
		}
	}
}

func TestCloudHumanLogsKeepSnapshotWhenOlderHistoryIsUnavailable(t *testing.T) {
	initial := logPage(`{"event":"agent_progress","event_seq":10,"data":{"audience":"user","kind":"user_update","text":"Checking risk limits."}}`, `"runtime_1"`)
	for _, test := range []struct {
		name string
		next string
		want string
	}{
		{"unreachable", logPage(`{"event":"deployment.status","seq":1,"message":"partial"}`, "null"), "runtime could not be reached"},
		{"session changed", logPage(`{"event":"agent_progress","event_seq":1,"data":{"audience":"user","kind":"user_update","text":"New session."}}`, `"runtime_2"`), "session changed"},
		{"server ignored cursor", initial, "older activity page"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, calls := logPagingServer(t, []string{initial, test.next}, []string{"tail=2", "before_cp=901&before_rt=10&tail=1000"})
			first, err := client.GetSessionLogPage("session_1", 2)
			if err != nil {
				t.Fatal(err)
			}
			page, err := expandCloudHumanLogs(client, "session_1", first, 2)
			if err == nil || !strings.Contains(err.Error(), test.want) || *calls != 2 {
				t.Fatalf("calls=%d err=%v", *calls, err)
			}
			if len(page.Events) != 1 || page.Events[0].Data["text"] != "Checking risk limits." {
				t.Fatalf("partial or different session was merged: %#v", page.Events)
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
	page, err := expandCloudHumanLogs(client, "session_1", first, 2)
	if err != nil || *calls != 2 || len(page.Events) != 1 {
		t.Fatalf("calls=%d page=%#v err=%v", *calls, page, err)
	}
}

func TestCloudHumanLogsUseOneLegacyFallback(t *testing.T) {
	initialEvents := `{"event":"agent_progress","data":{"kind":"tool","text":"read"}},` +
		`{"event":"agent_progress","data":{"kind":"tool","text":"write"}}`
	for _, envelope := range []string{"", `,"cursors":{"rt":null,"cp":null,"session":"runtime_1"}`} {
		t.Run(envelope, func(t *testing.T) {
			initial := `{"events":[` + initialEvents + `]` + envelope + `}`
			full := `{"events":[{"event":"agent_progress","data":{"audience":"user","kind":"user_update","text":"Checking risk limits."}}]}`
			client, calls := logPagingServer(t, []string{initial, full}, []string{"tail=2", ""})
			first, err := client.GetSessionLogPage("session_1", 2)
			if err != nil {
				t.Fatal(err)
			}
			page, err := expandCloudHumanLogs(client, "session_1", first, 2)
			if err != nil || *calls != 2 || len(renderLogRows(page.Events)) != 1 {
				t.Fatalf("calls=%d page=%#v err=%v", *calls, page, err)
			}
		})
	}
}

func TestCloudHumanLogsDoNotReplaceSnapshotWithAnotherSessionDuringLegacyFallback(t *testing.T) {
	client, calls := logPagingServer(t, []string{
		logPage(`{"event":"agent_progress","event_seq":10,"data":{"audience":"user","kind":"user_update","text":"Original session."}}`, `"runtime_1"`),
		logPage(`{"event":"agent_progress","data":{"kind":"tool","text":"legacy"}}`, `"runtime_1"`),
		logPage(`{"event":"agent_progress","data":{"audience":"user","kind":"user_update","text":"New session."}}`, `"runtime_2"`),
	}, []string{"tail=2", "before_cp=901&before_rt=10&tail=1000", ""})
	first, err := client.GetSessionLogPage("session_1", 2)
	if err != nil {
		t.Fatal(err)
	}
	page, err := expandCloudHumanLogs(client, "session_1", first, 2)
	if err == nil || !strings.Contains(err.Error(), "session changed") || *calls != 3 || page.Events[0].Data["text"] != "Original session." {
		t.Fatalf("calls=%d page=%#v err=%v", *calls, page, err)
	}
}

func TestCloudRawAndJSONLogsKeepTheirSingleRequestContract(t *testing.T) {
	for _, raw := range []bool{false, true} {
		t.Run(fmt.Sprintf("raw=%v", raw), func(t *testing.T) {
			query := "tail=2"
			if raw {
				query = ""
			}
			client, calls := logPagingServer(t, []string{logPage(`{"event":"agent_progress","event_seq":1,"data":{"kind":"tool","text":"technical evidence"},"future_field":"preserved"}`, `"runtime_1"`)}, []string{query})
			configureCloudTest(t, client.Endpoint)
			t.Setenv("TELOS_CONTEXT", "")
			out := captureStdout(t, func() {
				printCloudSessionLogs(&cloud.SessionRecord{ID: "session_1", State: "healthy"}, logViewOptions{Tail: 2}, !raw, raw, "")
			})
			if *calls != 1 || !strings.Contains(out, "technical evidence") || raw && !strings.Contains(out, "future_field") {
				t.Fatalf("calls=%d output=%s", *calls, out)
			}
		})
	}
}

func TestCloudHumanLogsDoNotCallUnreachableRuntimeQuiet(t *testing.T) {
	client, _ := logPagingServer(t, []string{
		logPage(`{"event":"agent_progress","event_seq":10,"received_at":"2020-01-01T12:01:40Z","data":{"audience":"user","kind":"user_update","text":"Checking risk limits."}}`, `"runtime_1"`),
		logPage("", "null"),
	}, []string{"tail=2", "before_cp=901&before_rt=10&tail=1000"})
	configureCloudTest(t, client.Endpoint)
	t.Setenv("TELOS_CONTEXT", "")
	out := captureStdout(t, func() {
		printCloudSessionLogs(&cloud.SessionRecord{ID: "session_1", State: "working"}, logViewOptions{Tail: 2}, false, false, "")
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
				logPage(`{"event":"agent_progress","event_seq":10,"received_at":"2020-01-01T12:01:40Z","data":{"audience":"user","kind":"user_update","text":"Checking risk limits."}}`, `"runtime_1"`),
			}, []string{"tail=1"})
			configureCloudTest(t, client.Endpoint)
			t.Setenv("TELOS_CONTEXT", "")
			out := captureStdout(t, func() {
				printCloudSessionLogs(&cloud.SessionRecord{ID: "session_1", State: "running", Status: test.status}, logViewOptions{Tail: 1}, false, false, "")
			})
			if strings.Contains(out, "No new progress update") != test.quiet {
				t.Fatalf("status=%q output=%s", test.status, out)
			}
		})
	}
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

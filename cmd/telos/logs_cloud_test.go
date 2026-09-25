package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/telos-org/telos/internal/cloud"
)

func TestCloudSessionLogsPageBackwardUntilTailRowsExist(t *testing.T) {
	requestCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		query := r.URL.Query()
		if query.Get("tail") != "1000" {
			t.Fatalf("tail: got %q", query.Get("tail"))
		}

		events := make([]map[string]any, 0, maxCloudLogTail)
		switch requestCount {
		case 1:
			if query.Get("before_rt") != "" || query.Get("before_cp") != "" {
				t.Fatalf("initial query: got %q", r.URL.RawQuery)
			}
			for sequence := int64(1001); sequence <= 2000; sequence++ {
				events = append(events, map[string]any{
					"event":     "round_start",
					"event_seq": sequence,
				})
			}
			events[len(events)-1] = map[string]any{
				"event":     "agent_progress",
				"event_seq": 2000,
				"data": map[string]any{
					"kind": "progress_update",
					"text": "Newest visible row",
				},
			}
		case 2:
			if query.Get("before_rt") != "1001" || query.Get("before_cp") != "" {
				t.Fatalf("older query: got %q", r.URL.RawQuery)
			}
			events = append(events, map[string]any{
				"event":     "agent_progress",
				"event_seq": 1000,
				"data": map[string]any{
					"kind": "progress_update",
					"text": "Older visible row",
				},
			})
		default:
			t.Fatalf("unexpected request %d", requestCount)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"events": events})
	}))
	defer srv.Close()

	page, err := getCloudSessionLogPage(
		cloud.NewClient(srv.URL, "test-token"),
		"sess_123",
		logViewOptions{Tail: 2},
		false,
		false,
	)
	if err != nil {
		t.Fatalf("getCloudSessionLogPage: %v", err)
	}
	rows := renderLogRows(page.Events)
	if requestCount != 2 || len(rows) != 2 ||
		rows[0].Summary != "Older visible row" || rows[1].Summary != "Newest visible row" {
		t.Fatalf("requests=%d rows=%#v", requestCount, rows)
	}
}

func TestCloudSessionLogsFallBackOnceForKeylessHistory(t *testing.T) {
	requestCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		var events []map[string]any
		if requestCount == 1 {
			if r.URL.Query().Get("tail") != "1000" {
				t.Fatalf("initial query: got %q", r.URL.RawQuery)
			}
			events = make([]map[string]any, maxCloudLogTail)
			for index := range events {
				events[index] = map[string]any{"event": "round_start"}
			}
		} else {
			if requestCount != 2 || r.URL.RawQuery != "" {
				t.Fatalf("fallback query: request=%d query=%q", requestCount, r.URL.RawQuery)
			}
			events = []map[string]any{
				{"event": "agent_progress", "data": map[string]any{"text": "Older row"}},
				{"event": "agent_progress", "data": map[string]any{"text": "Newer row"}},
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"events": events})
	}))
	defer srv.Close()

	page, err := getCloudSessionLogPage(
		cloud.NewClient(srv.URL, "test-token"),
		"sess_123",
		logViewOptions{Tail: 2},
		false,
		false,
	)
	if err != nil {
		t.Fatalf("getCloudSessionLogPage: %v", err)
	}
	if rows := renderLogRows(page.Events); requestCount != 2 || len(rows) != 2 {
		t.Fatalf("requests=%d rows=%#v", requestCount, rows)
	}
}

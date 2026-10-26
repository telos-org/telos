package sessionapi

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/telos-org/telos/internal/evidence"
)

func TestActivityPagesAndStreamsDisplayRows(t *testing.T) {
	store, path := eventFixture(t, "")
	ev := evidence.New("pvg", path, eventSessionID, 1)
	progress := func(audience, text string) {
		ev.Log("agent_progress", 1, "prover", map[string]interface{}{"audience": audience, "text": text})
	}
	for i := 1; i <= 3; i++ {
		progress("agent", "tool call")
		progress("user", fmt.Sprintf("update %d", i))
		ev.LogAgent(1, "prover", "CONTINUE", "", nil)
	}
	mux := http.NewServeMux()
	RegisterRoutes(mux, store, AllowAllAuthorizer{}, RuntimeIdentity{})
	server := httptest.NewServer(mux)
	defer server.Close()
	url := fmt.Sprintf("%s/api/sessions/%s/activity", server.URL, eventSessionID)
	texts := func(rows []map[string]any) []any {
		var out []any
		for _, row := range rows {
			if row["kind"] != evidence.TurnEnded {
				out = append(out, row["text"])
			}
		}
		return out
	}

	for query, want := range map[string][]any{
		"?tail=2":          {"update 2", "update 3"},
		"?before=3&tail=2": {"update 1"},
	} {
		response, err := http.Get(url + query)
		if err != nil {
			t.Fatal(err)
		}
		var page ActivityPage
		err = json.NewDecoder(response.Body).Decode(&page)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(texts(page.Events)) != fmt.Sprint(want) || ptrOr(page.HeadEventSeq, 0) != 6 {
			t.Fatalf("%s: rows %v head %v, want %v head 6", query, texts(page.Events), page.HeadEventSeq, want)
		}
	}

	request, _ := http.NewRequest(http.MethodGet, url+"?after=4", nil)
	request.Header.Set("Accept", "text/event-stream")
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	next := func() any {
		t.Helper()
		for scanner.Scan() {
			if data, ok := bytes.CutPrefix(scanner.Bytes(), []byte("data: ")); ok {
				var row map[string]any
				if err := json.Unmarshal(data, &row); err != nil {
					t.Fatal(err)
				}
				if row["kind"] == evidence.TurnEnded {
					continue
				}
				return row["text"]
			}
		}
		t.Fatalf("stream ended: %v", scanner.Err())
		return nil
	}
	if text := next(); text != "update 3" {
		t.Fatalf("stream resumed at %v, want update 3", text)
	}
	progress("agent", "tool call")
	progress("user", "update 4")
	if text := next(); text != "update 4" {
		t.Fatalf("stream appended %v, want update 4", text)
	}
}

func TestActivityServedOnlyForLogsBegunWithIt(t *testing.T) {
	store, path := eventFixture(t, "")
	other := filepath.Join(filepath.Dir(path), "other.jsonl")
	mux := http.NewServeMux()
	RegisterRoutes(mux, store, AllowAllAuthorizer{}, RuntimeIdentity{})
	server := httptest.NewServer(mux)
	defer server.Close()
	get := func() (int, ActivityPage) {
		t.Helper()
		response, err := http.Get(fmt.Sprintf("%s/api/sessions/%s/activity", server.URL, eventSessionID))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var page ActivityPage
		_ = json.NewDecoder(response.Body).Decode(&page)
		return response.StatusCode, page
	}
	activity := func() bool {
		t.Helper()
		session, err := store.Get(eventSessionID)
		if err != nil {
			t.Fatal(err)
		}
		return session.ActivityRows
	}

	if !activity() {
		t.Fatal("a session with no events yet did not report activity")
	}
	if status, page := get(); status != http.StatusOK || len(page.Events) != 0 || page.HeadEventSeq != nil {
		t.Fatalf("status %d page %+v, want an empty page", status, page)
	}

	// An older runtime's events leave the session on raw events for good.
	if err := os.WriteFile(path, []byte(writerLine(1, "older runtime")), 0o644); err != nil {
		t.Fatal(err)
	}
	evidence.New("pvg", path, eventSessionID, 1).Log("agent_progress", 1, "prover", map[string]interface{}{"audience": "user", "text": "upgraded"})
	if activity() {
		t.Fatal("a session an older runtime began reported activity")
	}
	if status, _ := get(); status != http.StatusNotFound {
		t.Fatalf("older session status %d, want 404", status)
	}
	request, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/api/sessions/%s/activity", server.URL, eventSessionID), nil)
	request.Header.Set("Accept", "text/event-stream")
	if response, err := http.DefaultClient.Do(request); err != nil || response.StatusCode != http.StatusNotFound {
		t.Fatalf("older session stream %v (%v), want 404", response, err)
	} else {
		response.Body.Close()
	}

	// Older multi-spec sessions keep no single log either.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	manifest := ManifestFromInitial(InitialManifest{
		SessionID: eventSessionID, SpecName: "example",
		Specs: []InitialManifestSpec{
			{Name: "example", DirName: "example", EvidencePath: &path},
			{Name: "other", DirName: "other", EvidencePath: &other},
		},
	})
	if err := WriteManifest(filepath.Join(filepath.Dir(path), "session.json"), &manifest); err != nil {
		t.Fatal(err)
	}
	if activity() {
		t.Fatal("multi-spec session reported activity")
	}
}

func TestActivityRestoresTheFinalRowOfAStoppedWriter(t *testing.T) {
	store, path := eventFixture(t, "")
	evidence.New("pvg", path, eventSessionID, 1).Log("agent_progress", 1, "prover", map[string]interface{}{"audience": "user", "text": "first"})
	// The writer stopped after the session's final event, before its row.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(`{"event":"game_end","event_seq":2,"ts":"t","data":{"audience":"user","game_result":"success"}}` + "\n")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}

	page, err := store.Activity(eventSessionID, EventWindow{})
	if err != nil {
		t.Fatal(err)
	}
	var got []any
	for _, row := range page.Events {
		got = append(got, row["kind"])
	}
	if fmt.Sprint(got) != "[message evaluation_ended]" || ptrOr(page.HeadEventSeq, 0) != 2 {
		t.Fatalf("rows %v head %v, want the final evaluation_ended row", got, page.HeadEventSeq)
	}
}

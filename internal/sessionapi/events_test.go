package sessionapi

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/telos-org/telos/internal/evidence"
)

const eventSessionID = "sess_events"

func eventFixture(t testing.TB, body string) (*FileStore, string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, eventSessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "evidence.jsonl")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := ManifestFromInitial(InitialManifest{
		SessionID: eventSessionID, SpecName: "example",
		Specs: []InitialManifestSpec{{Name: "example", DirName: "example", EvidencePath: &path}},
	})
	if err := WriteManifest(filepath.Join(dir, "session.json"), &manifest); err != nil {
		t.Fatal(err)
	}
	return NewFileStore(root, RuntimeLocal), path
}

func writerLine(seq int, text string) string {
	return fmt.Sprintf("{\"schema\":\"telos.evidence.v2\",\"session_id\":%q,\"event_seq\":%d,\"event\":\"agent_progress\",\"data\":{\"text\":%q}}\n", eventSessionID, seq, text)
}

func writerHistory(count int) string {
	var body strings.Builder
	for seq := 1; seq <= count; seq++ {
		body.WriteString(writerLine(seq, strings.Repeat("x", 256)))
	}
	return body.String()
}

type guardedLogReader struct {
	io.ReaderAt
	middleEnd int64
	bytesRead int64
}

func (reader *guardedLogReader) ReadAt(buffer []byte, offset int64) (int, error) {
	if offset < reader.middleEnd && offset+int64(len(buffer)) > 4096 {
		return 0, errors.New("read touched unrequested historical data")
	}
	n, err := reader.ReaderAt.ReadAt(buffer, offset)
	reader.bytesRead += int64(n)
	return n, err
}

func TestColdTailReadsOnlyPrefixAndRequestedSuffix(t *testing.T) {
	for _, count := range []int{1000, 32000, 100000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			body := writerHistory(count)
			reader := &guardedLogReader{ReaderAt: strings.NewReader(body), middleEnd: int64(len(body)) - 128*1024}
			page, supported, err := readEvidenceWindow(reader, int64(len(body)), eventSessionID, &ManifestSpec{Name: "example"}, EventWindow{Tail: 200})
			if err != nil || !supported || len(page.Events) != 200 || *page.Events[0].EventSeq != int64(count-199) || *page.HeadEventSeq != int64(count) {
				t.Fatalf("cold tail: supported=%v, len=%d, err=%v", supported, len(page.Events), err)
			}
			if reader.bytesRead > 4096+128*1024 {
				t.Fatalf("read %d bytes for the last 200 events", reader.bytesRead)
			}
			t.Logf("%d events: read %d of %d bytes", count, reader.bytesRead, len(body))
		})
	}
}

func TestReverseWindowsMatchCompleteProjection(t *testing.T) {
	body := strings.ReplaceAll(writerHistory(1000), "\n", "\r\n") + "invalid json\nnull\n{\"data\":{}}\n"
	body += writerLine(1001, strings.Repeat("large\n世界", 20000)) + `{"event":"partial"`
	store, path := eventFixture(t, body)
	full, err := store.Events(eventSessionID)
	if err != nil {
		t.Fatal(err)
	}
	for _, window := range []EventWindow{
		{Tail: 200}, {After: 999}, {After: 1001}, {After: 9999},
		{Before: 802, Tail: 200}, {After: 990, Before: 999, Tail: 3},
		{After: 999, Before: 900}, {Tail: 2000},
	} {
		page, err := store.EventsPage(eventSessionID, window)
		if err != nil || !reflect.DeepEqual(page.Events, filterEventsWindow(full, window)) || !reflect.DeepEqual(page.HeadEventSeq, headEventSeq(full)) {
			t.Fatalf("window %+v changed projection, err=%v", window, err)
		}
	}
	// A replacement is read directly; there is no stale index to invalidate.
	if err := os.WriteFile(path, []byte(writerLine(1, "replacement")), 0o644); err != nil {
		t.Fatal(err)
	}
	page, err := store.EventsPage(eventSessionID, EventWindow{Tail: 200})
	if err != nil || len(page.Events) != 1 || page.Events[0].Data["text"] != "replacement" {
		t.Fatalf("replacement not reflected: %v", err)
	}
}

func TestReverseReaderFallbackAndErrors(t *testing.T) {
	for _, body := range []string{
		`{"event":"legacy"}` + "\n" + writerHistory(300),
		writerLine(1, "one") + writerLine(3, "three") + writerLine(2, "two"),
		writerLine(1000, "before reset") + writerHistory(300),
		writerLine(1, "one") + `{"event":"legacy append"}` + "\n",
		writerLine(1, "one") + strings.ReplaceAll(writerLine(2, "two"), eventSessionID, "other_session"),
	} {
		store, _ := eventFixture(t, body)
		full, err := store.Events(eventSessionID)
		if err != nil {
			t.Fatal(err)
		}
		page, err := store.EventsPage(eventSessionID, EventWindow{Tail: 200})
		if err != nil || !reflect.DeepEqual(page.Events, filterEventsWindow(full, EventWindow{Tail: 200})) || !reflect.DeepEqual(page.HeadEventSeq, headEventSeq(full)) {
			t.Fatalf("compatibility path changed: %v", err)
		}
	}
	// The JSON ends at the prefix limit, but the actual first line continues.
	prefix := strings.TrimSuffix(writerLine(1, "one"), "\n")
	body := strings.Repeat(" ", 64*1024-len(prefix)) + prefix + "invalid\n" + writerLine(2, "two")
	if seq, err := writerPrefix(strings.NewReader(body), int64(len(body)), eventSessionID); err != nil || seq != 0 {
		t.Fatalf("accepted an incomplete prefix: %d, %v", seq, err)
	}
	body = writerHistory(1000)
	reader := &guardedLogReader{ReaderAt: strings.NewReader(body), middleEnd: int64(len(body))}
	if _, _, err := readEvidenceWindow(reader, int64(len(body)), eventSessionID, &ManifestSpec{}, EventWindow{Tail: 200}); err == nil {
		t.Fatal("I/O failure was silently returned as a successful page")
	}
}

func TestRealWriterAppendAndPartialTail(t *testing.T) {
	store, path := eventFixture(t, "")
	writer := evidence.New("example", path, eventSessionID, 1)
	for range 3 {
		writer.Log("agent_progress", 1, "prover", map[string]any{"text": "hello"})
	}
	page, err := store.EventsPage(eventSessionID, EventWindow{Tail: 2})
	if err != nil || len(page.Events) != 2 || *page.HeadEventSeq != 3 || *page.Events[0].EventSeq != 2 {
		t.Fatalf("real writer tail: %+v, %v", page, err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	line := writerLine(4, "finished later")
	if _, err := io.WriteString(file, line[:len(line)/2]); err != nil {
		t.Fatal(err)
	}
	page, err = store.EventsPage(eventSessionID, EventWindow{After: 3})
	if err != nil || len(page.Events) != 0 || *page.HeadEventSeq != 3 {
		t.Fatalf("incomplete append leaked: %+v, %v", page, err)
	}
	if _, err := io.WriteString(file, line[len(line)/2:]); err != nil {
		t.Fatal(err)
	}
	page, err = store.EventsPage(eventSessionID, EventWindow{After: 3})
	if err != nil || len(page.Events) != 1 || *page.Events[0].EventSeq != 4 {
		t.Fatalf("completed append missing: %+v, %v", page, err)
	}
}

func TestLogReadPathsDoNotWaitForSummaryLock(t *testing.T) {
	store, _ := eventFixture(t, writerHistory(1000))
	mux := http.NewServeMux()
	RegisterRoutes(mux, store, AllowAllAuthorizer{}, RuntimeIdentity{})
	server := httptest.NewServer(mux)
	defer server.Close()
	store.mu.Lock()
	defer store.mu.Unlock()
	client := &http.Client{Timeout: time.Second}
	for _, path := range []string{"/api/sessions?include_evidence=false", "/api/sessions/" + eventSessionID + "/events?tail=200"} {
		response, err := client.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s: status %d", path, response.StatusCode)
		}
	}
	done := make(chan error, 1)
	go func() {
		_, err := store.Status(eventSessionID)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("stream status waited for an evidence summary")
	}
}

func TestMetadataLookupKeepsNormalUsageSummaries(t *testing.T) {
	store, _ := eventFixture(t, `{"event":"agent_complete","data":{"cost_usd":3}}`+"\n")
	mux := http.NewServeMux()
	RegisterRoutes(mux, store, AllowAllAuthorizer{}, RuntimeIdentity{})
	for _, fixture := range []struct {
		query  string
		status int
		cost   bool
	}{
		{"", 200, true}, {"?include_evidence=true", 200, true},
		{"?include_evidence=false", 200, false}, {"?include_evidence=invalid", 400, false},
	} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/sessions"+fixture.query, nil))
		if response.Code != fixture.status {
			t.Fatalf("%s: status %d", fixture.query, response.Code)
		}
		if response.Code == 200 {
			var list SessionListResponse
			if err := json.Unmarshal(response.Body.Bytes(), &list); err != nil || len(list.Sessions) != 1 {
				t.Fatalf("metadata list: %+v, %v", list, err)
			}
			if (list.Sessions[0].TotalCostUSD != nil) != fixture.cost {
				t.Fatalf("%s: incorrect evidence hydration", fixture.query)
			}
			if !list.Sessions[0].VisibleEvents {
				t.Fatalf("%s: sessions do not report ?visible support", fixture.query)
			}
		}
	}
}

type observedEventStore struct {
	*FileStore
	read chan struct{}
}

func (store *observedEventStore) EventsPage(id string, window EventWindow) (SessionEventsResponse, error) {
	page, err := store.FileStore.EventsPage(id, window)
	select {
	case store.read <- struct{}{}:
	default:
	}
	return page, err
}

func TestLiveStreamAppendsAndCompatibilityFallback(t *testing.T) {
	for _, futureCursor := range []bool{false, true} {
		t.Run(fmt.Sprintf("future_cursor=%v", futureCursor), func(t *testing.T) {
			fileStore, path := eventFixture(t, writerHistory(1000))
			store := &observedEventStore{FileStore: fileStore, read: make(chan struct{}, 1)}
			mux := http.NewServeMux()
			RegisterRoutes(mux, store, AllowAllAuthorizer{}, RuntimeIdentity{})
			server := httptest.NewServer(mux)
			defer server.Close()
			after := 998
			if futureCursor {
				after = 2000
			}
			request, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/api/sessions/%s/events?after=%d", server.URL, eventSessionID, after), nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Accept", "text/event-stream")
			client := &http.Client{Timeout: 10 * time.Second}
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("stream status: %d", response.StatusCode)
			}
			scanner := bufio.NewScanner(response.Body)
			nextEvent := func() SessionEvent {
				t.Helper()
				for scanner.Scan() {
					if data, ok := bytes.CutPrefix(scanner.Bytes(), []byte("data: ")); ok {
						var event SessionEvent
						if err := json.Unmarshal(data, &event); err != nil {
							t.Fatal(err)
						}
						return event
					}
				}
				t.Fatalf("stream ended before the next event: %v", scanner.Err())
				return SessionEvent{}
			}
			appendEvents := func(body string) {
				t.Helper()
				file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
				if err != nil {
					t.Fatal(err)
				}
				defer file.Close()
				if _, err := io.WriteString(file, body); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-store.read:
			case <-time.After(5 * time.Second):
				t.Fatal("stream did not read its first snapshot")
			}
			if futureCursor {
				// Reading an older head must not lower the client's cursor.
				appendEvents(writerLine(1001, "below cursor") + writerLine(2001, "above cursor"))
				if event := nextEvent(); ptrOr(event.EventSeq, 0) != 2001 {
					t.Fatalf("future cursor replayed an older event: %+v", event)
				}
				return
			}
			for _, seq := range []int64{999, 1000} {
				if event := nextEvent(); ptrOr(event.EventSeq, 0) != seq {
					t.Fatalf("initial stream event: %+v, want %d", event, seq)
				}
			}
			appendEvents(writerLine(1001, "new append"))
			if event := nextEvent(); ptrOr(event.EventSeq, 0) != 1001 {
				t.Fatalf("append missing or replayed: %+v", event)
			}
			appendEvents("{\"event\":\"legacy append\"}\n")
			// Losing durable sequences replays the complete compatibility
			// history once; a page length is not a full-history offset.
			for i := range 1002 {
				event := nextEvent()
				if event.EventSeq != nil || (i == 1001 && event.Event != "legacy append") {
					t.Fatalf("incorrect compatibility replay at %d: %+v", i, event)
				}
			}
			appendEvents("{\"event\":\"second legacy append\"}\n")
			if event := nextEvent(); event.Event != "second legacy append" {
				t.Fatalf("compatibility history replayed again: %+v", event)
			}
		})
	}
}

func BenchmarkColdTail(b *testing.B) {
	for _, count := range []int{1000, 32000, 100000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			store, _ := eventFixture(b, writerHistory(count))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := store.EventsPage(eventSessionID, EventWindow{Tail: 200}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkFullEventScan32000(b *testing.B) {
	store, _ := eventFixture(b, writerHistory(32000))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := store.Events(eventSessionID); err != nil {
			b.Fatal(err)
		}
	}
}

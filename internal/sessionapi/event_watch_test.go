package sessionapi_test

import (
	"bufio"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/telos-org/telos/internal/sessionapi"
)

func TestEventsStreamObservesAppendsAndManifestReplacement(t *testing.T) {
	srv, store := newTestServer(t)
	defer srv.Close()
	created := createSession(t, srv.URL, createSessionBody(t, "watched"))
	req, err := http.NewRequest("GET", srv.URL+"/api/sessions/"+created.SessionID+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("stream status: %d", resp.StatusCode)
	}
	events := make(chan string, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "data: ") {
				events <- scanner.Text()
			}
		}
	}()
	expect := func(name string) {
		t.Helper()
		select {
		case line := <-events:
			if !strings.Contains(line, `"event":"`+name+`"`) {
				t.Fatalf("unexpected event: %s", line)
			}
		case <-time.After(750 * time.Millisecond):
			t.Fatal("file change did not reach stream within 750ms")
		}
	}
	writeEventsFixture(t, store.Root, created.SessionID, "watched", []string{`{"event_seq":1,"event":"first"}`})
	expect("first")

	// The manifest is atomically replaced before the new evidence directory
	// exists. Both creation and subsequent appends must remain observable.
	manifestPath := filepath.Join(store.Root, created.SessionID, "session.json")
	manifest, err := sessionapi.ReadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	next := filepath.Join(store.Root, created.SessionID, "replacement", "nested", "events.jsonl")
	manifest.Specs[0].EvidencePath = &next
	if err := sessionapi.WriteManifest(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(next), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(next, []byte(`{"event_seq":2,"event":"second"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	expect("second")
	file, err := os.OpenFile(next, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteString(`{"event_seq":3,"event":"third"}` + "\n")
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	expect("third")
	stopSession(t, srv.URL, created.SessionID)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("terminal manifest change did not end stream")
	}
}

func TestEventWatchCloseReleasesSubscriber(t *testing.T) {
	srv, store := newTestServer(t)
	defer srv.Close()
	created := createSession(t, srv.URL, createSessionBody(t, "watchclose"))
	changed, closeWatch, err := store.WatchEvents(created.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	closeWatch()
	closeWatch()
	select {
	case _, ok := <-changed:
		if ok {
			t.Fatal("watch remained open after close")
		}
	case <-time.After(time.Second):
		t.Fatal("watch cleanup did not finish")
	}
}

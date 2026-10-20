package sessionapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type trackedJournalStore struct {
	*FileStore
	watching chan struct{}
	stopped  chan struct{}
}

func (store *trackedJournalStore) WatchEvents(id string) (<-chan struct{}, func(), error) {
	changed, stop, err := store.FileStore.WatchEvents(id)
	if err != nil {
		return nil, nil, err
	}
	close(store.watching)
	return changed, func() {
		stop()
		close(store.stopped)
	}, nil
}

func TestJournalContentionDoesNotRetainCanceledStream(t *testing.T) {
	fileStore, _ := eventFixture(t, "{\"event\":\"legacy\"}\n")
	if _, err := fileStore.Events(eventSessionID); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(fileStore.eventPositionsPath(eventSessionID)+".lock", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	store := &trackedJournalStore{fileStore, make(chan struct{}), make(chan struct{})}
	mux := http.NewServeMux()
	RegisterRoutes(mux, store, AllowAllAuthorizer{}, RuntimeIdentity{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/api/sessions/"+eventSessionID+"/events", nil).WithContext(ctx)
	request.Header.Set("Accept", "text/event-stream")
	done := make(chan struct{})
	go func() {
		defer close(done)
		mux.ServeHTTP(httptest.NewRecorder(), request)
	}()
	select {
	case <-store.watching:
	case <-time.After(time.Second):
		t.Fatal("stream did not subscribe")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("canceled stream remained blocked on an external journal lock")
	}
	select {
	case <-store.stopped:
	default:
		t.Fatal("stream retained its activity watcher")
	}
	if !store.mu.TryLock() {
		t.Fatal("journal contention retained the whole-store mutex")
	}
	store.mu.Unlock()
}

func TestLegacyAppendWorkIsBoundedAfterRestart(t *testing.T) {
	line := fmt.Sprintf("{\"event\":\"progress\",\"data\":{\"text\":%q}}\n", strings.Repeat("x", 256))
	store, path := eventFixture(t, strings.Repeat(line, 10000))
	if _, err := store.Events(eventSessionID); err != nil {
		t.Fatal(err)
	}
	journalPath := store.eventPositionsPath(eventSessionID) + "l"
	before, err := os.Stat(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	var failure error
	allocations := testing.AllocsPerRun(3, func() {
		_, failure = source.WriteString(line)
		if failure != nil {
			return
		}
		// A fresh reader must use the persisted offsets, not an in-memory cache.
		store = NewFileStore(store.Root, RuntimeLocal)
		var page SessionEventsResponse
		page, failure = store.EventsPage(eventSessionID, EventWindow{Tail: 1})
		if failure == nil && len(page.Events) != 1 {
			failure = fmt.Errorf("tail contains %d events", len(page.Events))
		}
	})
	if failure != nil {
		t.Fatal(failure)
	}
	if allocations > 2000 {
		t.Fatalf("one-record append/tail allocated %.0f objects; old history was materialized", allocations)
	}
	after, err := os.Stat(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) || after.Size()-before.Size() > 4096 {
		t.Fatal("append replaced or rewrote the journal instead of extending it")
	}
	body, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	reader := &guardedLogReader{ReaderAt: strings.NewReader(string(body)), middleEnd: after.Size() - 128*1024}
	if _, err := readJournalWindow(reader, after.Size(), 10004, EventWindow{Tail: 1}); err != nil {
		t.Fatalf("tail read old journal history: %v", err)
	}
	t.Logf("one append plus tail: %.0f allocations", allocations)
}

func TestLegacyJournalRecoversIncompleteAppendAndReplacedSource(t *testing.T) {
	store, path := eventFixture(t, "{\"event\":\"original\"}\n")
	if _, err := store.Events(eventSessionID); err != nil {
		t.Fatal(err)
	}
	journalPath := store.eventPositionsPath(eventSessionID) + "l"
	journal, err := os.OpenFile(journalPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.WriteString(journal, "{\"event\":\"uncommitted\",\"event_seq\":2}\n")
	journal.Close()
	if err != nil {
		t.Fatal(err)
	}
	// A crash before checkpoint publication must not expose or retain this tail.
	store = NewFileStore(store.Root, RuntimeLocal)
	page, err := store.EventsPage(eventSessionID, EventWindow{After: 1})
	if err != nil || len(page.Events) != 0 || ptrOr(page.HeadEventSeq, 0) != 1 {
		t.Fatalf("uncommitted append survived: %+v, %v", page, err)
	}
	for _, replace := range []bool{false, true} {
		body := "{\"event\":\"original\"}\n{\"event\":\"new\"}\n{\"event\":\"partial\""
		if replace {
			if err := os.WriteFile(path+".new", []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(path+".new", path); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		store = NewFileStore(store.Root, RuntimeLocal)
		page, err = store.EventsPage(eventSessionID, EventWindow{After: 1})
		if err != nil || len(page.Events) != 1 || page.Events[0].Event != "new" || ptrOr(page.HeadEventSeq, 0) != 2 {
			t.Fatalf("replacement changed published history: %+v, %v", page, err)
		}
	}
	source, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.WriteString("}\n")
	source.Close()
	if err != nil {
		t.Fatal(err)
	}
	page, err = store.EventsPage(eventSessionID, EventWindow{After: 2})
	if err != nil || len(page.Events) != 1 || page.Events[0].Event != "partial" || ptrOr(page.HeadEventSeq, 0) != 3 {
		t.Fatalf("completed partial record lost or duplicated: %+v, %v", page, err)
	}
	if err := os.WriteFile(path, []byte("{\"event\":\"truncated\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	page, err = store.EventsPage(eventSessionID, EventWindow{})
	if err != nil || len(page.Events) != 4 || page.Events[0].Event != "original" || page.Events[3].Event != "truncated" || ptrOr(page.HeadEventSeq, 0) != 4 {
		t.Fatalf("truncation discarded or reassigned history: %+v, %v", page, err)
	}
	// A valid EOF record is supported, but subsequent bytes are not a new
	// physical record unless a newline separates them.
	if err := os.WriteFile(path, []byte("{\"event\":\"valid-eof\"}"), 0o600); err != nil {
		t.Fatal(err)
	}
	page, err = store.EventsPage(eventSessionID, EventWindow{After: 4})
	if err != nil || len(page.Events) != 1 || ptrOr(page.HeadEventSeq, 0) != 5 {
		t.Fatalf("valid EOF record missing: %+v, %v", page, err)
	}
	source, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.WriteString("{\"event\":\"not-a-separate-line\"}\n")
	source.Close()
	if err != nil {
		t.Fatal(err)
	}
	page, err = store.EventsPage(eventSessionID, EventWindow{After: 5})
	if err != nil || len(page.Events) != 0 || ptrOr(page.HeadEventSeq, 0) != 5 {
		t.Fatalf("continuation of EOF record became a new event: %+v, %v", page, err)
	}
}

func TestMultiSpecPositionsSurviveEarlierSourceAppendAndRestart(t *testing.T) {
	store, alpha := eventFixture(t, "{\"event\":\"alpha\",\"event_seq\":1}\n")
	manifest, err := ReadManifest(store.manifestPath(eventSessionID))
	if err != nil {
		t.Fatal(err)
	}
	beta := filepath.Join(store.sessionDir(eventSessionID), "beta.jsonl")
	if err := os.WriteFile(beta, []byte("{\"event\":\"beta\",\"event_seq\":2}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest.Specs = append(manifest.Specs, ManifestSpec{Name: "beta", DirName: "beta", EvidencePath: &beta})
	if err := WriteManifest(store.manifestPath(eventSessionID), manifest); err != nil {
		t.Fatal(err)
	}
	first, err := store.EventsPage(eventSessionID, EventWindow{Tail: 10})
	if err != nil || len(first.Events) != 2 || ptrOr(first.HeadEventSeq, 0) != 2 {
		t.Fatalf("initial positions: %+v, %v", first, err)
	}
	// Two identical legacy events must both survive, even though they were
	// appended to the spec that precedes beta in the manifest.
	file, err := os.OpenFile(alpha, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteString("{\"event\":\"duplicate\"}\n{\"event\":\"duplicate\"}\n")
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		store = NewFileStore(store.Root, RuntimeLocal)
		page, err := store.EventsPage(eventSessionID, EventWindow{After: 2})
		if err != nil || len(page.Events) != 2 || ptrOr(page.HeadEventSeq, 0) != 4 {
			t.Fatalf("resume after restart: %+v, %v", page, err)
		}
		for i, event := range page.Events {
			if event.Event != "duplicate" || ptrOr(event.EventSeq, 0) != int64(i+3) {
				t.Fatalf("missing or duplicated event: %+v", event)
			}
		}
		old, err := store.EventsPage(eventSessionID, EventWindow{Before: 3})
		if err != nil || len(old.Events) != 2 || old.Events[1].Event != "beta" {
			t.Fatalf("immutable history moved: %+v, %v", old, err)
		}
	}
}

func TestIndependentReadersNeverReassignPublishedPositions(t *testing.T) {
	store, alpha := eventFixture(t, "{\"event\":\"alpha\"}\n")
	manifest, err := ReadManifest(store.manifestPath(eventSessionID))
	if err != nil {
		t.Fatal(err)
	}
	beta := filepath.Join(store.sessionDir(eventSessionID), "beta.jsonl")
	if err := os.WriteFile(beta, []byte("{\"event\":\"beta\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest.Specs = append(manifest.Specs, ManifestSpec{Name: "beta", DirName: "beta", EvidencePath: &beta})
	if err := WriteManifest(store.manifestPath(eventSessionID), manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Events(eventSessionID); err != nil {
		t.Fatal(err)
	}
	snapshots := make(chan []SessionEvent, 40)
	failures := make(chan error, 3)
	var workers sync.WaitGroup
	for range 2 {
		workers.Go(func() {
			reader := NewFileStore(store.Root, RuntimeLocal)
			for range 20 {
				events, err := reader.Events(eventSessionID)
				if err != nil {
					failures <- err
					return
				}
				snapshots <- events
				runtime.Gosched()
			}
		})
	}
	workers.Go(func() {
		for i := range 100 {
			file, err := os.OpenFile([]string{alpha, beta}[i%2], os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				failures <- err
				return
			}
			_, err = fmt.Fprintf(file, "{\"event\":\"append-%d\"}\n", i)
			file.Close()
			if err != nil {
				failures <- err
				return
			}
			runtime.Gosched()
		}
	})
	workers.Wait()
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	final, err := NewFileStore(store.Root, RuntimeLocal).Events(eventSessionID)
	if err != nil || len(final) != 102 {
		t.Fatalf("final history: %d events, %v", len(final), err)
	}
	close(snapshots)
	for snapshot := range snapshots {
		for _, event := range snapshot {
			seq := ptrOr(event.EventSeq, 0)
			if seq < 1 || seq > int64(len(final)) || final[seq-1].Event != event.Event {
				t.Fatalf("published cursor reassigned: %+v", event)
			}
		}
	}
}

func BenchmarkLegacyActivityAppend(b *testing.B) {
	for _, count := range []int{10000, 100000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			line := fmt.Sprintf("{\"event\":\"agent_progress\",\"data\":{\"text\":%q}}\n", strings.Repeat("x", 256))
			store, path := eventFixture(b, strings.Repeat(line, count))
			if _, err := store.Events(eventSessionID); err != nil {
				b.Fatal(err)
			}
			file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				b.Fatal(err)
			}
			defer file.Close()
			b.ResetTimer()
			for range b.N {
				if _, err := file.WriteString(line); err != nil {
					b.Fatal(err)
				}
				if _, err := store.EventsPage(eventSessionID, EventWindow{Tail: 1}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

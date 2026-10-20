package sessionapi

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

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

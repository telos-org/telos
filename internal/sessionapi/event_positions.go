package sessionapi

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

func (fs *FileStore) eventPositionsPath(id string) string {
	return filepath.Join(fs.sessionDir(id), "event-positions.json")
}

// sequenceEvents gives legacy and multi-spec evidence the same durable cursor
// contract as modern single-file logs. Once created, the journal is append-only:
// an append to an earlier spec cannot move events already served to clients.
// An OS lock also serializes independent CLI readers and server processes.
// An atomic, synced replacement precedes publication.
func (fs *FileStore) sequenceEvents(id string, events []SessionEvent, multipleSpecs bool) ([]SessionEvent, error) {
	path := fs.eventPositionsPath(id)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) && !multipleSpecs && durableEventSeqs(events) {
		return events, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return nil, err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	// Another reader may have committed positions while this one waited.
	data, err = os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	var journal []SessionEvent
	if err == nil {
		if err := json.Unmarshal(data, &journal); err != nil {
			return nil, err
		}
	}
	// Counts retain identical repeated events while ignoring the source's
	// local sequence, which may collide across specs or be absent entirely.
	identity := func(event SessionEvent) ([32]byte, error) {
		event.EventSeq = nil
		encoded, err := json.Marshal(event)
		return sha256.Sum256(encoded), err
	}
	known := map[[32]byte]int{}
	for _, event := range journal {
		key, err := identity(event)
		if err != nil {
			return nil, err
		}
		known[key]++
	}
	original := len(journal)
	var sequence int64
	if original > 0 {
		sequence = ptrOr(journal[original-1].EventSeq, 0)
	}
	seen := map[[32]byte]int{}
	for _, event := range events {
		key, err := identity(event)
		if err != nil {
			return nil, err
		}
		seen[key]++
		if seen[key] <= known[key] {
			continue
		}
		// Preserve any previously advertised modern prefix during conversion.
		sequence = max(sequence+1, ptrOr(event.EventSeq, 0))
		position := sequence
		event.EventSeq = &position
		journal = append(journal, event)
	}
	if len(journal) != original {
		data, err := json.Marshal(journal)
		if err != nil {
			return nil, err
		}
		if err := writeFileAtomic(path, data, 0o600); err != nil {
			return nil, err
		}
	}
	return journal, nil
}

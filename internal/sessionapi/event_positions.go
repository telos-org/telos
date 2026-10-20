package sessionapi

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"syscall"

	"github.com/telos-org/telos/internal/evidence"
)

type sourcePosition struct {
	Offset       int64    `json:"offset"`
	FileID       string   `json:"file_id"`
	Anchor       [32]byte `json:"anchor"`
	Unterminated bool     `json:"unterminated,omitempty"`
}

type eventPositions struct {
	Size    int64                     `json:"size"`
	Head    int64                     `json:"head"`
	Sources map[string]sourcePosition `json:"sources"`
}

type eventSource struct {
	file     *os.File
	spec     *ManifestSpec
	key      string
	size     int64
	position sourcePosition
}

func (fs *FileStore) eventPositionsPath(id string) string {
	return filepath.Join(fs.sessionDir(id), "event-positions.json")
}

// journalEvents commits append-only cursor history before publishing it. The
// small checkpoint commits both source offsets and journal length; a crashed
// ingestion can leave only an uncommitted suffix, which the next reader removes.
func (fs *FileStore) journalEvents(id string, manifest *Manifest, window EventWindow) (SessionEventsResponse, error) {
	path := fs.eventPositionsPath(id)
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return SessionEventsResponse{}, err
	}
	defer lock.Close()
	operation := syscall.LOCK_EX
	if window.tryLock {
		operation |= syscall.LOCK_NB
	}
	if err := syscall.Flock(int(lock.Fd()), operation); err != nil {
		return SessionEventsResponse{}, fmt.Errorf("acquire event journal: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	positions := eventPositions{Sources: map[string]sourcePosition{}}
	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, &positions); err != nil {
			return SessionEventsResponse{}, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return SessionEventsResponse{}, err
	}
	journal, err := os.OpenFile(path+"l", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return SessionEventsResponse{}, err
	}
	defer journal.Close()
	info, err := journal.Stat()
	if err != nil {
		return SessionEventsResponse{}, err
	}
	if info.Size() < positions.Size {
		return SessionEventsResponse{}, fmt.Errorf("event journal is shorter than its committed checkpoint")
	}
	if info.Size() > positions.Size {
		if err := journal.Truncate(positions.Size); err != nil {
			return SessionEventsResponse{}, err
		}
	}
	if _, err := journal.Seek(positions.Size, io.SeekStart); err != nil {
		return SessionEventsResponse{}, err
	}
	if err := updateEventPositions(path, manifest, journal, &positions); err != nil {
		return SessionEventsResponse{}, err
	}
	return readJournalWindow(journal, positions.Size, positions.Head, window)
}

func updateEventPositions(path string, manifest *Manifest, journal *os.File, positions *eventPositions) error {
	var sources []eventSource
	reset := false
	for i := range manifest.Specs {
		source, changed, err := openEventSource(&manifest.Specs[i], positions.Sources)
		if err != nil {
			return err
		}
		if source != nil {
			defer source.file.Close()
			sources = append(sources, *source)
			reset = reset || changed
		}
	}
	var known map[[32]byte]int
	var err error
	if reset && positions.Size > 0 {
		known, err = journalIdentities(journal, positions.Size)
		if err != nil {
			return err
		}
	}
	writer := bufio.NewWriter(journal)
	encoder := json.NewEncoder(writer)
	changed := false
	for _, source := range sources {
		if reset {
			source.position.Offset = 0
		}
		if err := source.appendEvents(encoder, &positions.Head, known); err != nil {
			return err
		}
		source.position.Anchor, err = sourceAnchor(source.file, source.position.Offset)
		if err != nil {
			return err
		}
		changed = changed || positions.Sources[source.key] != source.position
		positions.Sources[source.key] = source.position
	}
	if changed && positions.Head > 0 {
		if err := writer.Flush(); err != nil {
			return err
		}
		if err := journal.Sync(); err != nil {
			return err
		}
		positions.Size, err = journal.Seek(0, io.SeekCurrent)
		if err != nil {
			return err
		}
		data, err := json.Marshal(positions)
		if err != nil {
			return err
		}
		if err := writeFileAtomic(path, data, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func openEventSource(spec *ManifestSpec, positions map[string]sourcePosition) (*eventSource, bool, error) {
	if spec.EvidencePath == nil || *spec.EvidencePath == "" {
		return nil, false, nil
	}
	file, err := os.Open(*spec.EvidencePath)
	if err != nil {
		return nil, false, nil // Missing evidence does not remove published history.
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, false, err
	}
	stat := info.Sys().(*syscall.Stat_t)
	key := fmt.Sprintf("%s\x00%s\x00%s\x00%d", *spec.EvidencePath, spec.Name, spec.DirName, ptrOr(spec.Index, -1))
	position, exists := positions[key]
	fileID := fmt.Sprintf("%d:%d", stat.Dev, stat.Ino)
	anchor, err := sourceAnchor(file, min(position.Offset, info.Size()))
	if err != nil {
		file.Close()
		return nil, false, err
	}
	reset := !exists || position.FileID != fileID || position.Offset > info.Size() || position.Anchor != anchor ||
		(position.Unterminated && info.Size() > position.Offset)
	position.FileID = fileID
	return &eventSource{file, spec, key, info.Size(), position}, reset, nil
}

// Check the committed source boundary as well as inode/size, so truncation and
// replacement trigger reconciliation while normal appends inspect fixed bytes.
func sourceAnchor(file io.ReaderAt, offset int64) ([32]byte, error) {
	data := make([]byte, min(offset, 256))
	_, err := file.ReadAt(data, offset-int64(len(data)))
	return sha256.Sum256(data), err
}

func journalIdentity(event SessionEvent) ([32]byte, error) {
	event.EventSeq = nil
	data, err := json.Marshal(event)
	return sha256.Sum256(data), err
}

// Only a replaced/truncated source requires reconciling historical identities.
// Counts preserve identical repeated records without reassigning old cursors.
func journalIdentities(journal io.ReaderAt, size int64) (map[[32]byte]int, error) {
	known := map[[32]byte]int{}
	decoder := json.NewDecoder(io.NewSectionReader(journal, 0, size))
	for {
		var event SessionEvent
		if err := decoder.Decode(&event); err != nil {
			if errors.Is(err, io.EOF) {
				return known, nil
			}
			return nil, err
		}
		key, err := journalIdentity(event)
		if err != nil {
			return nil, err
		}
		known[key]++
	}
}

func (source *eventSource) appendEvents(encoder *json.Encoder, head *int64, known map[[32]byte]int) error {
	reader := bufio.NewReader(io.NewSectionReader(source.file, source.position.Offset, source.size-source.position.Offset))
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		var raw map[string]any
		valid := json.Unmarshal(line, &raw) == nil
		if errors.Is(err, io.EOF) && !valid {
			return nil // Retry an incomplete trailing record on the next append.
		}
		source.position.Offset += int64(len(line))
		source.position.Unterminated = errors.Is(err, io.EOF)
		event, ok := projectEvidenceEvent(raw, source.spec)
		if ok {
			if known != nil {
				key, err := journalIdentity(event)
				if err != nil {
					return err
				}
				if known[key] > 0 {
					known[key]--
					continue
				}
			}
			*head = max(*head+1, ptrOr(event.EventSeq, 0))
			sequence := *head
			event.EventSeq = &sequence
			if err := encoder.Encode(event); err != nil {
				return err
			}
		}
		if err != nil {
			return nil
		}
	}
}

func readJournalWindow(reader io.ReaderAt, size, head int64, window EventWindow) (SessionEventsResponse, error) {
	page := SessionEventsResponse{Events: []SessionEvent{}}
	if head > 0 {
		page.HeadEventSeq = &head
	}
	err := evidence.VisitRawLinesReverse(reader, size, func(line []byte) bool {
		var event SessionEvent
		if json.Unmarshal(line, &event) != nil {
			return false
		}
		sequence := ptrOr(event.EventSeq, 0)
		if window.After > 0 && sequence <= window.After {
			return true
		}
		if window.Before > 0 && sequence >= window.Before {
			return false
		}
		page.Events = append(page.Events, event)
		return window.Tail > 0 && int64(len(page.Events)) >= window.Tail
	})
	slices.Reverse(page.Events)
	return page, err
}

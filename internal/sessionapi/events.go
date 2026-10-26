//go:build unix

package sessionapi

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/telos-org/telos/internal/evidence"
)

type EventWindow struct {
	After  int64
	Before int64
	Tail   int64
}

// EventsPage reads modern single-file logs backward. Their writer serializes
// appends and stamps increasing sequence numbers; bounded reads rely on that
// invariant, rather than validating unseen history. Legacy or multi-spec logs
// and anomalies encountered while reading retain the full compatibility path.
func (fs *FileStore) EventsPage(id string, window EventWindow) (SessionEventsResponse, error) {
	if window.After > 0 || window.Before > 0 || window.Tail > 0 {
		// Manifests are replaced atomically. Do not wait for fs.mu: a session
		// summary holding that lock may be scanning a large evidence file.
		manifest, err := ReadManifest(fs.manifestPath(id))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return SessionEventsResponse{}, fmt.Errorf("session %s: %w", id, ErrNotFound)
			}
			return SessionEventsResponse{}, err
		}
		if len(manifest.Specs) == 1 && manifest.Specs[0].EvidencePath != nil {
			spec := &manifest.Specs[0]
			file, err := os.Open(*spec.EvidencePath)
			if err == nil {
				defer file.Close()
				info, err := file.Stat()
				if err != nil {
					return SessionEventsResponse{}, err
				}
				page, supported, err := readEvidenceWindow(file, info.Size(), id, spec, window)
				if err != nil || supported {
					return page, err
				}
			}
		}
	}
	events, err := fs.Events(id)
	if err != nil {
		return SessionEventsResponse{}, err
	}
	return SessionEventsResponse{
		Events:       filterEventsWindow(events, window.After, window.Before, window.Tail),
		HeadEventSeq: headEventSeq(events),
	}, nil
}

func writerSequence(raw map[string]any, sessionID string) (int64, bool) {
	seq := eventSeq(raw)
	return ptrOr(seq, 0), raw["schema"] == evidence.SchemaVersion &&
		raw["session_id"] == sessionID && seq != nil && *seq > 0
}

// A small prefix check excludes logs upgraded from a sequence-less format.
// Unknown or oversized prefixes use the existing compatibility reader.
func writerPrefix(reader io.ReaderAt, size int64, sessionID string) (int64, error) {
	const prefixLimit = 64 * 1024
	lines := bufio.NewReader(io.NewSectionReader(reader, 0, min(size, prefixLimit)))
	for {
		line, err := lines.ReadBytes('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return 0, err
		}
		if errors.Is(err, io.EOF) && size > prefixLimit {
			return 0, nil // The artificial boundary is not a complete record.
		}
		var raw map[string]any
		if json.Unmarshal(bytes.TrimSpace(line), &raw) == nil {
			if event, ok := raw["event"].(string); ok && event != "" {
				seq, supported := writerSequence(raw, sessionID)
				if supported {
					return seq, nil
				}
				return 0, nil
			}
		}
		if err != nil {
			return 0, nil
		}
	}
}

func readEvidenceWindow(reader io.ReaderAt, size int64, sessionID string, spec *ManifestSpec, window EventWindow) (SessionEventsResponse, bool, error) {
	page := SessionEventsResponse{Events: []SessionEvent{}}
	firstSeq, err := writerPrefix(reader, size, sessionID)
	if err != nil || firstSeq == 0 {
		return page, false, err
	}
	supported := true
	var previous int64
	err = evidence.VisitLinesReverse(reader, size, func(raw map[string]any) bool {
		event, ok := projectEvidenceEvent(raw, spec)
		if !ok {
			return false
		}
		seq, valid := writerSequence(raw, sessionID)
		if !valid || seq < firstSeq || (previous > 0 && seq >= previous) {
			supported = false
			return true
		}
		if page.HeadEventSeq == nil {
			page.HeadEventSeq = &seq
		}
		previous = seq
		if window.After > 0 && seq <= window.After {
			return true
		}
		if window.Before > 0 && seq >= window.Before {
			return false
		}
		page.Events = append(page.Events, event)
		return window.Tail > 0 && int64(len(page.Events)) >= window.Tail
	})
	slices.Reverse(page.Events)
	return page, supported, err
}

// Status reads only the manifest and runner state, without deriving usage totals.
func (fs *FileStore) Status(id string) (SessionStatus, error) {
	manifest, err := ReadManifest(fs.manifestPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("session %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return "", err
	}
	return deriveStatus(fs.sessionDir(id), manifest), nil
}

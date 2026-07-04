package executor

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sync"
)

var errFileChangedOnDisk = errors.New("file changed on disk since it was last read")

type fileTracker struct {
	mu       sync.Mutex
	baseline map[string]string
}

func newFileTracker() *fileTracker {
	return &fileTracker{baseline: map[string]string{}}
}

func (t *fileTracker) record(full string, content []byte) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.baseline[full] = hashContent(content)
	t.mu.Unlock()
}

func (t *fileTracker) forget(full string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	delete(t.baseline, full)
	t.mu.Unlock()
}

func (t *fileTracker) check(full string) error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	hash, ok := t.baseline[full]
	t.mu.Unlock()
	if !ok {
		return nil
	}
	current, err := os.ReadFile(full)
	if err != nil {
		if os.IsNotExist(err) {
			return errFileChangedOnDisk
		}
		return err
	}
	if hashContent(current) != hash {
		return errFileChangedOnDisk
	}
	return nil
}

func hashContent(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func staleWriteError(rel string) error {
	return fmt.Errorf("%s changed since it was last read; re-read the file before writing so newer content is not overwritten", rel)
}

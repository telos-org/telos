package sessionapi

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// AcquireExecution locks a session while its agents run. The agent
// inherits the file, so a worker crash does not release a surviving agent's lock.
func AcquireExecution(sessionDir string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(sessionDir, "execution.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("lock session execution: %w", err)
	}
	return f, nil
}

func executionRunning(sessionDir string, m *Manifest) bool {
	f, err := os.Open(filepath.Join(sessionDir, "execution.lock"))
	if errors.Is(err, os.ErrNotExist) {
		// Workers started by an older runtime do not hold an execution lock.
		return m.OpenEpoch() != nil
	}
	if err != nil {
		return true
	}
	defer f.Close()
	return syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB) != nil
}

package sessionapi

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Retirement serializes with manifest publication, inference admission, and
// worker ownership across processes. Cached defaults cannot revive a retired ID.
func withInferenceConnections(root string, action func() error) error {
	dir := filepath.Join(root, ".inference-connections")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return action()
}

func requireLiveInferenceConnections(root string, m *Manifest) error {
	connections := []*InferenceConnection{m.InferenceConnection}
	if update := m.InferenceUpdate; update != nil && (update.Status == "pending" || update.Status == "applying") {
		connections = append(connections, update.Connection)
	}
	for _, connection := range connections {
		if connection == nil {
			continue
		}
		if !inferenceRequestID.MatchString(connection.ID) {
			return fmt.Errorf("%w: invalid inference connection ID", ErrInvalidSession)
		}
		_, err := os.Stat(filepath.Join(root, ".inference-connections", connection.ID+".retired"))
		if err == nil {
			return fmt.Errorf("%w: inference connection %s has retired; reload current settings", ErrConflict, connection.ID)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func writeInferenceManifest(path string, m *Manifest) error {
	root := filepath.Dir(filepath.Dir(path))
	return withInferenceConnections(root, func() error {
		if err := requireLiveInferenceConnections(root, m); err != nil {
			return err
		}
		return WriteManifest(path, m)
	})
}

// MutateInferenceManifest admits new connection use under the retirement lock.
// Workers call this while holding runner.lock, before starting any agent work.
func MutateInferenceManifest(path string, mutate func(*Manifest) error) (*Manifest, error) {
	root := filepath.Dir(filepath.Dir(path))
	var manifest *Manifest
	err := withInferenceConnections(root, func() error {
		var err error
		manifest, err = MutateManifest(path, func(m *Manifest) error {
			if err := mutate(m); err != nil {
				return err
			}
			return requireLiveInferenceConnections(root, m)
		})
		return err
	})
	return manifest, err
}

func supportsInferenceRetirement(m *Manifest) bool {
	runner := m.Runner
	if runner == nil {
		if epoch := m.OpenEpoch(); epoch != nil {
			runner = epoch.Runner
		}
	}
	return runner != nil && runner.InferenceRetirement
}

type InferenceRetirement struct {
	ConnectionID string `json:"connection_id"`
	Retired      bool   `json:"retired"`
}

func (fs *FileStore) RetireInferenceConnection(id, connectionID string) (*InferenceRetirement, error) {
	if !safeSessionID(id) || !inferenceRequestID.MatchString(connectionID) {
		return nil, ErrInvalidSession
	}
	err := withInferenceConnections(fs.Root, func() error {
		root, err := ReadManifest(fs.manifestPath(id))
		if errors.Is(err, os.ErrNotExist) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if root.ParentSessionID != nil {
			return fmt.Errorf("%w: connection retirement requires a root session", ErrInvalidSession)
		}
		if !supportsInferenceRetirement(root) {
			return fmt.Errorf("%w: restart the session worker before retiring connections", ErrInferenceUnavailable)
		}
		entries, err := os.ReadDir(fs.Root)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if !entry.IsDir() || entry.Name() == ".inference-connections" {
				continue
			}
			m, err := ReadManifest(fs.manifestPath(entry.Name()))
			if errors.Is(err, os.ErrNotExist) {
				continue
			} // A create still preparing must pass publication's fence.
			if err != nil {
				return err
			} // Unreadable manifests cannot prove a connection unused.
			uses := m.InferenceConnection != nil && m.InferenceConnection.ID == connectionID
			if update := m.InferenceUpdate; update != nil && update.Connection != nil && update.Connection.ID == connectionID {
				uses = uses || update.Status == "pending" || update.Status == "applying" || update.Status == "unknown"
			}
			if !uses {
				continue
			}
			terminal := m.IsStopped()
			if epoch := m.LastEpoch(); m.SessionKind == KindTask && epoch != nil && epoch.FinishedAt != nil {
				terminal = true
			}
			if !terminal || (m.Runner != nil && m.Runner.InCluster) {
				return fmt.Errorf("%w: inference connection is in use by session %s", ErrConflict, m.SessionID)
			}
			// Stop intent alone is insufficient: a live worker may still be draining.
			alive, err := runnerLockHeld(fs.sessionDir(entry.Name()))
			if err != nil {
				return err
			}
			if alive {
				return fmt.Errorf("%w: inference connection worker is still draining", ErrConflict)
			}
		}
		dir := filepath.Join(fs.Root, ".inference-connections")
		fence, err := os.OpenFile(filepath.Join(dir, connectionID+".retired"), os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer fence.Close()
		if err := fence.Sync(); err != nil {
			return err
		}
		directory, err := os.Open(dir)
		if err != nil {
			return err
		}
		defer directory.Close()
		return directory.Sync()
	})
	if err != nil {
		return nil, err
	}
	return &InferenceRetirement{ConnectionID: connectionID, Retired: true}, nil
}

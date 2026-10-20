package sessionapi

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/fsnotify/fsnotify"
)

// WatchEvents observes writes by the runner and its child processes. Subscribe
// before reading the current window so an append cannot fall between the two.
// Closing the channel means the watch failed: the HTTP stream must reconnect.
func (fs *FileStore) WatchEvents(id string) (<-chan struct{}, func(), error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, nil, err
	}
	watched := map[string]bool{}
	var ownerPID int
	var ownerExited <-chan error
	stopOwner := func() {}
	watch := func(path string, needed map[string]bool) error {
		for {
			info, err := os.Stat(path)
			if err == nil && info.IsDir() {
				break
			}
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			parent := filepath.Dir(path)
			if parent == path {
				return err
			}
			path = parent
		}
		needed[path] = true
		if watched[path] {
			return nil
		}
		if err := watcher.Add(path); err != nil {
			return err
		}
		watched[path] = true
		return nil
	}
	refresh := func() error {
		needed := map[string]bool{}
		if err := watch(fs.sessionDir(id), needed); err != nil {
			return err
		}
		manifest, err := ReadManifest(fs.manifestPath(id))
		if err != nil {
			return err
		}
		pid, _ := manifest.Runner.ProcessID()
		if pid != ownerPID {
			stopOwner()
			ownerExited, stopOwner, err = watchProcessExit(pid)
			if err != nil {
				return err
			}
			ownerPID = pid
		}
		for _, spec := range manifest.Specs {
			if spec.EvidencePath != nil && *spec.EvidencePath != "" {
				if err := watch(filepath.Dir(*spec.EvidencePath), needed); err != nil {
					return err
				}
			}
		}
		for path := range watched {
			if !needed[path] {
				if err := watcher.Remove(path); err != nil && !errors.Is(err, fsnotify.ErrNonExistentWatch) {
					return err
				}
				delete(watched, path)
			}
		}
		return nil
	}
	if err := refresh(); err != nil {
		stopOwner()
		watcher.Close()
		return nil, nil, err
	}
	changed := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(changed)
		defer watcher.Close()
		defer func() { stopOwner() }()
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
					delete(watched, event.Name)
				}
				if err := refresh(); err != nil {
					return
				}
			case <-watcher.Errors:
				return
			case err := <-ownerExited:
				ownerExited = nil
				if err != nil {
					return
				}
				// A new owner may have replaced the exiting one before its
				// manifest notification reached us. Attach before waking readers
				// so their status check cannot race a subsequent owner exit.
				if err := refresh(); err != nil {
					return
				}
			}
			select {
			case changed <- struct{}{}:
			default:
			}
		}
	}()
	return changed, func() {
		watcher.Close()
		<-done
	}, nil
}

// The cancellation pipe interrupts the native wait, including when an owner is
// replaced or an HTTP subscriber disconnects while the worker remains alive.
func watchProcessExit(pid int) (<-chan error, func(), error) {
	stop := func() {}
	if pid <= 0 {
		return nil, stop, nil
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, stop, err
	}
	wait, release, err := processExitWait(pid, int(reader.Fd()))
	if err != nil {
		reader.Close()
		writer.Close()
		if errors.Is(err, syscall.ESRCH) {
			exited := make(chan error, 1)
			exited <- nil // Already gone between reading the manifest and attaching.
			return exited, stop, nil
		}
		return nil, stop, err
	}
	exited := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer reader.Close()
		defer release()
		exited <- wait()
	}()
	var once sync.Once
	return exited, func() {
		once.Do(func() {
			writer.Close()
			<-done
		})
	}, nil
}

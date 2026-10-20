package sessionapi

import (
	"errors"
	"os"
	"path/filepath"

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
		watcher.Close()
		return nil, nil, err
	}
	changed := make(chan struct{}, 1)
	go func() {
		defer close(changed)
		defer watcher.Close()
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
				select {
				case changed <- struct{}{}:
				default:
				}
			case <-watcher.Errors:
				return
			}
		}
	}()
	return changed, func() { watcher.Close() }, nil
}

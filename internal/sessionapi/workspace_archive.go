package sessionapi

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// archiveExcludedNames stay out of a workspace download wherever they are:
// secrets, and dependencies that reinstall with one command.
var archiveExcludedNames = map[string]bool{
	".env": true, ".envrc": true,
	"node_modules": true, ".venv": true, "venv": true, "__pycache__": true, ".cache": true,
}

// WriteWorkspace streams the session's folder as a .tar.gz: the agent's files
// with their git history, logs, transcripts, and spec versions. It leaves out
// session.json (the session's token and provider key), the older workspace
// checkpoint, legacy spec snapshots (revisions/ holds every version), .env
// files, and dependency folders. Files still being written are cut at their
// size when reached.
func (fs *FileStore) WriteWorkspace(id string, w io.Writer) error {
	if !fileExists(fs.manifestPath(id)) {
		return fmt.Errorf("session %s: %w", id, ErrNotFound)
	}
	root := fs.sessionDir(id)
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	err := filepath.WalkDir(root, func(file string, entry os.DirEntry, err error) error {
		if err != nil || file == root {
			return err
		}
		rel, err := filepath.Rel(root, file)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if excludedFromArchive(rel) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		return writeArchiveEntry(tw, file, rel)
	})
	if err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func excludedFromArchive(rel string) bool {
	name := path.Base(rel)
	if rel == "snapshots" || !strings.Contains(rel, "/") && strings.HasPrefix(name, "session.json") {
		return true
	}
	checkpoint, _ := path.Match("specs/*/workspace.tar.gz*", rel)
	return checkpoint || archiveExcludedNames[name] || strings.HasPrefix(name, ".env.")
}

func writeArchiveEntry(tw *tar.Writer, file, rel string) error {
	info, err := os.Lstat(file)
	if err != nil {
		return err
	}
	link := ""
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		if link, err = os.Readlink(file); err != nil {
			return err
		}
	case info.IsDir():
		rel += "/"
	case !info.Mode().IsRegular():
		return nil // sockets and pipes have no content to download
	}
	header, err := tar.FileInfoHeader(info, link)
	if err != nil {
		return err
	}
	header.Name = rel
	if err := tw.WriteHeader(header); err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.CopyN(tw, f, header.Size)
	return err
}

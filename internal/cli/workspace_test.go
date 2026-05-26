package cli

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type testTarEntry struct {
	header tar.Header
	body   string
}

func TestExtractWorkspaceArtifactRejectsEscapingSymlink(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "workspace.tar.gz")
	writeWorkspaceArchive(t, archive, []testTarEntry{
		{header: tar.Header{Name: "safe", Typeflag: tar.TypeDir, Mode: 0o755}},
		{header: tar.Header{Name: "safe/link", Typeflag: tar.TypeSymlink, Linkname: "../../outside", Mode: 0o777}},
	})

	err := extractWorkspaceArtifact(archive, filepath.Join(dir, "dest"))
	if err == nil || !strings.Contains(err.Error(), "unsafe archive symlink") {
		t.Fatalf("extract error: got %v, want unsafe symlink error", err)
	}
}

func TestExtractWorkspaceArtifactRestoresInternalSymlink(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "workspace.tar.gz")
	writeWorkspaceArchive(t, archive, []testTarEntry{
		{header: tar.Header{Name: "target.txt", Typeflag: tar.TypeReg, Mode: 0o644}, body: "hello"},
		{header: tar.Header{Name: "link.txt", Typeflag: tar.TypeSymlink, Linkname: "target.txt", Mode: 0o777}},
	})

	dest := filepath.Join(dir, "dest")
	if err := extractWorkspaceArtifact(archive, dest); err != nil {
		t.Fatalf("extract: %v", err)
	}
	link, err := os.Readlink(filepath.Join(dest, "link.txt"))
	if err != nil {
		t.Fatalf("read link: %v", err)
	}
	if link != "target.txt" {
		t.Fatalf("link target: got %q, want target.txt", link)
	}
}

func writeWorkspaceArchive(t *testing.T, path string, entries []testTarEntry) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	gzipWriter := gzip.NewWriter(file)
	defer gzipWriter.Close()

	tarWriter := tar.NewWriter(gzipWriter)
	defer tarWriter.Close()

	for _, entry := range entries {
		header := entry.header
		if header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeRegA {
			header.Size = int64(len(entry.body))
		}
		if err := tarWriter.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if header.Size > 0 {
			if _, err := tarWriter.Write([]byte(entry.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
}

package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/cloud"
)

func TestWorkspaceArchiveDestinationUsesSessionID(t *testing.T) {
	got, err := workspaceArchiveDestination(" sess_123 ")
	if err != nil {
		t.Fatal(err)
	}
	if want := "telos-workspace-sess_123.tar.gz"; got != want {
		t.Fatalf("destination = %q, want %q", got, want)
	}
}

func TestWorkspaceArchiveDestinationRejectsUnsafeSessionID(t *testing.T) {
	if _, err := workspaceArchiveDestination("sess_../../secret"); err == nil {
		t.Fatal("expected unsafe session ID to be rejected")
	}
}

func TestDownloadWorkspaceArchiveWritesPrivateFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = io.WriteString(w, "workspace archive")
	}))
	defer srv.Close()

	destination := filepath.Join(t.TempDir(), "nested", "workspace.tar.gz")
	got, err := downloadWorkspaceArchive(
		cloud.NewClient(srv.URL, "test-token"),
		"sess_123",
		destination,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != destination {
		t.Fatalf("destination = %q, want %q", got, destination)
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "workspace archive" {
		t.Fatalf("archive = %q", data)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("archive mode = %o, want 600", got)
	}
}

func TestDownloadWorkspaceArchiveDoesNotOverwrite(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = io.WriteString(w, "replacement")
	}))
	defer srv.Close()

	destination := filepath.Join(t.TempDir(), "workspace.tar.gz")
	if err := os.WriteFile(destination, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := downloadWorkspaceArchive(
		cloud.NewClient(srv.URL, "test-token"),
		"sess_123",
		destination,
	)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error = %v", err)
	}
	data, readErr := os.ReadFile(destination)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "keep" {
		t.Fatalf("existing archive = %q", data)
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want 0", requests)
	}
}

func TestDownloadWorkspaceArchiveRemovesFailedDownload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"detail":"workspace archive is not ready"}`)
	}))
	defer srv.Close()

	destination := filepath.Join(t.TempDir(), "workspace.tar.gz")
	_, err := downloadWorkspaceArchive(
		cloud.NewClient(srv.URL, "test-token"),
		"sess_123",
		destination,
	)
	if err == nil || !strings.Contains(err.Error(), "workspace archive is not ready") {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(destination); !os.IsNotExist(statErr) {
		t.Fatalf("failed download remains at destination: %v", statErr)
	}
	assertNoWorkspaceDownloadTemps(t, destination)
}

func TestDownloadWorkspaceArchivePublishesOnlyCompleteFileWithoutOverwrite(t *testing.T) {
	downloadStarted := make(chan struct{})
	finishDownload := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "partial ")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		close(downloadStarted)
		<-finishDownload
		_, _ = io.WriteString(w, "archive")
	}))
	defer srv.Close()

	destination := filepath.Join(t.TempDir(), "workspace.tar.gz")
	result := make(chan error, 1)
	go func() {
		_, err := downloadWorkspaceArchive(
			cloud.NewClient(srv.URL, "test-token"),
			"sess_123",
			destination,
		)
		result <- err
	}()

	<-downloadStarted
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination became visible before completion: %v", err)
	}
	if err := os.WriteFile(destination, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	close(finishDownload)
	if err := <-result; err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error = %v", err)
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "keep" {
		t.Fatalf("existing archive = %q", data)
	}
	assertNoWorkspaceDownloadTemps(t, destination)
}

func assertNoWorkspaceDownloadTemps(t *testing.T, destination string) {
	t.Helper()
	matches, err := filepath.Glob(
		filepath.Join(filepath.Dir(destination), "."+filepath.Base(destination)+".*.partial"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary downloads remain: %v", matches)
	}
}

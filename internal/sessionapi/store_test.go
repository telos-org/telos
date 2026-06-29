package sessionapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/agentsession"
)

func TestCreateSessionDirSkipsExistingID(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root, RuntimeLocal)

	sessionSeq.Store(0)
	existingID := generateSessionID(RuntimeLocal)
	existingDir := filepath.Join(root, existingID)
	if err := os.Mkdir(existingDir, 0o755); err != nil {
		t.Fatal(err)
	}

	sessionSeq.Store(0)
	id, dir, err := store.createSessionDir()
	if err != nil {
		t.Fatal(err)
	}
	if id == existingID {
		t.Fatalf("reused existing session id %q", id)
	}
	if dir == existingDir {
		t.Fatalf("reused existing session dir %q", dir)
	}
	if _, err := os.Stat(existingDir); err != nil {
		t.Fatalf("existing session dir was modified: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("created session dir missing: %v", err)
	}
}

func TestReadSessionLogDiagnosticsHandlesLargeJSONLLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "specs", "diag", "turns", "0001-prover", "session.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	if err := enc.Encode(agentsession.Event{
		Type: agentsession.KindMessage,
		Message: &agentsession.Message{
			Role:    "toolResult",
			Content: []agentsession.Content{{Type: "text", Text: strings.Repeat("x", 96<<10)}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := enc.Encode(agentsession.Event{
		Type: agentsession.KindError,
		Data: agentsession.MarshalPayload(agentsession.ErrorPayload{
			Sequence:  2,
			Error:     "provider_rate_limited: retry exhausted",
			ErrorCode: "provider_rate_limited",
		}),
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	diagnostics := &SessionDiagnosticsResponse{
		Failures:         map[string]int{},
		SessionLogEvents: map[string]int{},
	}
	if err := readSessionLogDiagnostics(diagnostics, path, nil); err != nil {
		t.Fatalf("readSessionLogDiagnostics: %v", err)
	}
	if diagnostics.SessionLogEvents[agentsession.KindMessage] != 1 || diagnostics.SessionLogEvents[agentsession.KindError] != 1 {
		t.Fatalf("session log events not fully scanned: %#v", diagnostics.SessionLogEvents)
	}
	if diagnostics.Failures["provider"] != 1 {
		t.Fatalf("expected provider failure after large line, got %#v", diagnostics.Failures)
	}
}

package game

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewPVGStateUsesTranscriptName(t *testing.T) {
	state := NewPVGState("test-system", t.TempDir(), "sess-001")
	want := filepath.Join(state.SpecDir, "transcript-sess-001.md")
	if state.TranscriptPath != want {
		t.Fatalf("transcript path: got %q want %q", state.TranscriptPath, want)
	}
}

func TestWriteTurnTaskOwnsTurnArtifacts(t *testing.T) {
	ts := &TurnState{Dir: t.TempDir()}
	if err := os.WriteFile(ts.RawLogPath(), []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := WriteTurnTask(ts, "do the thing"); err != nil {
		t.Fatalf("WriteTurnTask: %v", err)
	}
	task, err := os.ReadFile(ts.TaskPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(task) != "do the thing" {
		t.Fatalf("task content: got %q", string(task))
	}
	raw, err := os.ReadFile(ts.RawLogPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 0 {
		t.Fatalf("raw log should be initialized empty, got %q", string(raw))
	}
}

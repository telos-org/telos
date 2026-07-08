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

func TestPVGStateTurnPathIncludesEpoch(t *testing.T) {
	state := NewPVGState("test-system", t.TempDir(), "sess-001")
	turn := state.Turn(7, 2, "verifier")
	want := filepath.Join(state.TurnsDir(), "epoch-0007", "0002-verifier")
	if turn.Dir != want {
		t.Fatalf("turn dir: got %q want %q", turn.Dir, want)
	}
}

func TestWriteTurnTaskWritesOnlyTaskArtifact(t *testing.T) {
	ts := &TurnState{Dir: t.TempDir()}

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
	if _, err := os.Stat(ts.PiSessionPath()); !os.IsNotExist(err) {
		t.Fatalf("WriteTurnTask should leave pi session ownership to Pi, stat err=%v", err)
	}
}

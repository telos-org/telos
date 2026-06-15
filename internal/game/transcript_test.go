package game

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitializeTranscript(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.md")

	err := InitializeTranscript(path, "sess-001", "test-system", "/path/to/evidence.jsonl", "2026-01-01T00:00:00.000Z")
	if err != nil {
		t.Fatalf("InitializeTranscript: %v", err)
	}

	content := ReadTranscript(path)
	if !strings.Contains(content, "# Session Transcript: sess-001") {
		t.Error("should contain transcript header")
	}
	if !strings.Contains(content, "test-system") {
		t.Error("should contain system name")
	}
	if !strings.Contains(content, "evidence.jsonl") {
		t.Error("should contain evidence path")
	}
}

func TestInitializeTranscriptIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.md")

	InitializeTranscript(path, "sess-001", "test-system", "/path/evidence.jsonl", "2026-01-01T00:00:00.000Z")

	// Append something
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("## Extra\n")
	f.Close()

	// Re-initialize should not overwrite
	InitializeTranscript(path, "sess-001", "test-system", "/path/evidence.jsonl", "2026-01-01T00:00:00.000Z")

	content := ReadTranscript(path)
	if !strings.Contains(content, "## Extra") {
		t.Error("second init should not overwrite")
	}
}

func TestAppendTurn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.md")
	os.WriteFile(path, []byte("# Transcript\n"), 0o644)

	stats := &TurnStats{Model: "claude-test", CostUSD: 0.5, NumTurns: 3}
	err := AppendTurn(path, "prover", 1, "CONTINUE", "I built the thing.\n\n<progress_update>Built it</progress_update>", stats, "0001-prover", "")
	if err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}

	content := ReadTranscript(path)
	if !strings.Contains(content, "## Implementation 1") {
		t.Error("should contain implementation header")
	}
	if !strings.Contains(content, "I built the thing.") {
		t.Error("should contain turn body")
	}
	if !strings.Contains(content, "<status>CONTINUE</status>") {
		t.Error("should contain status tag")
	}
	if !strings.Contains(content, "model `claude-test`") {
		t.Error("should contain model in metadata")
	}
	if strings.Contains(content, "pi-session.jsonl") || strings.Contains(content, "task.md") {
		t.Error("transcript should not expose turn artifact paths without an error")
	}
}

func TestAppendTurnVerifier(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.md")
	os.WriteFile(path, []byte("# Transcript\n"), 0o644)

	AppendTurn(path, "verifier", 1, "CONCEDE", "All good.\n\n<progress_update>Conceding</progress_update>", nil, "0002-verifier", "")

	content := ReadTranscript(path)
	if !strings.Contains(content, "## Evaluation 1") {
		t.Error("should contain evaluation header")
	}
	if !strings.Contains(content, "<status>CONCEDE</status>") {
		t.Error("should contain concede status")
	}
}

func TestAppendTurnWithError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.md")
	os.WriteFile(path, []byte("# Transcript\n"), 0o644)

	piSessionPath := filepath.Join(dir, "turns", "0001-prover", "pi-session.jsonl")
	evidencePath := filepath.Join(dir, "evidence.jsonl")
	AppendTurnWithOptions(path, "prover", 1, "CONTINUE", "I changed main.go before the tool failed.\n\n<status>CONTINUE</status>", nil, "0001-prover", "pi_failed:1", AppendTurnOptions{
		IncludeStatus: true,
		PiSessionPath: piSessionPath,
		EvidencePath:  evidencePath,
	})

	content := ReadTranscript(path)
	if !strings.Contains(content, "runtime error") {
		t.Error("should mention runtime error")
	}
	if !strings.Contains(content, "pi_failed:1") {
		t.Error("should contain error detail")
	}
	if !strings.Contains(content, piSessionPath) {
		t.Error("should point to agent session")
	}
	if !strings.Contains(content, evidencePath) {
		t.Error("should point to evidence log")
	}
	if strings.Contains(content, "Captured Assistant Text Before Error") {
		t.Error("should not summarize captured assistant text in the transcript")
	}
	if strings.Contains(content, "I changed main.go before the tool failed.") {
		t.Error("should leave captured assistant text in Pi artifacts")
	}
	if strings.Contains(content, "<status>CONTINUE</status>\n\n<progress_update>") {
		t.Error("captured assistant text should not preserve the assistant status tag")
	}
}

func TestAppendGameResult(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.md")
	os.WriteFile(path, []byte("# Transcript\n"), 0o644)

	AppendGameResult(path, "success", "")

	content := ReadTranscript(path)
	if !strings.Contains(content, "## Result") {
		t.Error("should contain result section")
	}
	if !strings.Contains(content, "`success`") {
		t.Error("should contain result status")
	}
}

func TestAppendGameResultWithError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.md")
	os.WriteFile(path, []byte("# Transcript\n"), 0o644)

	AppendGameResult(path, "failure", "budget exceeded")

	content := ReadTranscript(path)
	if !strings.Contains(content, "`failure`") {
		t.Error("should contain failure result")
	}
	if !strings.Contains(content, "budget exceeded") {
		t.Error("should contain error message")
	}
}

func TestStripFinalStatus(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"text\n<status>CONTINUE</status>\n", "text"},
		{"text\n<status>CONCEDE</status>", "text"},
		{"just text", "just text"},
		{"", ""},
	}
	for _, tt := range tests {
		got := stripFinalStatus(tt.input)
		if got != tt.expected {
			t.Errorf("stripFinalStatus(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

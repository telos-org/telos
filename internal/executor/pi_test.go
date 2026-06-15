package executor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/game"
)

func TestReadPiSessionExtractsAssistantTextStatsAndTurns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pi-session.jsonl")
	writePiSession(t, path, `{"type":"session","version":3,"id":"sess","timestamp":"2026-05-21T00:00:00Z","cwd":"/tmp"}`)
	appendPiSession(t, path, `{"type":"message","id":"u","parentId":null,"timestamp":"2026-05-21T00:00:01Z","message":{"role":"user","content":"do it","timestamp":1770000000000}}`)
	appendPiSession(t, path, `{"type":"message","id":"t","parentId":"u","timestamp":"2026-05-21T00:00:02Z","message":{"role":"toolResult","toolCallId":"call_1","toolName":"bash","content":[{"type":"text","text":"ok"}],"isError":false,"timestamp":1770000000001}}`)
	appendPiSession(t, path, `{"type":"message","id":"a","parentId":"t","timestamp":"2026-05-21T00:00:03Z","message":{"role":"assistant","provider":"openai-codex","model":"gpt-5.5","stopReason":"stop","content":[{"type":"thinking","thinking":"hidden"},{"type":"text","text":"Implemented it.\n\n<status>CONCEDE</status>\n"}],"usage":{"input":10,"output":20,"cacheRead":30,"cacheWrite":40,"totalTokens":100,"cost":{"input":0.1,"output":0.2,"cacheRead":0.3,"cacheWrite":0.4,"total":1.0}},"timestamp":1770000000002}}`)

	summary, err := ReadPiSession(path)
	if err != nil {
		t.Fatalf("ReadPiSession: %v", err)
	}
	if summary.Logs != "Implemented it.\n\n<status>CONCEDE</status>\n" {
		t.Fatalf("logs: got %q", summary.Logs)
	}
	if summary.Error != "" {
		t.Fatalf("error: got %q", summary.Error)
	}
	want := game.TurnStats{
		CostUSD:             1.0,
		NumTurns:            1,
		InputTokens:         10,
		OutputTokens:        20,
		CacheReadTokens:     30,
		CacheCreationTokens: 40,
		Model:               "gpt-5.5",
	}
	if summary.Stats != want {
		t.Fatalf("stats: got %+v want %+v", summary.Stats, want)
	}
}

func TestReadPiSessionUsesLastAssistantTextAndAggregatesAssistantUsage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pi-session.jsonl")
	writePiSession(t, path, `{"type":"session","version":3,"id":"sess","timestamp":"2026-05-21T00:00:00Z","cwd":"/tmp"}`)
	appendPiSession(t, path, `{"type":"message","id":"a1","parentId":null,"timestamp":"2026-05-21T00:00:01Z","message":{"role":"assistant","model":"gpt-5.5","stopReason":"stop","content":[{"type":"text","text":"first"}],"usage":{"input":1,"output":1,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.1}}}}`)
	appendPiSession(t, path, `{"type":"message","id":"a2","parentId":"a1","timestamp":"2026-05-21T00:00:02Z","message":{"role":"assistant","model":"gpt-5.5","stopReason":"stop","content":[{"type":"text","text":"second"}],"usage":{"input":2,"output":3,"cacheRead":4,"cacheWrite":5,"cost":{"total":0.6}}}}`)

	summary, err := ReadPiSession(path)
	if err != nil {
		t.Fatalf("ReadPiSession: %v", err)
	}
	if summary.Logs != "second" {
		t.Fatalf("logs: got %q", summary.Logs)
	}
	if summary.Stats.InputTokens != 3 || summary.Stats.OutputTokens != 4 ||
		summary.Stats.CacheReadTokens != 4 || summary.Stats.CacheCreationTokens != 5 ||
		summary.Stats.CostUSD != 0.7 {
		t.Fatalf("stats should sum all assistant usage: %+v", summary.Stats)
	}
}

func TestReadPiSessionMapsLengthStopToRecoverableError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pi-session.jsonl")
	writePiSession(t, path, `{"type":"session","version":3,"id":"sess","timestamp":"2026-05-21T00:00:00Z","cwd":"/tmp"}`)
	appendPiSession(t, path, `{"type":"message","id":"a","parentId":null,"timestamp":"2026-05-21T00:00:01Z","message":{"role":"assistant","model":"gpt-5.5","stopReason":"length","content":[{"type":"text","text":"partial"}],"usage":{"input":1,"output":2,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.3}}}}`)

	summary, err := ReadPiSession(path)
	if err != nil {
		t.Fatalf("ReadPiSession: %v", err)
	}
	if summary.Error != "agent_output_truncated:length" {
		t.Fatalf("error: got %q", summary.Error)
	}
}

func TestReadPiSessionIgnoresTransientErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pi-session.jsonl")
	writePiSession(t, path, `{"type":"session","version":3,"id":"sess","timestamp":"2026-05-21T00:00:00Z","cwd":"/tmp"}`)
	appendPiSession(t, path, `{"type":"message","id":"a","parentId":null,"timestamp":"2026-05-21T00:00:01Z","message":{"role":"assistant","model":"gpt-5.5","stopReason":"error","errorMessage":"overloaded_error: try again","content":[{"type":"text","text":"partial"}],"usage":{"input":1,"output":2,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.3}}}}`)

	summary, err := ReadPiSession(path)
	if err != nil {
		t.Fatalf("ReadPiSession: %v", err)
	}
	if summary.Error != "" {
		t.Fatalf("transient error should be ignored, got %q", summary.Error)
	}
}

func TestReadPiSessionRequiresAssistantMessage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pi-session.jsonl")
	writePiSession(t, path, `{"type":"session","version":3,"id":"sess","timestamp":"2026-05-21T00:00:00Z","cwd":"/tmp"}`)

	_, err := ReadPiSession(path)
	if err == nil || !strings.Contains(err.Error(), "no assistant message") {
		t.Fatalf("expected no assistant error, got %v", err)
	}
}

func writePiSession(t *testing.T, path string, line string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendPiSession(t *testing.T, path string, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatal(err)
	}
}

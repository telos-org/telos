package executor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/game"
	"github.com/telos-org/telos/internal/platform"
)

func TestNewPiExecutorDefaultsToNoTimeout(t *testing.T) {
	exec := NewPiExecutor(nil, "claude-test", "", 0)

	if exec.Timeout != 0 {
		t.Fatalf("timeout should default to disabled, got %d", exec.Timeout)
	}
	if exec.Thinking != "medium" {
		t.Fatalf("thinking: got %q", exec.Thinking)
	}
}

func TestBuildPiArgvUsesTextModeWithoutSessionByDefault(t *testing.T) {
	argv := BuildPiArgv("claude-test", "high", "", "")
	if len(argv) != 6 {
		t.Fatalf("expected 6 args, got %d", len(argv))
	}
	if argv[0] != "sh" {
		t.Errorf("first arg: got %q", argv[0])
	}
	if argv[4] != "claude-test" {
		t.Errorf("model arg: got %q", argv[4])
	}
	if argv[5] != "high" {
		t.Errorf("thinking arg: got %q", argv[5])
	}
	if !strings.Contains(argv[2], `prompt="${TELOS_TASK}"`) {
		t.Errorf("task prompt is not expanded from env: %s", argv[2])
	}
	if !strings.Contains(argv[2], `--mode text`) {
		t.Errorf("pi should run in text mode: %s", argv[2])
	}
	if !strings.Contains(argv[2], `--no-session`) {
		t.Errorf("fallback path should stay ephemeral: %s", argv[2])
	}
	if strings.Contains(argv[2], `--mode json`) {
		t.Errorf("pi should not use the streaming json event mode: %s", argv[2])
	}
}

func TestBuildPiArgvUsesTaskFileAndSessionFile(t *testing.T) {
	argv := BuildPiArgv("claude-test", "high", "/tmp/task.md", "/tmp/pi-session.jsonl")
	if len(argv) != 8 {
		t.Fatalf("expected 8 args, got %d", len(argv))
	}
	if argv[6] != "@/tmp/task.md" {
		t.Errorf("task file arg: got %q", argv[6])
	}
	if argv[7] != "/tmp/pi-session.jsonl" {
		t.Errorf("session file arg: got %q", argv[7])
	}
	if !strings.Contains(argv[2], `--session "$4"`) {
		t.Errorf("pi session file is not selected from argv: %s", argv[2])
	}
	if strings.Contains(argv[2], `-p "${TELOS_TASK}"`) {
		t.Errorf("task env is still expanded directly into argv: %s", argv[2])
	}
}

func TestBuildPiArgvUsesSessionFileWithoutTaskFile(t *testing.T) {
	argv := BuildPiArgv("claude-test", "high", "", "/tmp/pi-session.jsonl")
	if len(argv) != 8 {
		t.Fatalf("expected 8 args with empty task placeholder, got %d", len(argv))
	}
	if argv[6] != "" {
		t.Errorf("task placeholder: got %q", argv[6])
	}
	if argv[7] != "/tmp/pi-session.jsonl" {
		t.Errorf("session file arg: got %q", argv[7])
	}
}

func TestExecuteTurnIncludesStderrOnPiFailure(t *testing.T) {
	workspace := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	piPath := filepath.Join(bin, "pi")
	script := "#!/bin/sh\necho \"EROFS: read-only file system\" >&2\nexit 1\n"
	if err := os.WriteFile(piPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	p := platform.NewLocalPlatform(workspace)
	p.Env = map[string]string{"HOME": home}
	exec := NewPiExecutor(p, "test-model", "high", 0)

	result := exec.ExecuteTurn("do it", "prover", nil)

	if result.Error == "" || !strings.Contains(result.Error, "pi_failed:1") {
		t.Fatalf("error: got %q", result.Error)
	}
	if !strings.Contains(result.Logs, "[stderr]") ||
		!strings.Contains(result.Logs, "EROFS: read-only file system") {
		t.Fatalf("logs should include stderr, got %q", result.Logs)
	}
}

func TestExecuteTurnTreatsTimeoutAsTerminalFailure(t *testing.T) {
	workspace := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	piPath := filepath.Join(bin, "pi")
	script := "#!/bin/sh\nsleep 5\n"
	if err := os.WriteFile(piPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	p := platform.NewLocalPlatform(workspace)
	p.Env = map[string]string{"HOME": home}
	exec := NewPiExecutor(p, "test-model", "high", 1)

	result := exec.ExecuteTurn("do it", "prover", nil)

	if result.Error != "local_timeout:1" {
		t.Fatalf("error: got %q", result.Error)
	}
	if result.Recoverable {
		t.Fatalf("timeout should be terminal, got recoverable result: %#v", result)
	}
}

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

func TestPiLineEventsProjectsSafeToolCallProgress(t *testing.T) {
	events := piLineEvents(`{"type":"message","message":{"role":"assistant","content":[{"type":"toolCall","name":"read","arguments":{"path":"/tmp/session/spec.md"}},{"type":"toolCall","name":"bash","arguments":{"command":"kubectl get pods --token SECRET"}},{"type":"toolCall","name":"bash","arguments":{"command":"git status --short"}}]}}`)

	got := make([]string, 0, len(events))
	for _, event := range events {
		got = append(got, event.Kind+":"+event.Text)
	}
	want := []string{
		"progress_update:Reading spec.md",
		"progress_update:Running kubectl",
		"progress_update:Updating workspace",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("events:\ngot\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if strings.Contains(strings.Join(got, "\n"), "SECRET") {
		t.Fatalf("tool progress leaked command contents: %v", got)
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

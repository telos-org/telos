package executor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/game"
	"github.com/telos-org/telos/internal/platform"
)

func TestParsePiJSONLine(t *testing.T) {
	// Valid JSON
	line := `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}`
	event := ParsePiJSONLine(line)
	if event == nil {
		t.Fatal("expected parsed event")
	}
	if event["type"] != "message_end" {
		t.Errorf("type: got %v", event["type"])
	}

	// Invalid JSON
	if ParsePiJSONLine("not json") != nil {
		t.Error("expected nil for invalid JSON")
	}

	// Non-object JSON
	if ParsePiJSONLine(`"just a string"`) != nil {
		t.Error("expected nil for non-object JSON")
	}

	// Empty
	if ParsePiJSONLine("") != nil {
		t.Error("expected nil for empty string")
	}
}

func TestNewPiExecutorDefaultsToNoTimeout(t *testing.T) {
	exec := NewPiExecutor(nil, "claude-test", "", 0)

	if exec.Timeout != 0 {
		t.Fatalf("timeout should default to disabled, got %d", exec.Timeout)
	}
	if exec.Thinking != "medium" {
		t.Fatalf("thinking: got %q", exec.Thinking)
	}
}

func TestHandlePiEventMessageEnd(t *testing.T) {
	event := map[string]interface{}{
		"type": "message_end",
		"message": map[string]interface{}{
			"role": "assistant",
			"content": []interface{}{
				map[string]interface{}{
					"type": "text",
					"text": "Hello, world!",
				},
			},
			"usage": map[string]interface{}{
				"input":      float64(1000),
				"output":     float64(500),
				"cacheRead":  float64(200),
				"cacheWrite": float64(100),
				"cost": map[string]interface{}{
					"total": float64(0.05),
				},
			},
			"model": "claude-test",
		},
	}

	var textParts []string
	stats := game.TurnStats{}
	HandlePiEvent(event, &textParts, &stats)

	if len(textParts) != 1 || textParts[0] != "Hello, world!" {
		t.Errorf("text: got %v", textParts)
	}
	if stats.InputTokens != 1000 {
		t.Errorf("input: got %d", stats.InputTokens)
	}
	if stats.OutputTokens != 500 {
		t.Errorf("output: got %d", stats.OutputTokens)
	}
	if stats.CacheReadTokens != 200 {
		t.Errorf("cache read: got %d", stats.CacheReadTokens)
	}
	if stats.CacheCreationTokens != 100 {
		t.Errorf("cache write: got %d", stats.CacheCreationTokens)
	}
	if stats.CostUSD != 0.05 {
		t.Errorf("cost: got %f", stats.CostUSD)
	}
	if stats.Model != "claude-test" {
		t.Errorf("model: got %q", stats.Model)
	}
}

func TestHandlePiEventToolEnd(t *testing.T) {
	event := map[string]interface{}{
		"type": "tool_execution_end",
	}

	var textParts []string
	stats := game.TurnStats{}
	HandlePiEvent(event, &textParts, &stats)

	if stats.NumTurns != 1 {
		t.Errorf("num_turns: got %d", stats.NumTurns)
	}
}

func TestHandlePiEventUserMessage(t *testing.T) {
	event := map[string]interface{}{
		"type": "message_end",
		"message": map[string]interface{}{
			"role":    "user",
			"content": []interface{}{},
		},
	}

	var textParts []string
	stats := game.TurnStats{}
	HandlePiEvent(event, &textParts, &stats)

	if len(textParts) != 0 {
		t.Error("user messages should not add text")
	}
}

func TestExtractPiEventError(t *testing.T) {
	// No error
	event := map[string]interface{}{
		"type": "message_end",
		"message": map[string]interface{}{
			"role": "assistant",
		},
	}
	if err := ExtractPiEventError(event); err != "" {
		t.Errorf("expected empty error, got %q", err)
	}

	// Real error
	event["message"] = map[string]interface{}{
		"role":         "assistant",
		"errorMessage": "something broke",
	}
	if err := ExtractPiEventError(event); err != "something broke" {
		t.Errorf("expected error, got %q", err)
	}

	// Transient error (should be ignored)
	event["message"] = map[string]interface{}{
		"role":         "assistant",
		"errorMessage": "overloaded_error: try again",
	}
	if err := ExtractPiEventError(event); err != "" {
		t.Errorf("transient error should be ignored, got %q", err)
	}
}

func TestExtractPiStopReason(t *testing.T) {
	event := map[string]interface{}{
		"type": "message_end",
		"message": map[string]interface{}{
			"role":       "assistant",
			"stopReason": "length",
		},
	}
	if sr := ExtractPiStopReason(event); sr != "length" {
		t.Errorf("stop reason: got %q", sr)
	}

	// No stop reason
	event["message"] = map[string]interface{}{
		"role": "assistant",
	}
	if sr := ExtractPiStopReason(event); sr != "" {
		t.Errorf("expected empty stop reason, got %q", sr)
	}
}

func TestExtractPiStopReasonAgentEnd(t *testing.T) {
	event := map[string]interface{}{
		"type": "agent_end",
		"messages": []interface{}{
			map[string]interface{}{
				"role":       "assistant",
				"stopReason": "end_turn",
			},
		},
	}
	if sr := ExtractPiStopReason(event); sr != "end_turn" {
		t.Errorf("stop reason: got %q", sr)
	}
}

func TestBuildPiArgv(t *testing.T) {
	argv := BuildPiArgv("claude-test", "high", "")
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
}

func TestBuildPiArgvUsesTaskFile(t *testing.T) {
	argv := BuildPiArgv("claude-test", "high", "/tmp/task.md")
	if len(argv) != 7 {
		t.Fatalf("expected 7 args, got %d", len(argv))
	}
	if argv[6] != "@/tmp/task.md" {
		t.Errorf("task file arg: got %q", argv[6])
	}
	if !strings.Contains(argv[2], `if [ -n "${3:-}" ]; then prompt="$3"; fi`) {
		t.Errorf("task file prompt is not selected from argv: %s", argv[2])
	}
	if strings.Contains(argv[2], `-p "${TELOS_TASK}"`) {
		t.Errorf("task env is still expanded directly into argv: %s", argv[2])
	}
}

func TestMalformedEventsHandledGracefully(t *testing.T) {
	malformed := []string{
		`{"type":"unknown_event"}`,
		`{"type":"message_end","message":"not_a_map"}`,
		`{"type":"message_end","message":{"role":"assistant","content":"not_array"}}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text"}]}}`,
		`{"type":"message_end","message":{"role":"assistant","usage":"not_a_map"}}`,
	}

	for _, line := range malformed {
		event := ParsePiJSONLine(line)
		if event == nil {
			continue
		}
		var textParts []string
		stats := game.TurnStats{}
		// Should not panic
		HandlePiEvent(event, &textParts, &stats)
	}
}

func TestRawLogLineFormat(t *testing.T) {
	// Valid JSON
	line := `{"type":"test","data":"value"}`
	event := ParsePiJSONLine(line)
	if event == nil {
		t.Fatal("should parse valid json")
	}
	b, _ := json.Marshal(event)
	var m map[string]interface{}
	json.Unmarshal(b, &m)
	if m["type"] != "test" {
		t.Errorf("expected type=test")
	}

	// Invalid JSON -> wraps as unparsed
	if ParsePiJSONLine("garbage") != nil {
		t.Error("invalid json should return nil")
	}
}

func TestAppendPiFailureWritesRawFailureRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "raw.jsonl")

	appendPiFailure(path, "pi_failed:1", &platform.CommandResult{
		ReturnCode: 1,
		DurationMS: 25,
		Stderr:     "unexpected event order",
	}, "partial assistant text")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read raw log: %v", err)
	}
	var record map[string]interface{}
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatalf("raw failure record should be json: %v\n%s", err, data)
	}
	if record["type"] != "telos_pi_failure" {
		t.Fatalf("type: got %v", record["type"])
	}
	if record["reason"] != "pi_failed:1" {
		t.Fatalf("reason: got %v", record["reason"])
	}
	if record["stderr"] != "unexpected event order" {
		t.Fatalf("stderr: got %v", record["stderr"])
	}
	if record["assistantText"] != "partial assistant text" {
		t.Fatalf("assistant text: got %v", record["assistantText"])
	}
}

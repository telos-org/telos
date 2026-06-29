package evidence

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEvidenceLog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "evidence.jsonl")

	ev := New("test-system", path, "sess-001", 0)
	ev.Log("test_event", 1, "prover", map[string]interface{}{"key": "value"})
	ev.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}

	var m map[string]interface{}
	json.Unmarshal([]byte(lines[0]), &m)
	if m["schema"] != SchemaVersion {
		t.Errorf("schema: got %v", m["schema"])
	}
	if m["session_id"] != "sess-001" {
		t.Errorf("session_id: got %v", m["session_id"])
	}
	if m["event"] != "test_event" {
		t.Errorf("event: got %v", m["event"])
	}
	if m["system"] != "test-system" {
		t.Errorf("system: got %v", m["system"])
	}
	if m["role"] != "prover" {
		t.Errorf("role: got %v", m["role"])
	}
	data2, _ := m["data"].(map[string]interface{})
	if data2["key"] != "value" {
		t.Errorf("data: got %v", m["data"])
	}
}

func TestEvidenceSequence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "evidence.jsonl")

	ev := New("test-system", path, "sess-002", 0)
	ev.Log("event1", 1, "prover", nil)
	ev.Log("event2", 2, "verifier", nil)
	ev.Log("event3", 3, "system", nil)
	ev.Close()

	data, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}

	for i, line := range lines {
		var m map[string]interface{}
		json.Unmarshal([]byte(line), &m)
		seq, _ := m["event_seq"].(float64)
		if int(seq) != i+1 {
			t.Errorf("line %d: expected seq %d, got %v", i, i+1, seq)
		}
	}
}

func TestEvidenceResumesSequence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "evidence.jsonl")

	// Write initial events
	ev1 := New("test-system", path, "sess-003", 0)
	ev1.Log("event1", 1, "prover", nil)
	ev1.Close()

	// Resume
	ev2 := New("test-system", path, "sess-003", 0)
	ev2.Log("event2", 2, "verifier", nil)
	ev2.Close()

	data, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}

	var m map[string]interface{}
	json.Unmarshal([]byte(lines[1]), &m)
	seq, _ := m["event_seq"].(float64)
	if int(seq) != 2 {
		t.Errorf("expected seq 2, got %v", seq)
	}
}

func TestEvidenceLogAgent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "evidence.jsonl")

	ev := New("test-system", path, "sess-004", 0)
	ev.LogAgent(1, "prover", "CONTINUE", "some logs", nil)
	ev.Close()

	data, _ := os.ReadFile(path)
	var m map[string]interface{}
	json.Unmarshal([]byte(strings.TrimSpace(string(data))), &m)
	if m["event"] != "agent_complete" {
		t.Errorf("event: got %v", m["event"])
	}
	d, _ := m["data"].(map[string]interface{})
	if d["status"] != "CONTINUE" {
		t.Errorf("status: got %v", d["status"])
	}
}

func TestEvidenceLogGameEnd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "evidence.jsonl")

	ev := New("test-system", path, "sess-005", 0)
	ev.LogGameEnd("success", 3, 2, 1, true, 5.5, true, 10000, 5000, 1000, 500, "", "verifier_conceded")
	ev.Close()

	data, _ := os.ReadFile(path)
	var m map[string]interface{}
	json.Unmarshal([]byte(strings.TrimSpace(string(data))), &m)
	if m["event"] != "game_end" {
		t.Errorf("event: got %v", m["event"])
	}
	d, _ := m["data"].(map[string]interface{})
	if d["game_result"] != "success" {
		t.Errorf("game_result: got %v", d["game_result"])
	}
	if d["verifier_conceded"] != true {
		t.Errorf("verifier_conceded: got %v", d["verifier_conceded"])
	}
	if d["cost_unavailable"] != true {
		t.Errorf("cost_unavailable: got %v", d["cost_unavailable"])
	}
	if d["completion_reason"] != "verifier_conceded" {
		t.Errorf("completion_reason: got %v", d["completion_reason"])
	}
}

func TestEvidenceNilData(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "evidence.jsonl")

	ev := New("test-system", path, "sess-006", 0)
	ev.Log("null_data", 1, "system", nil)
	ev.Close()

	data, _ := os.ReadFile(path)
	var m map[string]interface{}
	json.Unmarshal([]byte(strings.TrimSpace(string(data))), &m)
	// data should be empty object, not null
	d, ok := m["data"].(map[string]interface{})
	if !ok {
		t.Errorf("data should be object, got %T: %v", m["data"], m["data"])
	}
	if len(d) != 0 {
		t.Errorf("data should be empty: got %v", d)
	}
}

func TestErrorCode(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "provider_timeout:turn_timeout:1", want: "provider_timeout"},
		{input: "no_successful_implementation", want: "no_successful_implementation"},
		{input: "", want: ""},
		{input: ":leading", want: ""},
		{input: " agent_protocol", want: ""},
	}
	for _, tt := range tests {
		if got := ErrorCode(tt.input); got != tt.want {
			t.Fatalf("ErrorCode(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

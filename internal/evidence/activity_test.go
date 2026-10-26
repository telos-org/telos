package evidence

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestActivityRowsFollowUserFacingEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "evidence.jsonl")
	ev := New("pvg", path, "sess-activity", 1)
	ev.Log("round_start", 1, "prover", map[string]interface{}{"audience": "user"})
	ev.Log("agent_progress", 1, "prover", map[string]interface{}{"audience": "agent", "kind": "tool", "text": "Read src/app.ts"})
	ev.Log("agent_progress", 1, "prover", map[string]interface{}{"audience": "user", "kind": "progress_update", "text": "Starting on the login page"})
	ev.LogAgent(1, "prover", "CONTINUE", "", map[string]interface{}{"cost_usd": 0.5})
	ev.Log("budget_exceeded", 1, "system", map[string]interface{}{"session_cost_usd": 5.0, "cap_usd": 4.0})
	ev.Log("round_start", 2, "verifier", nil)
	ev.Log("agent_progress", 2, "verifier", map[string]interface{}{"audience": "user", "text": ""})
	ev.LogGameEnd("success", 2, 1, 1, true, 1.5, 0, 0, 0, 0, "", "verifier_conceded")

	data, err := os.ReadFile(ActivityPath(path))
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]interface{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var row map[string]interface{}
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatal(err)
		}
		if row["ts"] == "" || row["ts"] == nil {
			t.Fatalf("row without a timestamp: %v", row)
		}
		delete(row, "ts")
		rows = append(rows, row)
	}
	want := []map[string]interface{}{
		{"event_seq": 1.0, "kind": "round_started", "role": "prover"},
		{"event_seq": 2.0, "kind": "message", "role": "prover", "text": "Starting on the login page"},
		{"event_seq": 3.0, "kind": "turn_ended", "role": "prover"},
		{"event_seq": 4.0, "kind": "budget_exceeded", "session_cost_usd": 5.0, "cap_usd": 4.0},
		{"event_seq": 5.0, "kind": "evaluation_ended", "game_result": "success", "completion_reason": "verifier_conceded"},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("activity rows:\n got %v\nwant %v", rows, want)
	}
}

func TestActivityLogsBeginWithTheirEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "evidence.jsonl")
	older := `{"event":"agent_progress","event_seq":1,"data":{"audience":"user","text":"Older runtime"}}` + "\n"
	if err := os.WriteFile(path, []byte(older), 0o644); err != nil {
		t.Fatal(err)
	}
	New("pvg", path, "sess-old", 1).Log("agent_progress", 1, "prover", map[string]interface{}{"audience": "user", "text": "After upgrade"})

	if HasActivity(path) || fileExists(ActivityPath(path)) {
		t.Fatal("a log an older runtime began reported partial display rows")
	}
}

// testdata/activity_rows.json is shared with Cloud's fallback for runtimes
// without activity logs (telos/control/tests/fixtures in telos-org/cloud).
func TestActivityRowsMatchSharedFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/activity_rows.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Event map[string]interface{} `json:"event"`
		Row   map[string]interface{} `json:"row"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		if got := activityRow(c.Event); !reflect.DeepEqual(got, c.Row) {
			t.Errorf("event %v:\n got %v\nwant %v", c.Event["event_seq"], got, c.Row)
		}
	}
}

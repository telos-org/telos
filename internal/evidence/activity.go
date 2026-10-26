package evidence

import "path/filepath"

// ActivityPath is the display log beside an evidence log. Each line is one
// row a user sees, numbered by its own dense event_seq so pages and live
// replay can resume from a row position.
func ActivityPath(evidencePath string) string {
	return filepath.Join(filepath.Dir(evidencePath), "activity.jsonl")
}

// TurnEnded rows mark where a session's spend changed. Clients act on them but
// do not show them, so ?tail counts the rows around them.
const TurnEnded = "turn_ended"

type activityKind struct {
	kind        string
	userOnly    bool     // shown only when explicitly addressed to the user
	anyAudience bool     // a marker clients act on but do not show
	required    string   // a field the row cannot be shown without
	fields      []string // data fields the row carries; clients render the text
}

var activityKinds = map[string]activityKind{
	"game_start":                {kind: "evaluation_started"},
	"agent_complete":            {kind: TurnEnded, anyAudience: true},
	"round_start":               {kind: "round_started", userOnly: true},
	"agent_progress":            {kind: "message", required: "text", fields: []string{"text"}},
	"agent_failure_recoverable": {kind: "retry", fields: []string{"error", "consecutive_failures", "max_failures"}},
	"agent_suspended":           {kind: "suspended", fields: []string{"error", "blocker_code", "action"}},
	"budget_exceeded":           {kind: "budget_exceeded", fields: []string{"session_cost_usd", "cap_usd"}},
	"game_error":                {kind: "error", fields: []string{"error"}},
	"game_end":                  {kind: "evaluation_ended", fields: []string{"game_result", "completion_reason", "error"}},
	"external_update":           {kind: "spec_updated", fields: []string{"message"}},
}

// activityRow returns the display row for a user-facing evidence record, or
// nil for internal activity such as tool calls. Events
// addressed to another audience are internal; progress written before
// audiences existed was addressed to the user.
func activityRow(record map[string]interface{}) map[string]interface{} {
	event, _ := record["event"].(string)
	spec, ok := activityKinds[event]
	data, _ := record["data"].(map[string]interface{})
	audience := data["audience"] // null means written before audiences
	if !ok || (!spec.anyAudience && audience != nil && audience != "user") || (spec.userOnly && audience == nil) {
		return nil
	}
	row := map[string]interface{}{"ts": record["ts"], "kind": spec.kind}
	if role := record["role"]; role == "prover" || role == "verifier" {
		row["role"] = role
	}
	for _, field := range spec.fields {
		if value := data[field]; value != nil && value != "" {
			row[field] = value
		}
	}
	// The earliest game_end events named their outcome `result`.
	if result := data["result"]; spec.kind == "evaluation_ended" && row["game_result"] == nil && result != nil && result != "" {
		row["game_result"] = result
	}
	if spec.required != "" && row[spec.required] == nil {
		return nil
	}
	return row
}

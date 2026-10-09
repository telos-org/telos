package main

import (
	"encoding/json"
	"os"
	"strings"
)

func printJSON(v interface{}) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}

// goalJSON prints a runtime session record the way the CLI names it, as a
// Goal: top-level keys such as session_id and parent_session_id become goal_id
// and parent_goal_id. The records keep their names on disk and on the wire.
func goalJSON(record any) any {
	data, err := json.Marshal(record)
	if err != nil {
		return record
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return record
	}
	goal := make(map[string]json.RawMessage, len(fields))
	for key, value := range fields {
		goal[strings.Replace(key, "session", "goal", 1)] = value
	}
	return goal
}

func goalJSONList[T any](records []T) []any {
	goals := make([]any, 0, len(records))
	for _, record := range records {
		goals = append(goals, goalJSON(record))
	}
	return goals
}

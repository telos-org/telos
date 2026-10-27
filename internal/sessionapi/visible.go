package sessionapi

import "strings"

// visibleEvent reports whether a person sees event in a Goal's activity, as
// GET /api/sessions/{id}/events?visible=true returns it. Events addressed to
// another audience are internal. Events written before audiences existed
// were for the user, except round markers and the agent's own notes. Cloud
// applies the same rules to runtimes without ?visible;
// testdata/visible_events.json keeps them equal.
func visibleEvent(event SessionEvent) bool {
	audience := event.Data["audience"]
	forUser := audience == nil || audience == "user"
	switch event.Event {
	case "round_start":
		return audience == "user"
	case "agent_progress":
		text, _ := event.Data["text"].(string)
		kind, _ := event.Data["kind"].(string)
		return forUser && strings.TrimSpace(text) != "" && !(audience == nil && LegacyAgentNote(kind, text))
	case "game_start", "agent_failure_recoverable", "agent_suspended",
		"budget_exceeded", "game_error", "game_end", "external_update":
		return forUser
	}
	return false
}

// LegacyAgentNote reports whether progress written before audiences existed
// was the agent's own note: a tool, review or summary entry, or a routine
// file or command action.
func LegacyAgentNote(kind, text string) bool {
	switch strings.ToLower(kind) {
	case "tool", "review", "summary":
		return true
	case "", "progress_update":
		trimmed := strings.TrimSpace(text)
		for _, prefix := range []string{
			"Reading ", "Editing ", "Creating ", "Writing ", "Updating ", "Deleting ",
			"Running shell command", "Running kubectl", "Running Node build step",
		} {
			if strings.HasPrefix(trimmed, prefix) {
				return true
			}
		}
	}
	return false
}

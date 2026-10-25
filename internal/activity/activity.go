// Package activity projects evidence into the public, display-ready Activity contract.
package activity

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Entry keeps source positions separate from display content. Sequence gaps are
// expected: hidden evidence still advances the source's replay watermark.
type Entry struct {
	ID        string  `json:"id"`
	EventSeq  *int64  `json:"event_seq,omitempty"`
	Seq       *int64  `json:"seq,omitempty"`
	Timestamp *string `json:"ts"`
	Kind      string  `json:"kind"`
	Headline  string  `json:"headline"`
	Body      *string `json:"body"`
	Tone      string  `json:"tone"`
	Phase     string  `json:"phase"`
}

func text(value any) string { s, _ := value.(string); return strings.TrimSpace(s) }
func number(value any) *int64 {
	var n int64
	switch v := value.(type) {
	case float64:
		if v != float64(int64(v)) {
			return nil
		}
		n = int64(v)
	case int64:
		n = v
	case json.Number:
		parsed, err := v.Int64()
		if err != nil {
			return nil
		}
		n = parsed
	default:
		return nil
	}
	if n <= 0 {
		return nil
	}
	return &n
}
func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
func capitalize(s string) string {
	_, width := utf8.DecodeRuneInString(s)
	return strings.ToUpper(s[:width]) + s[width:]
}
func human(s string) string {
	return capitalize(strings.NewReplacer("_", " ", ".", " ").Replace(s))
}

// Project returns nil for internal activity. No event text is deduplicated or grouped.
func Project(raw map[string]any, fallbackID string, costs bool) *Entry {
	data, _ := raw["data"].(map[string]any)
	name, audience, role := text(raw["event"]), text(data["audience"]), text(raw["role"])
	stamp := text(raw["ts"])
	if stamp == "" {
		stamp = text(raw["time"])
	}
	e := &Entry{ID: fallbackID, EventSeq: number(raw["event_seq"]), Seq: number(raw["seq"]), Timestamp: optional(stamp), Kind: "lifecycle", Tone: "neutral", Phase: "system"}
	if e.EventSeq != nil {
		e.ID = fmt.Sprintf("rt:%d", *e.EventSeq)
	} else if e.Seq != nil {
		e.ID = fmt.Sprintf("cp:%d", *e.Seq)
	}
	control := strings.HasPrefix(name, "deployment.") || strings.HasPrefix(name, "runtime.") || strings.HasPrefix(name, "provisioning.")
	failed := strings.HasSuffix(name, ".failed") || strings.HasSuffix(name, "_failed") || strings.Contains(name, ".error")
	if control && !failed && strings.HasSuffix(name, ".started") {
		return nil
	}
	if raw["activity_truncated"] == true {
		e.Headline = "This activity entry was too large to display"
		e.Tone = "warning"
		return e
	}
	switch name {
	case "agent_progress":
		e.Headline = text(data["text"])
		if e.Headline == "" || (audience != "" && audience != "user") || (audience == "" && text(data["kind"]) == "tool") {
			return nil
		}
		e.Kind = "narrative"
		if audience == "user" {
			e.Kind = "goal"
		}
		e.Phase = "build"
		lower := strings.ToLower(e.Headline)
		if role == "verifier" {
			e.Phase = "verify"
		} else if strings.Contains(lower, "test") || strings.Contains(lower, "probe") || strings.Contains(lower, "check") {
			e.Phase = "test"
		}
	case "round_start":
		if audience != "user" || (role != "prover" && role != "verifier") {
			return nil
		}
		e.Headline = "Working on the spec requirements"
		e.Phase = "build"
		if role == "verifier" {
			e.Headline = "Checking the result against the spec"
			e.Phase = "verify"
		}
	case "game_start":
		e.Headline = "Evaluation cycle started"
		e.Phase = "verify"
	case "agent_complete":
		if audience != "" {
			return nil
		}
		e.Headline = "Implementation turn completed"
		e.Phase = "build"
		if role == "verifier" {
			e.Phase = "verify"
			switch text(data["status"]) {
			case "CONCEDE":
				e.Headline = "Current spec accepted"
				e.Tone = "success"
			case "CONTINUE":
				e.Headline = "Verification requested another iteration"
			}
		}
		parts := []string{}
		if n := number(data["num_turns"]); n != nil {
			unit := "turns"
			if *n == 1 {
				unit = "turn"
			}
			parts = append(parts, fmt.Sprintf("%d %s", *n, unit))
		}
		if cost, ok := data["cost_usd"].(float64); ok && costs && cost > 0 {
			parts = append(parts, fmt.Sprintf("$%.2f", cost))
		}
		if model := text(data["model"]); model != "" {
			parts = append(parts, model)
		}
		e.Body = optional(strings.Join(parts, " · "))
	case "agent_failure_recoverable":
		err := text(data["error"])
		lower := strings.ToLower(err)
		e.Headline = "Agent error, retrying"
		switch {
		case strings.HasPrefix(lower, "502") || strings.Contains(lower, "no healthy upstream") || strings.Contains(lower, "public routes unavailable"):
			e.Headline = "Model provider unavailable"
		case strings.HasPrefix(lower, "429"):
			e.Headline = "Model provider at capacity"
		case strings.Contains(lower, "socket connection was closed"):
			e.Headline = "Agent connection interrupted"
		case strings.Contains(lower, "agent_no_output"):
			e.Headline = "Agent returned no output"
		}
		if n, m := number(data["consecutive_failures"]), number(data["max_failures"]); n != nil && m != nil {
			if err != "" {
				err += " · "
			}
			err += fmt.Sprintf("attempt %d of %d", *n, *m)
		}
		e.Body = optional(err)
		e.Tone = "warning"
		e.Phase = "build"
		if role == "verifier" {
			e.Phase = "verify"
		}
	case "agent_suspended":
		e.Headline = "Execution suspended"
		e.Tone = "danger"
		detail := text(data["error"])
		if detail == "" {
			detail = text(data["blocker_code"])
		}
		if action := text(data["action"]); action != "" {
			if detail != "" {
				detail += " · "
			}
			detail += action
		}
		e.Body = optional(detail)
	case "budget_exceeded":
		e.Headline = "Budget requires attention"
		e.Tone = "warning"
		spent, ok := data["session_cost_usd"].(float64)
		cap, ok2 := data["cap_usd"].(float64)
		if costs && ok && ok2 {
			e.Body = optional(fmt.Sprintf("$%.2f spent of $%.2f cap", spent, cap))
		}
	case "game_error":
		e.Headline = "Evaluation error"
		e.Tone = "danger"
		e.Body = optional(text(data["error"]))
	case "game_end":
		e.Phase = "verify"
		result := text(data["game_result"])
		if result == "" {
			result = text(data["result"])
		}
		reason := text(data["completion_reason"])
		err := text(data["error"])
		e.Headline = "Evaluation cycle completed"
		switch result {
		case "failure":
			e.Headline = "Evaluation cycle interrupted"
			e.Tone = "danger"
			if err == "" {
				err = human(reason)
			}
			e.Body = optional(err)
		case "success":
			e.Tone = "success"
			if audience != "user" {
				e.Headline = "Current spec accepted"
			}
			if reason != "success" && !(audience == "user" && reason == "verifier_conceded") {
				e.Body = optional(human(reason))
			}
		case "stopped":
			e.Headline = "Evaluation stopped"
			e.Body = optional(err)
		}
	default:
		if !control {
			return nil
		}
		e.Phase = "deploy"
		if failed {
			e.Tone = "danger"
		}
		e.Headline = text(raw["message"])
		if e.Headline == "" {
			e.Headline = human(name)
		} else {
			e.Headline = capitalize(e.Headline)
		}
		if name == "deployment.protection_changed" {
			meta, _ := raw["metadata"].(map[string]any)
			if required, ok := meta["require_change_requests"].(bool); ok {
				e.Headline = "Change request requirement disabled"
				if required {
					e.Headline = "Change request requirement enabled"
				}
			}
		}
	}
	return e
}

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/telos-org/telos/internal/sessionapi"
)

const defaultLogTail = 50

type logViewOptions struct {
	Tail int
	All  bool
}

type renderedLogRow struct {
	Timestamp       string
	Level           string
	Summary         string
	Detail          string
	MultilineDetail bool
}

func printStructuredLogs(
	out io.Writer,
	events []sessionapi.SessionEvent,
	options logViewOptions,
) {
	rows := renderLogRows(events)
	if !options.All && options.Tail > 0 && len(rows) > options.Tail {
		rows = rows[len(rows)-options.Tail:]
	}
	if len(rows) == 0 {
		fmt.Fprintln(out, "No activity yet.")
		return
	}
	for _, row := range rows {
		printRenderedLogRow(out, row)
	}
}

func printJSONLogEvents(out io.Writer, events []sessionapi.SessionEvent) error {
	return printJSONLogEventsForContext(out, events, "")
}

func printJSONLogEventsForContext(
	out io.Writer,
	events []sessionapi.SessionEvent,
	contextName string,
) error {
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	for _, event := range events {
		value := any(event)
		if contextName != "" {
			value = struct {
				sessionapi.SessionEvent
				Context string `json:"context"`
			}{
				SessionEvent: event,
				Context:      contextName,
			}
		}
		if err := encoder.Encode(value); err != nil {
			return err
		}
	}
	return nil
}

func selectLogEvents(
	events []sessionapi.SessionEvent,
	options logViewOptions,
) []sessionapi.SessionEvent {
	if options.All || options.Tail >= len(events) {
		return events
	}
	return events[len(events)-options.Tail:]
}

func renderLogRows(events []sessionapi.SessionEvent) []renderedLogRow {
	rows := make([]renderedLogRow, 0, len(events))
	for _, event := range events {
		row, ok := renderedLogRowFromEvent(event)
		if !ok {
			continue
		}
		if len(rows) > 0 && sameLogMessage(rows[len(rows)-1], row) {
			continue
		}
		rows = append(rows, row)
	}
	return rows
}

func sameLogMessage(left, right renderedLogRow) bool {
	return left.Level == right.Level && left.Summary == right.Summary && left.Detail == right.Detail
}

func renderedLogRowFromEvent(event sessionapi.SessionEvent) (renderedLogRow, bool) {
	row := renderedLogRow{
		Timestamp: eventTimestamp(event),
		Level:     "INFO",
	}
	role := eventRole(event)

	switch event.Event {
	case "agent_progress":
		text := eventDataString(event, "text")
		kind := strings.ToLower(eventDataString(event, "kind"))
		if text == "" || kind == "tool" || kind == "review" || kind == "summary" ||
			isLegacyToolLogAction(kind, text) {
			return renderedLogRow{}, false
		}
		row.Summary, row.Detail = splitLogText(text)
		return row, true
	case "agent_complete":
		status := strings.ToUpper(eventDataString(event, "status"))
		if status != "CONCEDE" || role != "verifier" {
			return renderedLogRow{}, false
		}
		row.Summary = "Current revision accepted"
		return row, true
	case "agent_failure_recoverable":
		errorText := eventDataString(event, "error")
		if errorText == "" {
			return renderedLogRow{}, false
		}
		row.Level = "WARNING"
		row.Summary = friendlyAgentError(errorText) + "; retrying"
		if current, ok := numericEventValue(event.Data["consecutive_failures"]); ok {
			if maxFailures, hasMax := numericEventValue(event.Data["max_failures"]); hasMax {
				row.Detail = fmt.Sprintf("attempt %d of %d", current, maxFailures)
			}
		}
		return row, true
	case "agent_suspended":
		row.Level = "ERROR"
		row.Summary = "Execution suspended"
		row.Detail = firstNonEmpty(
			eventDataString(event, "error"),
			eventDataString(event, "blocker_code"),
		)
		if action := eventDataString(event, "action"); action != "" {
			if row.Detail == "" {
				row.Detail = action
			} else {
				row.Detail += " · " + action
			}
		}
		row.MultilineDetail = strings.Contains(row.Detail, "\n")
		return row, true
	case "game_end":
		result := strings.ToLower(eventDataString(event, "game_result"))
		if result == "" {
			result = strings.ToLower(eventDataString(event, "result"))
		}
		reason := eventDataString(event, "completion_reason")
		switch result {
		case "success":
			row.Summary = "Current revision accepted"
		case "failure":
			row.Level = "ERROR"
			row.Summary = "Execution failed"
			row.Detail = firstNonEmpty(eventDataString(event, "error"), humanizeLogToken(reason))
		case "stopped":
			row.Level = "WARNING"
			row.Summary = "Execution stopped"
			row.Detail = eventDataString(event, "error")
		default:
			return renderedLogRow{}, false
		}
		row.MultilineDetail = strings.Contains(row.Detail, "\n")
		return row, true
	case "budget_exceeded":
		row.Level = "ERROR"
		row.Summary = "Budget exceeded"
		row.Detail = eventDataString(event, "error")
		return row, true
	case "game_error":
		row.Level = "ERROR"
		row.Summary = "Execution error"
		row.Detail = eventDataString(event, "error")
		return row, true
	case "external_update":
		row.Summary = firstNonEmpty(eventDataString(event, "message"), "Revision updated")
		row.Detail = firstNonEmpty(
			eventDataString(event, "current_revision"),
			eventDataString(event, "current_spec_sha256"),
		)
		return row, true
	case "game_start", "round_start", "workspace_checkpoint":
		return renderedLogRow{}, false
	}

	if isRoutineLogEvent(event.Event) {
		return renderedLogRow{}, false
	}
	message := eventDataString(event, "message")
	if !strings.HasPrefix(event.Event, "deployment.") &&
		!strings.HasPrefix(event.Event, "workload.") &&
		!strings.HasPrefix(event.Event, "runtime.") &&
		!strings.HasPrefix(event.Event, "provisioning.") &&
		!strings.HasPrefix(event.Event, "hostd.") {
		return renderedLogRow{}, false
	}
	row.Level = logLevelForEvent(event)
	if message != "" {
		row.Summary, row.Detail = splitLogText(message)
		row.Summary = sentenceCase(row.Summary)
	} else {
		row.Summary = humanizeLogToken(event.Event)
	}
	if row.Summary == "" {
		return renderedLogRow{}, false
	}
	return row, true
}

func isRoutineLogEvent(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	return strings.Contains(name, "heartbeat") ||
		strings.Contains(name, "lease.renew") ||
		strings.HasSuffix(name, ".poll")
}

func logLevelForEvent(event sessionapi.SessionEvent) string {
	name := strings.ToLower(strings.TrimSpace(event.Event))
	state := strings.ToLower(eventDataString(event, "state"))
	switch {
	case isTerminalLogFailureEvent(name), state == "failed":
		return "ERROR"
	case strings.HasSuffix(name, ".waiting"), strings.Contains(name, "retry"), state == "waiting":
		return "WARNING"
	default:
		return "INFO"
	}
}

func printRenderedLogRow(out io.Writer, row renderedLogRow) {
	prefix := fmt.Sprintf(
		"[%s] [%s]",
		displayLogTimestamp(row.Timestamp),
		strings.ToUpper(firstNonEmpty(row.Level, "INFO")),
	)
	summary := strings.TrimSpace(row.Summary)
	detail := strings.TrimSpace(row.Detail)
	if detail == "" {
		fmt.Fprintf(out, "%s %s\n", prefix, summary)
		return
	}
	if !row.MultilineDetail && !strings.Contains(detail, "\n") {
		fmt.Fprintf(out, "%s %s — %s\n", prefix, summary, detail)
		return
	}
	fmt.Fprintf(out, "%s %s\n", prefix, summary)
	indent := strings.Repeat(" ", len(prefix)+1)
	for _, line := range strings.Split(detail, "\n") {
		fmt.Fprintf(out, "%s│ %s\n", indent, strings.TrimRight(line, " \t\r"))
	}
}

func eventTimestamp(event sessionapi.SessionEvent) string {
	if event.Timestamp == nil {
		return ""
	}
	return strings.TrimSpace(*event.Timestamp)
}

func eventRole(event sessionapi.SessionEvent) string {
	if event.Role == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(*event.Role))
}

func eventDataString(event sessionapi.SessionEvent, key string) string {
	value, _ := event.Data[key].(string)
	return strings.TrimSpace(value)
}

func isTerminalLogFailureEvent(event string) bool {
	name := strings.ToLower(strings.TrimSpace(event))
	if name == "agent_failure_recoverable" {
		return false
	}
	return strings.HasSuffix(name, ".failed") ||
		strings.HasSuffix(name, "_failed") ||
		strings.HasSuffix(name, ".error")
}

func isMinorLogAction(text string) bool {
	trimmed := strings.TrimSpace(text)
	for _, prefix := range []string{
		"Reading ",
		"Editing ",
		"Creating ",
		"Writing ",
		"Updating ",
		"Deleting ",
		"Running shell command",
		"Running kubectl",
		"Running Node build step",
	} {
		if strings.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return false
}

func isLegacyToolLogAction(kind string, text string) bool {
	return (kind == "" || kind == "progress_update") && isMinorLogAction(text)
}

func friendlyAgentError(errorText string) string {
	lower := strings.ToLower(errorText)
	switch {
	case strings.HasPrefix(lower, "502"), strings.Contains(lower, "no healthy upstream"), strings.Contains(lower, "public routes unavailable"):
		return "Model provider unavailable"
	case strings.HasPrefix(lower, "429"):
		return "Model provider at capacity"
	case strings.Contains(lower, "socket connection was closed"):
		return "Execution connection interrupted"
	case lower == "agent_no_output":
		return "Execution returned no output"
	default:
		return "Execution error"
	}
}

func splitLogText(value string) (string, string) {
	normalized := strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if normalized == "" {
		return "", ""
	}
	cut := -1
	for _, marker := range []string{". ", ": "} {
		if index := strings.Index(normalized, marker); index > 0 && (cut == -1 || index < cut) {
			cut = index
		}
	}
	if cut == -1 || cut > 110 {
		return conciseLogText(normalized, 140), ""
	}
	summary := strings.TrimSuffix(normalized[:cut], ".")
	detailStart := cut + 2
	if detailStart >= len(normalized) {
		return summary, ""
	}
	return summary, normalized[detailStart:]
}

func conciseLogText(value string, limit int) string {
	normalized := strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if limit <= 0 || utf8.RuneCountInString(normalized) <= limit {
		return normalized
	}
	runes := []rune(normalized)
	return strings.TrimSpace(string(runes[:limit-1])) + "…"
}

func humanizeLogToken(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return sentenceCase(strings.ReplaceAll(strings.ReplaceAll(value, "_", " "), ".", " "))
}

func sentenceCase(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	runes := []rune(value)
	runes[0] = []rune(strings.ToUpper(string(runes[0])))[0]
	return string(runes)
}

func displayLogTimestamp(value string) string {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "unknown-time"
	}
	return parsed.UTC().Format("2006-01-02T15:04:05Z")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

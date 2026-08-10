package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/sessionapi"
)

const defaultLogTail = 50

type logViewOptions struct {
	Verbose bool
	Tail    int
	All     bool
}

type logHeader struct {
	Name       string
	State      string
	PackageRef string
	SessionID  string
	UpdatedAt  string
	Failure    string
}

type overallLogStatus struct {
	Label  string
	Reason string
}

type renderedLogRow struct {
	Timestamp       string
	Source          string
	Phase           string
	Summary         string
	Detail          string
	MultilineDetail bool
}

func cloudLogHeader(session *cloud.SessionRecord) logHeader {
	failure := ""
	if session.FailureReason != nil {
		failure = strings.TrimSpace(*session.FailureReason)
	}
	return logHeader{
		Name:       session.Name,
		State:      session.State,
		PackageRef: session.PackageRef,
		SessionID:  session.ID,
		UpdatedAt:  session.UpdatedAt,
		Failure:    failure,
	}
}

func localLogHeader(session *sessionapi.Session) logHeader {
	name := session.SessionID
	if session.SpecName != nil && strings.TrimSpace(*session.SpecName) != "" {
		name = strings.TrimSpace(*session.SpecName)
	}
	failure := ""
	if session.Error != nil {
		failure = strings.TrimSpace(*session.Error)
	}
	updatedAt := ""
	if session.FinishedAt != nil {
		updatedAt = *session.FinishedAt
	}
	return logHeader{
		Name:      name,
		State:     string(session.Status),
		SessionID: session.SessionID,
		UpdatedAt: updatedAt,
		Failure:   failure,
	}
}

func printStructuredLogs(
	out io.Writer,
	header logHeader,
	events []sessionapi.SessionEvent,
	options logViewOptions,
) {
	status := deriveOverallLogStatus(header, events)
	fmt.Fprintln(out, strings.ToUpper(header.Name))
	fmt.Fprintln(out)
	printSummaryField(out, "Status", status.Label)
	printSummaryField(out, "Summary", status.Reason)
	if header.PackageRef != "" {
		printSummaryField(out, "Spec", header.PackageRef)
	}
	if header.UpdatedAt != "" {
		printSummaryField(out, "Updated", displayLogDate(header.UpdatedAt))
	}
	printSummaryField(out, "Session", header.SessionID)

	rows := renderLogRows(events, options.Verbose)
	if !options.All && options.Tail > 0 && len(rows) > options.Tail {
		rows = rows[len(rows)-options.Tail:]
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "ACTIVITY")
	if len(rows) == 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "No activity yet.")
		return
	}
	fmt.Fprintln(out)
	for _, row := range rows {
		printRenderedLogRow(out, row)
	}
}

func printJSONLogEvents(out io.Writer, events []sessionapi.SessionEvent) error {
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	for _, event := range events {
		if err := encoder.Encode(event); err != nil {
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

func deriveOverallLogStatus(
	header logHeader,
	events []sessionapi.SessionEvent,
) overallLogStatus {
	state := strings.ToLower(strings.TrimSpace(header.State))
	switch state {
	case "deleted", "stopped", "deleting":
		reason := "This session was stopped."
		if strings.EqualFold(strings.TrimSpace(header.State), "deleting") {
			reason = "Stopping this deployment."
		}
		return overallLogStatus{Label: "Stopped", Reason: firstNonEmpty(header.Failure, reason)}
	case "failed", "error", "errored", "stale":
		return overallLogStatus{Label: "Failed", Reason: firstNonEmpty(header.Failure, "The session could not complete.")}
	}
	if strings.TrimSpace(header.Failure) != "" {
		return overallLogStatus{Label: "Needs attention", Reason: strings.TrimSpace(header.Failure)}
	}
	starting := state == "pending" || state == "provisioning" || state == "deploying"

	reset := -1
	latestConcede := -1
	latestContinue := -1
	latestFailure := -1
	latestWork := -1
	latestWorkText := ""
	latestNotice := -1
	latestNoticeText := ""
	for index, event := range events {
		switch event.Event {
		case "external_update", "deployment.update_accepted":
			reset = index
			latestConcede = -1
			latestContinue = -1
			latestFailure = -1
			latestWork = -1
			latestWorkText = ""
			latestNotice = -1
			latestNoticeText = ""
		case "agent_complete":
			status := strings.ToUpper(eventDataString(event, "status"))
			role := eventRole(event)
			if status == "CONCEDE" && role == "verifier" {
				latestConcede = index
			} else if status == "CONTINUE" && role == "verifier" {
				latestContinue = index
				latestWork = index
				latestWorkText = "Verification requested another iteration."
			} else {
				latestWork = index
				latestWorkText = "Implementation turn completed; verification is next."
			}
		case "game_end":
			result := strings.ToLower(eventDataString(event, "game_result"))
			if result == "" {
				result = strings.ToLower(eventDataString(event, "result"))
			}
			if result == "success" {
				latestConcede = index
			} else if result == "failure" || result == "stopped" {
				latestFailure = index
			}
		case "budget_exceeded", "game_error":
			latestFailure = index
		case "agent_failure_recoverable":
			latestNotice = index
			notice := strings.TrimSuffix(
				friendlyAgentError(eventDataString(event, "error")),
				", retrying",
			)
			latestNoticeText = notice + "; retrying."
		case "game_start", "agent_progress":
			latestWork = index
			if event.Event == "agent_progress" {
				text := eventDataString(event, "text")
				if text != "" && !isMinorLogAction(text) {
					latestWorkText = text
				}
			}
		default:
			if isTerminalLogFailureEvent(event.Event) {
				latestFailure = index
			}
		}
	}

	if latestConcede > reset && latestConcede > latestContinue && latestConcede > latestFailure {
		return overallLogStatus{
			Label:  "Ready",
			Reason: "Current spec accepted; latest verification completed successfully.",
		}
	}
	if latestFailure > reset && latestFailure > latestConcede && latestFailure > latestWork {
		failure := eventFailure(events[latestFailure])
		return overallLogStatus{
			Label:  "Needs attention",
			Reason: firstNonEmpty(failure, "Progress stopped on an evaluation error."),
		}
	}
	if latestNotice > reset && latestNotice > latestWork {
		return overallLogStatus{Label: "Working", Reason: latestNoticeText}
	}
	if starting && latestWork <= reset {
		return overallLogStatus{Label: "Starting", Reason: "Preparing the deployment and runtime."}
	}
	return overallLogStatus{
		Label:  "Working",
		Reason: firstNonEmpty(conciseLogText(latestWorkText, 120), "Implementing or verifying the current spec."),
	}
}

func renderLogRows(events []sessionapi.SessionEvent, verbose bool) []renderedLogRow {
	rows := make([]renderedLogRow, 0, len(events))
	for _, event := range events {
		row, ok := renderedLogRowFromEvent(event, verbose)
		if !ok {
			continue
		}
		rows = append(rows, row)
	}
	return rows
}

func renderedLogRowFromEvent(
	event sessionapi.SessionEvent,
	verbose bool,
) (renderedLogRow, bool) {
	row := renderedLogRow{
		Timestamp: eventTimestamp(event),
		Source:    logEventSource(event),
		Phase:     "SYSTEM",
	}
	role := eventRole(event)

	switch event.Event {
	case "agent_progress":
		text := eventDataString(event, "text")
		kind := strings.ToLower(eventDataString(event, "kind"))
		if text == "" || (!verbose && (kind == "tool" || isLegacyToolLogAction(kind, text))) {
			return renderedLogRow{}, false
		}
		if kind == "tool" {
			row.Phase = "TOOL"
			row.Summary = text
			return row, true
		}
		row.Phase = progressPhase(role, text)
		if kind == "review" || kind == "summary" {
			row.Summary = sentenceCase(kind)
			row.Detail = text
			row.MultilineDetail = true
			return row, true
		}
		row.Summary, row.Detail = splitLogText(text)
		return row, true
	case "agent_complete":
		status := strings.ToUpper(eventDataString(event, "status"))
		if status == "" {
			return renderedLogRow{}, false
		}
		row.Phase = progressPhase(role, "")
		switch {
		case status == "CONCEDE" && role == "verifier":
			row.Summary = "Current spec accepted"
		case role == "verifier":
			row.Summary = "Verification requested another iteration"
		case verbose:
			row.Summary = "Implementation turn completed"
		default:
			return renderedLogRow{}, false
		}
		row.Detail = agentLogDetail(event)
		return row, true
	case "agent_failure_recoverable":
		errorText := eventDataString(event, "error")
		if errorText == "" {
			return renderedLogRow{}, false
		}
		row.Phase = "RETRY"
		row.Summary = friendlyAgentError(errorText)
		row.Detail = errorText
		if current, ok := numericEventValue(event.Data["consecutive_failures"]); ok {
			if maxFailures, hasMax := numericEventValue(event.Data["max_failures"]); hasMax {
				row.Detail += fmt.Sprintf(" · attempt %d of %d", current, maxFailures)
			}
		}
		row.MultilineDetail = strings.Contains(row.Detail, "\n")
		return row, true
	case "game_end":
		row.Phase = "VERIFY"
		result := strings.ToLower(eventDataString(event, "game_result"))
		if result == "" {
			result = strings.ToLower(eventDataString(event, "result"))
		}
		reason := eventDataString(event, "completion_reason")
		switch result {
		case "success":
			row.Summary = "Current spec accepted"
			if reason != "" && reason != "success" {
				row.Detail = humanizeLogToken(reason)
			}
		case "failure":
			row.Summary = "Evaluation cycle interrupted"
			row.Detail = firstNonEmpty(eventDataString(event, "error"), humanizeLogToken(reason))
		case "stopped":
			row.Summary = "Evaluation stopped"
			row.Detail = eventDataString(event, "error")
		default:
			row.Summary = "Evaluation cycle completed"
			row.Detail = humanizeLogToken(reason)
		}
		row.MultilineDetail = strings.Contains(row.Detail, "\n")
		return row, true
	case "budget_exceeded":
		row.Phase = "SYSTEM"
		row.Summary = "Budget requires attention"
		row.Detail = eventDataString(event, "error")
		return row, true
	case "game_error":
		row.Phase = "SYSTEM"
		row.Summary = "Evaluation error"
		row.Detail = eventDataString(event, "error")
		return row, true
	case "external_update":
		row.Phase = "SPEC"
		row.Summary = firstNonEmpty(eventDataString(event, "message"), "Spec updated")
		return row, true
	case "game_start":
		if !verbose {
			return renderedLogRow{}, false
		}
		row.Phase = "VERIFY"
		row.Summary = "Evaluation cycle started"
		return row, true
	case "round_start":
		if !verbose {
			return renderedLogRow{}, false
		}
		row.Phase = progressPhase(role, "")
		row.Summary = sentenceCase(firstNonEmpty(role, "Agent")) + " round started"
		return row, true
	case "workspace_checkpoint":
		if !verbose {
			return renderedLogRow{}, false
		}
		row.Phase = "SYSTEM"
		row.Summary = "Workspace checkpoint saved"
		if size, ok := numericEventValue(event.Data["bytes"]); ok {
			row.Detail = formatLogBytes(int64(size))
		}
		return row, true
	}

	message := eventDataString(event, "message")
	if strings.HasPrefix(event.Event, "deployment.") {
		row.Phase = "DEPLOY"
	} else if strings.HasPrefix(event.Event, "runtime.") || strings.HasPrefix(event.Event, "provisioning.") {
		row.Phase = "START"
	} else if !verbose {
		return renderedLogRow{}, false
	}
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

func printRenderedLogRow(out io.Writer, row renderedLogRow) {
	prefix := fmt.Sprintf(
		"[%s] [%s] [%s]",
		displayLogTimestamp(row.Timestamp),
		strings.ToLower(firstNonEmpty(row.Source, "system")),
		strings.ToUpper(firstNonEmpty(row.Phase, "system")),
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

func logEventSource(event sessionapi.SessionEvent) string {
	name := strings.ToLower(strings.TrimSpace(event.Event))
	switch {
	case strings.HasPrefix(name, "agent_"):
		return "agent"
	case strings.HasPrefix(name, "deployment."), name == "external_update":
		return "control"
	case strings.HasPrefix(name, "hostd."):
		return "hostd"
	case strings.HasPrefix(name, "workload."):
		return "workload"
	case strings.HasPrefix(name, "runtime."), strings.HasPrefix(name, "provisioning."):
		return "runtime"
	case strings.HasPrefix(name, "game_"), strings.HasPrefix(name, "round_"),
		strings.HasPrefix(name, "workspace_"), name == "budget_exceeded":
		return "runtime"
	default:
		return "system"
	}
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

func progressPhase(role string, text string) string {
	if role == "verifier" {
		return "VERIFY"
	}
	lower := strings.ToLower(text)
	if strings.Contains(lower, "test") || strings.Contains(lower, "probe") || strings.Contains(lower, "check") {
		return "TEST"
	}
	return "BUILD"
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

func agentLogDetail(event sessionapi.SessionEvent) string {
	parts := []string{}
	if turns, ok := numericEventValue(event.Data["num_turns"]); ok && turns > 0 {
		parts = append(parts, fmt.Sprintf("%d turns", turns))
	}
	if model := eventDataString(event, "model"); model != "" {
		parts = append(parts, model)
	}
	return strings.Join(parts, " · ")
}

func eventFailure(event sessionapi.SessionEvent) string {
	if errorText := eventDataString(event, "error"); errorText != "" {
		return errorText
	}
	if reason := eventDataString(event, "reason"); reason != "" {
		return reason
	}
	if reason := eventDataString(event, "completion_reason"); reason != "" {
		return humanizeLogToken(reason)
	}
	if message := eventDataString(event, "message"); message != "" {
		return message
	}
	return ""
}

func friendlyAgentError(errorText string) string {
	lower := strings.ToLower(errorText)
	switch {
	case strings.HasPrefix(lower, "502"), strings.Contains(lower, "no healthy upstream"), strings.Contains(lower, "public routes unavailable"):
		return "Model provider unavailable"
	case strings.HasPrefix(lower, "429"):
		return "Model provider at capacity"
	case strings.Contains(lower, "socket connection was closed"):
		return "Agent connection interrupted"
	case lower == "agent_no_output":
		return "Agent returned no output"
	default:
		return "Agent error, retrying"
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

func displayLogDate(value string) string {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return value
	}
	return parsed.UTC().Format("2006-01-02 15:04:05 UTC")
}

func formatLogBytes(value int64) string {
	const (
		kib = int64(1024)
		mib = 1024 * kib
		gib = 1024 * mib
	)
	switch {
	case value >= gib:
		return fmt.Sprintf("%.1f GiB", float64(value)/float64(gib))
	case value >= mib:
		return fmt.Sprintf("%.1f MiB", float64(value)/float64(mib))
	case value >= kib:
		return fmt.Sprintf("%.1f KiB", float64(value)/float64(kib))
	default:
		return fmt.Sprintf("%d B", value)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

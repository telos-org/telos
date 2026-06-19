package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/telos-org/telos/internal/sessionapi"
)

// -- describe -----------------------------------------------------------------

func cmdDescribe(args []string) {
	fs := flag.NewFlagSet("describe", flag.ExitOnError)
	env := fs.String("env", "", "Cloud environment")
	jsonOut := fs.Bool("json", false, "JSON output")
	parseFlags(fs, args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: telos describe SESSION [--json]")
		os.Exit(1)
	}
	sessionID := fs.Arg(0)

	session, err := getSessionFromAnywhere(sessionID, *env)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		printJSON(session)
		return
	}

	printSessionDescription(os.Stdout, *session)
}

func printSessionDescription(out io.Writer, session sessionapi.Session) {
	row := displayRow(session)
	printSummaryField(out, "Name", row.Name)
	printSummaryField(out, "Platform", row.Platform)
	printSummaryField(out, "Status", row.Status)
	printSummaryField(out, "Cost", formatDetailCost(session.TotalCostUSD))
	printSummaryField(out, "Session", row.Session)

	fmt.Fprintln(out)
	fmt.Fprintln(out, "Lifecycle")
	printDetailField(out, "api status", string(session.Status))
	printDetailField(out, "result", sessionRawResult(session))
	printDetailField(out, "kind", sessionKind(session))
	if session.ParentSessionID != nil && *session.ParentSessionID != "" {
		printDetailField(out, "parent", *session.ParentSessionID)
	} else {
		printDetailField(out, "parent", "-")
	}
	if interval := sessionInterval(session); interval != "" {
		printDetailField(out, "interval", interval)
	}
	printDetailField(out, "current turn", sessionTurn(session))
	printDetailField(out, "created", optionalString(session.CreatedAt))
	printDetailField(out, "finished", optionalString(session.FinishedAt))
	if session.CurrentSpecVersion != nil {
		printDetailField(out, "spec version", fmt.Sprint(*session.CurrentSpecVersion))
	}
	if session.CompletionReason != nil && *session.CompletionReason != "" {
		printDetailField(out, "completion", *session.CompletionReason)
	}
	if session.VerifierConceded != nil {
		printDetailField(out, "evaluation", evaluationDisposition(session))
	}
	if session.Error != nil {
		printDetailField(out, "error", *session.Error)
	}
	if session.ErrorCode != nil && *session.ErrorCode != "" {
		printDetailField(out, "error code", *session.ErrorCode)
	}
	if session.RoundCount != nil {
		printDetailField(out, "rounds", fmt.Sprint(*session.RoundCount))
	}
	printRuntimeSummary(out, session)
	if session.ArtifactURI != nil && *session.ArtifactURI != "" {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Artifact")
		fmt.Fprintf(out, "  %s\n", *session.ArtifactURI)
	}
	if len(session.Epochs) > 0 {
		fmt.Fprintln(out)
		printLatestEpoch(out, session)
	}
	if len(session.Specs) > 0 {
		fmt.Fprintln(out)
		printSessionArtifacts(out, session)
	}
}

func printSummaryField(out io.Writer, label string, value string) {
	fmt.Fprintf(out, "%-9s %s\n", label, orDash(value))
}

func printDetailField(out io.Writer, label string, value string) {
	fmt.Fprintf(out, "  %-14s %s\n", label, orDash(value))
}

func optionalString(value *string) string {
	if value == nil {
		return "-"
	}
	return *value
}

func sessionInterval(session sessionapi.Session) string {
	if len(session.Specs) == 0 || session.Specs[0].IntervalSeconds == nil {
		return ""
	}
	seconds := *session.Specs[0].IntervalSeconds
	if seconds <= 0 {
		return ""
	}
	if seconds%3600 == 0 {
		return fmt.Sprintf("%dh", seconds/3600)
	}
	if seconds%60 == 0 {
		return fmt.Sprintf("%dm", seconds/60)
	}
	return fmt.Sprintf("%ds", seconds)
}

func sessionRawResult(session sessionapi.Session) string {
	if session.Result != nil && *session.Result != "" {
		return *session.Result
	}
	if result := latestEpochString(session, "result"); result != "" {
		return result
	}
	return "-"
}

func evaluationDisposition(session sessionapi.Session) string {
	if !session.Status.IsTerminal() {
		return "pending"
	}
	if session.VerifierConceded != nil && *session.VerifierConceded {
		return "accepted"
	}
	if session.CompletionReason != nil && *session.CompletionReason == "review_cycles_complete" {
		return "review cycles complete (acceptance not used)"
	}
	return "not accepted"
}

func printLatestEpoch(out io.Writer, session sessionapi.Session) {
	fmt.Fprintln(out, "Latest Epoch")
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "RESULT\tSTARTED\tFINISHED")
	fmt.Fprintf(w, "%s\t%s\t%s\n",
		sessionRawResult(session),
		orDash(latestEpochString(session, "started_at")),
		orDash(latestEpochString(session, "finished_at")),
	)
	_ = w.Flush()
}

func printSessionArtifacts(out io.Writer, session sessionapi.Session) {
	fmt.Fprintln(out, "Paths")
	if session.ActiveWorkspacePath != nil || session.ActiveWorkspaceExists != nil {
		printDetailField(out, "active workspace", artifactPath(session.ActiveWorkspaceExists, session.ActiveWorkspacePath))
	}
	for _, spec := range session.Specs {
		prefix := sessionSpecName(spec)
		printDetailField(out, prefix+" workspace", artifactPath(spec.WorkspaceExists, spec.WorkspacePath))
		printDetailField(out, prefix+" evidence", artifactPath(spec.EvidenceExists, spec.EvidencePath))
		printDetailField(out, prefix+" transcript", artifactPath(spec.TranscriptExists, spec.TranscriptPath))
		printDetailField(out, prefix+" ledger", artifactPath(spec.ObjectiveLedgerExists, spec.ObjectiveLedgerPath))
	}
}

func printRuntimeSummary(out io.Writer, session sessionapi.Session) {
	if len(session.Config) == 0 &&
		session.TotalInputTokens == nil &&
		session.TotalOutputTokens == nil &&
		session.TotalCacheReadTokens == nil &&
		session.TotalCacheCreateTokens == nil {
		return
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Runtime")
	printDetailField(out, "model", stringConfig(session.Config, "model"))
	printDetailField(out, "thinking", stringConfig(session.Config, "thinking"))
	printDetailField(out, "budgets", formatSessionBudgets(session.Config))
	printDetailField(out, "tokens", formatSessionTokens(session))
}

func stringConfig(config map[string]any, key string) string {
	if value, ok := config[key].(string); ok {
		return value
	}
	return "-"
}

func formatSessionBudgets(config map[string]any) string {
	parts := []string{}
	if value := numberConfig(config, "max_cost_usd"); value != "" {
		parts = append(parts, "cost $"+value)
	}
	if value := numberConfig(config, "max_rounds"); value != "" {
		parts = append(parts, "rounds "+value)
	}
	if value := numberConfig(config, "max_duration_sec"); value != "" {
		parts = append(parts, "duration "+value+"s")
	}
	if value := numberConfig(config, "max_input_tokens"); value != "" {
		parts = append(parts, "input "+value)
	}
	if value := numberConfig(config, "max_output_tokens"); value != "" {
		parts = append(parts, "output "+value)
	}
	if value := numberConfig(config, "max_tool_loops"); value != "" {
		parts = append(parts, "tool-loops "+value)
	}
	if value := numberConfig(config, "agent_timeout_sec"); value != "" {
		parts = append(parts, "agent-timeout "+value+"s")
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ", ")
}

func numberConfig(config map[string]any, key string) string {
	switch value := config[key].(type) {
	case int:
		if value == 0 {
			return ""
		}
		return fmt.Sprint(value)
	case int64:
		if value == 0 {
			return ""
		}
		return fmt.Sprint(value)
	case float64:
		if value == 0 {
			return ""
		}
		return fmt.Sprintf("%g", value)
	case float32:
		if value == 0 {
			return ""
		}
		return fmt.Sprintf("%g", value)
	default:
		return ""
	}
}

func formatSessionTokens(session sessionapi.Session) string {
	parts := []string{}
	if session.TotalInputTokens != nil {
		parts = append(parts, fmt.Sprintf("input %d", *session.TotalInputTokens))
	}
	if session.TotalOutputTokens != nil {
		parts = append(parts, fmt.Sprintf("output %d", *session.TotalOutputTokens))
	}
	if session.TotalCacheReadTokens != nil {
		parts = append(parts, fmt.Sprintf("cache-read %d", *session.TotalCacheReadTokens))
	}
	if session.TotalCacheCreateTokens != nil {
		parts = append(parts, fmt.Sprintf("cache-write %d", *session.TotalCacheCreateTokens))
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ", ")
}

func sessionSpecName(spec sessionapi.SessionSpec) string {
	if spec.Name != nil && *spec.Name != "" {
		return *spec.Name
	}
	if spec.DirName != nil && *spec.DirName != "" {
		return *spec.DirName
	}
	return "-"
}

func artifactPath(exists *bool, path *string) string {
	if exists != nil && !*exists {
		return "missing"
	}
	if path != nil && *path != "" {
		return fileURI(*path)
	}
	if exists != nil && *exists {
		return "present"
	}
	return "-"
}

func orDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

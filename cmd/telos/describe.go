package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/sessionapi"
)

// -- describe -----------------------------------------------------------------

func cmdDescribe(args []string) {
	fs := flag.NewFlagSet("describe", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "JSON output")
	parseFlags(fs, args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: telos describe SESSION [--json]")
		os.Exit(1)
	}
	sessionID := fs.Arg(0)

	session, err := getSessionFromAnywhere(sessionID)
	if err == nil {
		if *jsonOut {
			printJSON(session)
			return
		}

		printSessionDescription(os.Stdout, *session)
		return
	}

	cloudSession, found, cloudErr := getCloudSessionIfConfigured(sessionID)
	if cloudErr != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", cloudErr)
		os.Exit(1)
	}
	if found {
		if *jsonOut {
			printJSON(cloudSession)
			return
		}
		printCloudSessionDescription(os.Stdout, *cloudSession)
		return
	}

	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}

func getCloudSession(sessionID string) (*cloud.SessionRecord, error) {
	control, err := cloud.ControlClient()
	if err != nil {
		return nil, err
	}
	return control.GetSession(sessionID)
}

func printCloudSessionDescription(out io.Writer, session cloud.SessionRecord) {
	printSummaryField(out, "Name", session.Name)
	printSummaryField(out, "Target", "cloud")
	printSummaryField(out, "Status", session.State)
	printSummaryField(out, "Package", session.PackageRef)
	printSummaryField(out, "Digest", session.PackageDigest)
	printSummaryField(out, "Session", session.ID)
	if session.RuntimeVersion != nil && *session.RuntimeVersion != "" {
		printSummaryField(out, "Runtime", *session.RuntimeVersion)
	}
	if session.ServiceURL != nil && *session.ServiceURL != "" {
		printSummaryField(out, "Service", *session.ServiceURL)
	}
	if session.DashboardURL != nil && *session.DashboardURL != "" {
		printSummaryField(out, "Dashboard", *session.DashboardURL)
	}
	if session.FailureReason != nil && *session.FailureReason != "" {
		printSummaryField(out, "Error", *session.FailureReason)
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, "Lifecycle")
	printDetailField(out, "created", session.CreatedAt)
	printDetailField(out, "updated", session.UpdatedAt)
}

func printSessionDescription(out io.Writer, session sessionapi.Session) {
	row := displayRow(session)
	printSummaryField(out, "Name", row.Name)
	printSummaryField(out, "Target", row.Target)
	printSummaryField(out, "Status", row.Status)
	printSummaryField(out, "Cost", formatDetailCost(session.TotalCostUSD))
	printSummaryField(out, "Session", row.Session)

	fmt.Fprintln(out)
	fmt.Fprintln(out, "Lifecycle")
	printDetailField(out, "api status", string(session.Status))
	printDetailField(out, "result", sessionRawResult(session))
	printDetailField(out, "lineage", sessionLineage(session))
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
	if session.RoundCount != nil {
		printDetailField(out, "rounds", fmt.Sprint(*session.RoundCount))
	}
	if serviceURL := sessionServiceURL(session); serviceURL != "" {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Service")
		fmt.Fprintf(out, "  %s\n", serviceURL)
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
	}
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

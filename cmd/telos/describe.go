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

	if isDeploymentID(sessionID) {
		deployment, err := getDeployment(sessionID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if *jsonOut {
			printJSON(deployment)
			return
		}
		printDeploymentDescription(os.Stdout, *deployment)
		return
	}

	session, err := getSessionFromAnywhere(sessionID)
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

func getDeployment(deploymentID string) (*cloud.DeploymentRecord, error) {
	control, err := cloud.ControlClient()
	if err != nil {
		return nil, err
	}
	return control.GetDeployment(deploymentID)
}

func printDeploymentDescription(out io.Writer, deployment cloud.DeploymentRecord) {
	printSummaryField(out, "Name", deployment.Name)
	printSummaryField(out, "Platform", "cloud")
	printSummaryField(out, "Status", deployment.State)
	printSummaryField(out, "Package", deployment.PackageRef)
	printSummaryField(out, "Digest", deployment.PackageDigest)
	printSummaryField(out, "Deployment", deployment.ID)
	if deployment.RuntimeVersion != nil && *deployment.RuntimeVersion != "" {
		printSummaryField(out, "Runtime", *deployment.RuntimeVersion)
	}
	if deployment.ServiceURL != nil && *deployment.ServiceURL != "" {
		printSummaryField(out, "Service", *deployment.ServiceURL)
	}
	if deployment.DashboardURL != nil && *deployment.DashboardURL != "" {
		printSummaryField(out, "Dashboard", *deployment.DashboardURL)
	}
	if deployment.FailureReason != nil && *deployment.FailureReason != "" {
		printSummaryField(out, "Error", *deployment.FailureReason)
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, "Lifecycle")
	printDetailField(out, "created", deployment.CreatedAt)
	printDetailField(out, "updated", deployment.UpdatedAt)
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

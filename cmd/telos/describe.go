package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/sessionapi"
)

// -- describe -----------------------------------------------------------------

func cmdDescribe(args []string) {
	fs := flag.NewFlagSet("describe", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "JSON output")
	verbose := fs.Bool("verbose", false, "Include runtime allocation details")
	contextValue := cloudContextFlag(fs)
	parseFlags(fs, args)
	contextOverride, err := cloudContextOverride(fs, *contextValue)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: telos describe SESSION [--json] [--verbose] [--context CONTEXT]")
		os.Exit(1)
	}
	sessionID := fs.Arg(0)
	if contextOverride != "" {
		cloudSession, contextName, err := getCloudSessionForContext(sessionID, contextOverride)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		progress, progressErr := loadCloudSessionProgress(cloudSession, contextOverride, time.Now())
		if *jsonOut {
			printCloudSessionJSONWithProgress(cloudSession, contextName, progress, progressErr)
			return
		}
		printCloudSessionDescriptionWithProgress(
			os.Stdout,
			*cloudSession,
			contextName,
			progress,
			*verbose,
			progressErr,
		)
		return
	}

	session, err := getSessionFromAnywhere(sessionID)
	if err == nil {
		if *jsonOut {
			printJSON(session)
			return
		}

		printSessionDescription(os.Stdout, *session)
		return
	}

	cloudSession, contextName, found, cloudErr := getCloudSessionIfConfigured(sessionID, "")
	if cloudErr != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", cloudErr)
		os.Exit(1)
	}
	if found {
		progress, progressErr := loadCloudSessionProgress(cloudSession, "", time.Now())
		if *jsonOut {
			printCloudSessionJSONWithProgress(cloudSession, contextName, progress, progressErr)
			return
		}
		printCloudSessionDescriptionWithProgress(
			os.Stdout,
			*cloudSession,
			contextName,
			progress,
			*verbose,
			progressErr,
		)
		return
	}

	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}

func printCloudSessionJSON(session *cloud.SessionRecord, contextName string) {
	printCloudSessionJSONWithProgress(
		session,
		contextName,
		deriveCloudSessionProgress(session, nil, time.Now()),
		nil,
	)
}

func printCloudSessionJSONWithProgress(
	session *cloud.SessionRecord,
	contextName string,
	progress cloudSessionProgress,
	progressErr error,
) {
	progressError := ""
	if progressErr != nil {
		progressError = progressErr.Error()
	}
	printJSON(struct {
		*cloud.SessionRecord
		Context       string               `json:"context"`
		Progress      cloudSessionProgress `json:"progress"`
		ProgressError string               `json:"progress_error,omitempty"`
	}{
		SessionRecord: session,
		Context:       contextName,
		Progress:      progress,
		ProgressError: progressError,
	})
}

func getCloudSession(sessionID, contextOverride string) (*cloud.SessionRecord, error) {
	session, _, err := getCloudSessionForContext(sessionID, contextOverride)
	return session, err
}

func getCloudSessionForContext(
	sessionID string,
	contextOverride string,
) (*cloud.SessionRecord, string, error) {
	control, err := cloud.ControlClientForContext(contextOverride)
	if err != nil {
		return nil, "", err
	}
	session, err := control.GetSession(sessionID)
	if err != nil {
		return nil, "", err
	}
	return session, resolvedCloudContext(control), nil
}

func printCloudSessionDescription(out io.Writer, session cloud.SessionRecord) {
	printCloudSessionDescriptionForContext(out, session, "")
}

func printCloudSessionDescriptionForContext(
	out io.Writer,
	session cloud.SessionRecord,
	contextName string,
) {
	printCloudSessionDescriptionWithProgress(
		out,
		session,
		contextName,
		deriveCloudSessionProgress(&session, nil, time.Now()),
		false,
		nil,
	)
}

func printCloudSessionDescriptionWithProgress(
	out io.Writer,
	session cloud.SessionRecord,
	contextName string,
	progress cloudSessionProgress,
	verbose bool,
	progressErr error,
) {
	printSummaryField(out, "Name", session.Name)
	printSummaryField(out, "Target", "cloud")
	printSummaryField(out, "Status", cloudSessionDisplayStatus(session))
	printSummaryField(out, "Stage", progress.Stage)
	printSummaryField(out, "Stage for", formatAge(progress.StageAgeSeconds))
	if progress.LatestActivity != "" {
		printSummaryField(out, "Latest", progress.LatestActivity)
		printSummaryField(out, "Activity age", formatAge(progress.LatestActivityAgeSeconds))
	}
	printSummaryField(out, "Package", session.PackageRef)
	printSummaryField(out, "Digest", session.PackageDigest)
	printSummaryField(out, "Session", session.ID)
	if contextName != "" {
		printSummaryField(out, "Context", contextName)
	}
	if session.RuntimeVersion != nil && *session.RuntimeVersion != "" {
		printSummaryField(out, "Runtime", *session.RuntimeVersion)
	}
	if session.ServiceURL != nil && *session.ServiceURL != "" {
		printSummaryField(out, "Service", *session.ServiceURL)
	} else {
		printSummaryField(out, "Service", "pending")
	}
	if session.DashboardURL != nil && *session.DashboardURL != "" {
		printSummaryField(out, "Dashboard", *session.DashboardURL)
	} else {
		printSummaryField(out, "Dashboard", "pending")
	}
	if session.FailureReason != nil && *session.FailureReason != "" {
		printSummaryField(out, "Error", *session.FailureReason)
	} else if progress.WaitingReason != "" {
		printSummaryField(out, "Waiting", progress.WaitingReason)
	}
	if verbose {
		if progress.RuntimeProvider != "" {
			printSummaryField(out, "Runtime provider", progress.RuntimeProvider)
		}
		if progress.Allocation != "" {
			printSummaryField(out, "Allocation", progress.Allocation)
		}
		if progress.Host != "" {
			printSummaryField(out, "Host", progress.Host)
		}
		if progressErr != nil {
			printSummaryField(out, "Progress error", progressErr.Error())
		}
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, "Lifecycle")
	printDetailField(out, "state", session.State)
	if session.StatusReason != "" {
		printDetailField(out, "status reason", session.StatusReason)
	}
	printDetailField(out, "created", session.CreatedAt)
	printDetailField(out, "updated", session.UpdatedAt)
	fmt.Fprintln(out)
	if contextName != "" {
		fmt.Fprintf(out, "Inspect   telos logs --context %s %s\n", contextName, session.ID)
	} else {
		fmt.Fprintf(out, "Inspect   telos logs %s\n", session.ID)
	}
}

func cloudSessionDisplayStatus(session cloud.SessionRecord) string {
	if session.Status != "" {
		return session.Status
	}
	return session.State
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
	if session.VerifierConceded != nil && *session.VerifierConceded {
		return "accepted"
	}
	if !session.Status.IsTerminal() {
		return "pending"
	}
	if session.CompletionReason != nil && *session.CompletionReason == "review_budget_exhausted" {
		return "review budget exhausted"
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

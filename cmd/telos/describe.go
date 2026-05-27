package main

import (
	"flag"
	"fmt"
	"io"
	"os"
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
	fmt.Fprintf(out, "Session:  %s\n", session.SessionID)
	fmt.Fprintf(out, "Name:     %s\n", sessionName(session))
	fmt.Fprintf(out, "Kind:     %s\n", sessionKind(session))
	fmt.Fprintf(out, "Runtime:  %s\n", session.Runtime)
	if session.ParentSessionID != nil && *session.ParentSessionID != "" {
		fmt.Fprintf(out, "Parent:   %s\n", *session.ParentSessionID)
	}
	fmt.Fprintf(out, "Status:   %s\n", session.Status)
	fmt.Fprintf(out, "Result:   %s\n", sessionResult(session))
	if session.CompletionReason != nil && *session.CompletionReason != "" {
		fmt.Fprintf(out, "Complete: %s\n", *session.CompletionReason)
	}
	if session.VerifierConceded != nil {
		fmt.Fprintf(out, "Evaluate: %s\n", evaluationDisposition(session))
	}
	if session.ArtifactURI != nil && *session.ArtifactURI != "" {
		fmt.Fprintf(out, "Artifact: %s\n", *session.ArtifactURI)
	}
	if session.CreatedAt != nil {
		fmt.Fprintf(out, "Created:  %s\n", *session.CreatedAt)
	}
	if session.FinishedAt != nil {
		fmt.Fprintf(out, "Finished: %s\n", *session.FinishedAt)
	}
	if session.CurrentSpecVersion != nil {
		fmt.Fprintf(out, "Spec Ver: %d\n", *session.CurrentSpecVersion)
	}
	if session.Error != nil {
		fmt.Fprintf(out, "Error:    %s\n", *session.Error)
	}
	if session.TotalCostUSD != nil {
		fmt.Fprintf(out, "Cost:     $%.4f\n", *session.TotalCostUSD)
	}
	if session.RoundCount != nil {
		fmt.Fprintf(out, "Rounds:   %d\n", *session.RoundCount)
	}
	if turn := sessionTurn(session); turn != "-" {
		fmt.Fprintf(out, "Turn:     %s\n", turn)
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
	fmt.Fprintln(out, "Latest Epoch:")
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tRESULT\tSTARTED\tFINISHED")
	fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
		latestEpochString(session, "id"),
		sessionResult(session),
		orDash(latestEpochString(session, "started_at")),
		orDash(latestEpochString(session, "finished_at")),
	)
	_ = w.Flush()
}

func printSessionArtifacts(out io.Writer, session sessionapi.Session) {
	fmt.Fprintln(out, "Artifacts:")
	for _, spec := range session.Specs {
		fmt.Fprintf(out, "  %s:\n", sessionSpecName(spec))
		fmt.Fprintf(out, "    workspace:  %s\n", artifactPath(spec.WorkspaceExists, spec.WorkspacePath))
		fmt.Fprintf(out, "    evidence:   %s\n", artifactPath(spec.EvidenceExists, spec.EvidencePath))
		fmt.Fprintf(out, "    transcript: %s\n", artifactPath(spec.TranscriptExists, spec.TranscriptPath))
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
		if exists == nil {
			return *path
		}
		return "yes:" + *path
	}
	if exists != nil && *exists {
		return "yes"
	}
	return "-"
}

func orDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

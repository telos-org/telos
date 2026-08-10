package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/sessionapi"
)

// -- delete -------------------------------------------------------------------

func cmdDelete(args []string) {
	fs := flag.NewFlagSet("delete", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "JSON output")
	contextValue := cloudContextFlag(fs)
	parseFlags(fs, args)
	contextOverride, err := cloudContextOverride(fs, *contextValue)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: telos delete SESSION [--json] [--context CONTEXT]")
		os.Exit(1)
	}
	sessionID := fs.Arg(0)

	if isCloudApplyID(sessionID) || contextOverride != "" {
		cloudSession, contextName, err := deleteCloudSessionForContext(sessionID, contextOverride)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if *jsonOut {
			printCloudSessionJSON(cloudSession, contextName)
			return
		}
		printCloudSessionDeleteReceiptForContext(os.Stdout, *cloudSession, contextName)
		return
	}

	session, err := stopSessionAnywhere(sessionID)
	if err == nil {
		if *jsonOut {
			printJSON(session)
			return
		}
		printLocalSessionDeleteReceipt(os.Stdout, *session)
		return
	}

	cloudSession, contextName, found, cloudErr := deleteCloudSessionIfConfigured(sessionID, "")
	if cloudErr != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", cloudErr)
		os.Exit(1)
	}
	if found {
		if *jsonOut {
			printCloudSessionJSON(cloudSession, contextName)
			return
		}
		printCloudSessionDeleteReceiptForContext(os.Stdout, *cloudSession, contextName)
		return
	}

	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}

func deleteCloudSessionForContext(
	sessionID string,
	contextOverride string,
) (*cloud.SessionRecord, string, error) {
	control, err := cloud.ControlClientForContext(contextOverride)
	if err != nil {
		return nil, "", err
	}
	session, err := control.DeleteSession(sessionID)
	if err != nil {
		return nil, "", err
	}
	return session, resolvedCloudContext(control), nil
}

func deleteCloudSessionIfConfigured(
	sessionID string,
	contextOverride string,
) (*cloud.SessionRecord, string, bool, error) {
	if _, contextName, found, err := getCloudSessionIfConfigured(sessionID, contextOverride); err != nil || !found {
		return nil, contextName, found, err
	}
	cloudSession, contextName, err := deleteCloudSessionForContext(sessionID, contextOverride)
	if err != nil {
		return nil, contextName, true, err
	}
	return cloudSession, contextName, true, nil
}

func printCloudSessionDeleteReceipt(out io.Writer, session cloud.SessionRecord) {
	printCloudSessionDeleteReceiptForContext(out, session, "")
}

func printCloudSessionDeleteReceiptForContext(
	out io.Writer,
	session cloud.SessionRecord,
	contextName string,
) {
	switch session.State {
	case "deleted":
		fmt.Fprintf(out, "deleted %s\n\n", session.Name)
	default:
		fmt.Fprintf(out, "delete requested for %s\n\n", session.Name)
	}
	printSummaryField(out, "Name", session.Name)
	printSummaryField(out, "Target", "cloud")
	printSummaryField(out, "Status", cloudSessionDisplayStatus(session))
	printSummaryField(out, "Package", session.PackageRef)
	printSummaryField(out, "Session", session.ID)
	if contextName != "" {
		printSummaryField(out, "Context", contextName)
	}
}

func printLocalSessionDeleteReceipt(out io.Writer, session sessionapi.Session) {
	fmt.Fprintf(out, "deleted %s (history preserved)\n\n", deletedSessionName(session))
	row := displayRow(session)
	printSummaryField(out, "Name", row.Name)
	printSummaryField(out, "Target", row.Target)
	printSummaryField(out, "Status", row.Status)
	printSummaryField(out, "Cost", formatDetailCost(session.TotalCostUSD))
	printSummaryField(out, "Session", row.Session)
}

func deletedSessionName(session sessionapi.Session) string {
	if name := sessionName(session); name != "-" {
		return name
	}
	if session.SessionID != "" {
		return session.SessionID
	}
	return "-"
}

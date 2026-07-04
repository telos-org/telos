package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/sessionapi"
)

// -- stop ---------------------------------------------------------------------

func cmdStop(args []string) {
	fs := flag.NewFlagSet("stop", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "JSON output")
	parseFlags(fs, args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: telos stop SESSION [--json]")
		os.Exit(1)
	}
	sessionID := fs.Arg(0)

	session, err := stopSessionAnywhere(sessionID)
	if err == nil {
		if *jsonOut {
			printJSON(session)
			return
		}
		printStopReceipt(os.Stdout, *session)
		return
	}

	cloudSession, found, cloudErr := deleteCloudSessionIfConfigured(sessionID)
	if cloudErr != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", cloudErr)
		os.Exit(1)
	}
	if found {
		if *jsonOut {
			printJSON(cloudSession)
			return
		}
		printCloudSessionDeleteReceipt(os.Stdout, *cloudSession)
		return
	}

	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}

func cmdDelete(args []string) {
	fs := flag.NewFlagSet("delete", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "JSON output")
	parseFlags(fs, args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: telos delete SESSION [--json]")
		os.Exit(1)
	}
	sessionID := fs.Arg(0)

	cloudSession, err := deleteCloudSession(sessionID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if *jsonOut {
		printJSON(cloudSession)
		return
	}
	printCloudSessionDeleteReceipt(os.Stdout, *cloudSession)
}

func deleteCloudSession(sessionID string) (*cloud.SessionRecord, error) {
	control, err := cloud.ControlClient()
	if err != nil {
		return nil, err
	}
	return control.DeleteSession(sessionID)
}

func deleteCloudSessionIfConfigured(sessionID string) (*cloud.SessionRecord, bool, error) {
	if _, found, err := getCloudSessionIfConfigured(sessionID); err != nil || !found {
		return nil, found, err
	}
	cloudSession, err := deleteCloudSession(sessionID)
	if err != nil {
		return nil, true, err
	}
	return cloudSession, true, nil
}

func printCloudSessionDeleteReceipt(out io.Writer, session cloud.SessionRecord) {
	switch session.State {
	case "deleted":
		fmt.Fprintf(out, "deleted %s\n\n", session.Name)
	default:
		fmt.Fprintf(out, "delete requested for %s\n\n", session.Name)
	}
	printSummaryField(out, "Name", session.Name)
	printSummaryField(out, "Target", "cloud")
	printSummaryField(out, "Status", session.State)
	printSummaryField(out, "Package", session.PackageRef)
	printSummaryField(out, "Session", session.ID)
}

func printStopReceipt(out io.Writer, session sessionapi.Session) {
	fmt.Fprintf(out, "stopped %s\n\n", stopOperationName(session))
	row := displayRow(session)
	printSummaryField(out, "Name", row.Name)
	printSummaryField(out, "Target", row.Target)
	printSummaryField(out, "Status", row.Status)
	printSummaryField(out, "Cost", formatDetailCost(session.TotalCostUSD))
	printSummaryField(out, "Session", row.Session)
}

func stopOperationName(session sessionapi.Session) string {
	if name := sessionName(session); name != "-" {
		return name
	}
	if session.SessionID != "" {
		return session.SessionID
	}
	return "-"
}

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

	deployment, found, cloudErr := deleteCloudDeploymentIfConfigured(sessionID)
	if cloudErr != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", cloudErr)
		os.Exit(1)
	}
	if found {
		if *jsonOut {
			printJSON(deployment)
			return
		}
		printDeploymentDeleteReceipt(os.Stdout, *deployment)
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
	deploymentID := fs.Arg(0)

	deployment, err := deleteDeployment(deploymentID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if *jsonOut {
		printJSON(deployment)
		return
	}
	printDeploymentDeleteReceipt(os.Stdout, *deployment)
}

func deleteDeployment(deploymentID string) (*cloud.DeploymentRecord, error) {
	control, err := cloud.ControlClient()
	if err != nil {
		return nil, err
	}
	return control.DeleteDeployment(deploymentID)
}

func deleteCloudDeploymentIfConfigured(deploymentID string) (*cloud.DeploymentRecord, bool, error) {
	if _, found, err := getCloudDeploymentIfConfigured(deploymentID); err != nil || !found {
		return nil, found, err
	}
	deployment, err := deleteDeployment(deploymentID)
	if err != nil {
		return nil, true, err
	}
	return deployment, true, nil
}

func printDeploymentDeleteReceipt(out io.Writer, deployment cloud.DeploymentRecord) {
	switch deployment.State {
	case "deleted":
		fmt.Fprintf(out, "deleted %s\n\n", deployment.Name)
	default:
		fmt.Fprintf(out, "delete requested for %s\n\n", deployment.Name)
	}
	printSummaryField(out, "Name", deployment.Name)
	printSummaryField(out, "Target", "cloud")
	printSummaryField(out, "Status", deployment.State)
	printSummaryField(out, "Package", deployment.PackageRef)
	printSummaryField(out, "Session", deployment.ID)
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

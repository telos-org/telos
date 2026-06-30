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
	env := fs.String("env", "", "Cloud environment")
	jsonOut := fs.Bool("json", false, "JSON output")
	parseFlags(fs, args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: telos stop SESSION [--json]")
		os.Exit(1)
	}
	sessionID := fs.Arg(0)

	if isDeploymentID(sessionID) && *env == "" {
		deployment, err := deleteDeployment(sessionID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if *jsonOut {
			printJSON(deployment)
			return
		}
		printDeploymentStopReceipt(os.Stdout, *deployment)
		return
	}

	session, err := stopSessionAnywhere(sessionID, *env)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		printJSON(session)
		return
	}
	printStopReceipt(os.Stdout, *session)
}

func cmdDelete(args []string) {
	fs := flag.NewFlagSet("delete", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "JSON output")
	parseFlags(fs, args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: telos delete DEPLOYMENT [--json]")
		os.Exit(1)
	}
	deploymentID := fs.Arg(0)
	if !isDeploymentID(deploymentID) {
		fmt.Fprintln(os.Stderr, "error: telos delete only accepts deployment IDs")
		os.Exit(1)
	}

	deployment, err := deleteDeployment(deploymentID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if *jsonOut {
		printJSON(deployment)
		return
	}
	printDeploymentStopReceipt(os.Stdout, *deployment)
}

func deleteDeployment(deploymentID string) (*cloud.DeploymentRecord, error) {
	control, err := cloud.ControlClient()
	if err != nil {
		return nil, err
	}
	return control.DeleteDeployment(deploymentID)
}

func printDeploymentStopReceipt(out io.Writer, deployment cloud.DeploymentRecord) {
	fmt.Fprintf(out, "stopped %s\n\n", deployment.Name)
	printSummaryField(out, "Name", deployment.Name)
	printSummaryField(out, "Platform", "cloud")
	printSummaryField(out, "Status", deployment.State)
	printSummaryField(out, "Package", deployment.PackageRef)
	printSummaryField(out, "Deployment", deployment.ID)
}

func printStopReceipt(out io.Writer, session sessionapi.Session) {
	fmt.Fprintf(out, "stopped %s\n\n", stopOperationName(session))
	row := displayRow(session)
	printSummaryField(out, "Name", row.Name)
	printSummaryField(out, "Platform", row.Platform)
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

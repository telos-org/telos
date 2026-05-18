package main

import (
	"flag"
	"fmt"
	"os"
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

	fmt.Printf("Session:  %s\n", session.SessionID)
	fmt.Printf("Status:   %s\n", session.Status)
	fmt.Printf("Runtime:  %s\n", session.Runtime)
	if session.SpecName != nil {
		fmt.Printf("Spec:     %s\n", *session.SpecName)
	}
	if session.CreatedAt != nil {
		fmt.Printf("Created:  %s\n", *session.CreatedAt)
	}
	if session.Result != nil {
		fmt.Printf("Result:   %s\n", *session.Result)
	}
	if session.Error != nil {
		fmt.Printf("Error:    %s\n", *session.Error)
	}
	if session.TotalCostUSD != nil {
		fmt.Printf("Cost:     $%.4f\n", *session.TotalCostUSD)
	}
	if session.RoundCount != nil {
		fmt.Printf("Rounds:   %d\n", *session.RoundCount)
	}
}

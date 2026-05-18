package main

import (
	"flag"
	"fmt"
	"os"
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

	session, err := stopSessionAnywhere(sessionID, *env)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		printJSON(session)
		return
	}
	fmt.Printf("session %s: %s\n", session.SessionID, session.Status)
}

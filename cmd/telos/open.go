package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/telos-org/telos/internal/cloud"
)

func cmdOpen(args []string) {
	fs := flag.NewFlagSet("open", flag.ExitOnError)
	target := fs.String("target", "service", "Surface to open: service or dashboard")
	path := fs.String("path", "/", "Absolute path on the surface")
	jsonOut := fs.Bool("json", false, "JSON output")
	parseFlags(fs, args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: telos open SESSION [--target service|dashboard] [--path /...] [--json]")
		os.Exit(1)
	}
	sessionID := fs.Arg(0)

	control, err := cloud.ControlClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	response, err := control.OpenSession(sessionID, *target, *path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if *jsonOut {
		printJSON(response)
		return
	}
	fmt.Println(response.URL)
}

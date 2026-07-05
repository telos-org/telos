package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/telos-org/telos/internal/cloud"
)

func cmdOpen(args []string) {
	fs := flag.NewFlagSet("open", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "JSON output")
	target := fs.String("target", "service", "Surface to open: service or dashboard")
	path := fs.String("path", "", "Path on the selected surface")
	parseFlags(fs, args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: telos open SESSION [--target service|dashboard] [--path PATH] [--json]")
		os.Exit(1)
	}
	sessionID := fs.Arg(0)

	response, err := openCloudSession(sessionID, *target, *path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if *jsonOut {
		printJSON(response)
		return
	}
	printOpenReceipt(os.Stdout, *response)
}

func openCloudSession(sessionID, target, path string) (*cloud.SessionOpenResponse, error) {
	control, err := cloud.ControlClient()
	if err != nil {
		return nil, err
	}
	return control.OpenSession(sessionID, target, path)
}

func printOpenReceipt(out io.Writer, response cloud.SessionOpenResponse) {
	fmt.Fprintln(out, response.URL)
}

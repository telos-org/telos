package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"time"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/sessionapi"
)

func cmdExec(args []string) {
	fs := newCommandFlagSet("exec", "telos exec GOAL [flags] -- COMMAND [ARG...]")
	timeout := fs.Duration("timeout", 30*time.Second, "Command timeout (1 ms to 5 minutes)")
	jsonOut := fs.Bool("json", false, "JSON output with base64-encoded stdout and stderr")
	contextValue := cloudContextFlag(fs)
	separator := slices.Index(args, "--")
	var argv []string
	if separator >= 0 {
		argv, args = args[separator+1:], args[:separator]
	}
	parseFlags(fs, args)
	requireArgCount(fs, 1, "one GOAL before --")
	if len(argv) == 0 {
		fmt.Fprintln(os.Stderr, "error: provide a command after --")
		os.Exit(2)
	}
	if *timeout < time.Millisecond || *timeout > 5*time.Minute {
		fmt.Fprintln(os.Stderr, "error: --timeout must be between 1 ms and 5 minutes")
		os.Exit(2)
	}
	request := sessionapi.SessionExecRequest{Argv: argv, TimeoutMS: int(timeout.Milliseconds())}
	if err := request.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	contextOverride, err := cloudContextOverride(fs, *contextValue)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	goalID := fs.Arg(0)
	if err := validateCloudSessionContext(goalID, contextOverride); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	if isLocalApplyID(goalID) {
		fmt.Fprintln(os.Stderr, "error: exec works only for Cloud Goals")
		os.Exit(2)
	}
	control, err := cloud.ControlClientForContext(contextOverride)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	result, err := control.ExecSession(ctx, goalID, request)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", goalNotFound(err, goalID, control.ContextName()))
		if ctx.Err() != nil {
			os.Exit(130)
		}
		os.Exit(1)
	}
	if *jsonOut {
		printJSON(result)
	} else {
		_, _ = os.Stdout.Write(result.Stdout)
		_, _ = os.Stderr.Write(result.Stderr)
		if result.StdoutTruncated {
			fmt.Fprintln(os.Stderr, "\nstdout truncated at 1 MiB")
		}
		if result.StderrTruncated {
			fmt.Fprintln(os.Stderr, "\nstderr truncated at 1 MiB")
		}
	}
	if result.ExitCode != 0 {
		os.Exit(result.ExitCode)
	}
}

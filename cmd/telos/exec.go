package main

import (
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"time"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/sessionapi"
)

// -- exec ---------------------------------------------------------------------

// execFailed is the exit code of telos exec when Telos fails, as ssh uses it.
// 2 is a usage error, and any other code is the command's own.
const execFailed = 255

const execUsage = "telos exec GOAL [flags] -- COMMAND [ARG...]\n\n" +
	"Runs COMMAND, with no shell, in the workspace of a Cloud Goal that\n" +
	"telos pause has paused. Exits with COMMAND's exit code, with 255 if Telos\n" +
	"fails, and with 2 for a usage error."

// cmdExec runs one command in a paused Goal's workspace and passes on its
// output and exit code.
func cmdExec(args []string) {
	// Everything after the first -- is the command, sent as given.
	var argv []string
	if split := slices.Index(args, "--"); split >= 0 {
		args, argv = args[:split], args[split+1:]
	}
	fs := newCommandFlagSet("exec", execUsage)
	stdinPath := fs.String("stdin", "", "File to give the command as its stdin, or - for this command's stdin")
	timeout := fs.Duration("timeout", time.Minute, "How long the command may run, from 1s to 80s")
	jsonOut := fs.Bool("json", false, "JSON output, with stdout and stderr as base64")
	contextValue := cloudContextFlag(fs)
	parseFlags(fs, args)
	contextOverride, err := cloudContextOverride(fs, *contextValue)
	if err != nil {
		execUsageError(fs, err.Error())
	}
	goalID := fs.Arg(0)
	switch {
	case fs.NArg() != 1 || len(argv) == 0:
		execUsageError(fs, "expected GOAL -- COMMAND")
	case isLocalApplyID(goalID):
		execUsageError(fs, "exec works only for Cloud Goals")
	case *timeout < time.Second || *timeout > 80*time.Second: // as Cloud caps it, under Cloudflare's 100 s
		execUsageError(fs, "--timeout must be from 1s to 80s")
	}

	var stdin []byte
	switch *stdinPath {
	case "":
	case "-":
		stdin, err = io.ReadAll(os.Stdin)
	default:
		stdin, err = os.ReadFile(*stdinPath)
	}
	if err != nil {
		execFail(fmt.Errorf("read --stdin: %w", err))
	}
	control, err := cloud.ControlClientForContext(contextOverride)
	if err != nil {
		execFail(err)
	}
	result, err := control.Exec(goalID, argv, stdin, *timeout)
	if err != nil {
		execFail(goalNotFound(err, goalID, control.ContextName()))
	}
	if *jsonOut {
		printJSON(result)
	} else if err := writeExecStreams(result); err != nil {
		execFail(err)
	}
	if result.TimedOut {
		fmt.Fprintf(os.Stderr, "error: the command was stopped at its time limit of %s. To allow more time, use --timeout, up to 80s.\n", *timeout)
	}
	if result.StdoutTruncated || result.StderrTruncated {
		fmt.Fprintln(os.Stderr, "warning: the output was cut at 24 MiB per stream. Write the output to a file on the Goal and read it in parts.")
	}
	os.Exit(result.ExitCode)
}

// writeExecStreams writes the command's stdout and stderr exactly as the
// command wrote them.
func writeExecStreams(result *sessionapi.ExecResponse) error {
	for _, stream := range []struct {
		encoded string
		to      *os.File
	}{{result.StdoutB64, os.Stdout}, {result.StderrB64, os.Stderr}} {
		data, err := base64.StdEncoding.DecodeString(stream.encoded)
		if err != nil {
			return fmt.Errorf("decode the command's output: %w", err)
		}
		if _, err := stream.to.Write(data); err != nil {
			return fmt.Errorf("write the command's output: %w", err)
		}
	}
	return nil
}

func execFail(err error) {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(execFailed)
}

func execUsageError(fs *flag.FlagSet, message string) {
	fmt.Fprintf(fs.Output(), "error: %s\n\n", message)
	fs.Usage()
	os.Exit(2)
}

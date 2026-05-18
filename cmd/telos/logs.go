package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/telos-org/telos-go/internal/sessionapi"
)

// -- logs ---------------------------------------------------------------------

func cmdLogs(args []string) {
	fs := flag.NewFlagSet("logs", flag.ExitOnError)
	follow := fs.Bool("f", false, "Follow transcript")
	env := fs.String("env", "", "Cloud environment")
	parseFlags(fs, args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: telos logs [-f] SESSION")
		os.Exit(1)
	}
	sessionID := fs.Arg(0)

	if *follow {
		followLogs(sessionID, *env)
		return
	}

	text, err := getTranscriptFromAnywhere(sessionID, *env)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Print(text)
}

func followLogs(sessionID, envID string) {
	if err := followTranscript(sessionID, envID, os.Stdout, time.Sleep); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func followTranscript(sessionID, envID string, out io.Writer, sleep func(time.Duration)) error {
	var lastLen int
	var lastTranscriptErr error
	for {
		text, err := getTranscriptFromAnywhere(sessionID, envID)
		if err == nil && len(text) > lastLen {
			fmt.Fprint(out, text[lastLen:])
			lastLen = len(text)
		}
		if err != nil {
			if !transcriptNotReady(err) {
				return err
			}
			lastTranscriptErr = err
		} else {
			lastTranscriptErr = nil
		}
		sess, err := getSessionFromAnywhere(sessionID, envID)
		if err != nil {
			return err
		}
		if sess.Status.IsTerminal() {
			if lastLen == 0 && lastTranscriptErr != nil {
				return lastTranscriptErr
			}
			return nil
		}
		sleep(2 * time.Second)
	}
}

func transcriptNotReady(err error) bool {
	if errors.Is(err, sessionapi.ErrNotFound) {
		return true
	}
	return strings.Contains(err.Error(), "HTTP 404")
}

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/telos-org/telos/internal/sessionapi"
)

// -- logs ---------------------------------------------------------------------

func cmdLogs(args []string) {
	fs := flag.NewFlagSet("logs", flag.ExitOnError)
	follow := fs.Bool("f", false, "Follow logs")
	env := fs.String("env", "", "Cloud environment")
	raw := fs.Bool("raw", false, "Show raw transcript")
	parseFlags(fs, args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: telos logs [-f] [--raw] SESSION")
		os.Exit(1)
	}
	sessionID := fs.Arg(0)

	if *follow {
		followLogs(sessionID, *env, *raw)
		return
	}

	text, err := getTranscriptFromAnywhere(sessionID, *env)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	printLogs(os.Stdout, text, *raw)
}

func followLogs(sessionID, envID string, raw bool) {
	if err := followTranscript(sessionID, envID, os.Stdout, time.Sleep, raw); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func followTranscript(sessionID, envID string, out io.Writer, sleep func(time.Duration), raw bool) error {
	var lastLen int
	var lastProgressCount int
	var lastTranscriptErr error
	for {
		text, err := getTranscriptFromAnywhere(sessionID, envID)
		if err == nil && raw && len(text) > lastLen {
			fmt.Fprint(out, text[lastLen:])
			lastLen = len(text)
		}
		if err == nil && !raw {
			updates := progressUpdates(text)
			for i := lastProgressCount; i < len(updates); i++ {
				printProgressUpdate(out, i+1, updates[i])
			}
			lastProgressCount = len(updates)
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
			if raw && lastLen == 0 && lastTranscriptErr != nil {
				return lastTranscriptErr
			}
			if !raw && lastProgressCount == 0 {
				if lastTranscriptErr != nil {
					return lastTranscriptErr
				}
				fmt.Fprintln(out, "no progress updates")
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

func printLogs(out io.Writer, transcript string, raw bool) {
	if raw {
		fmt.Fprint(out, transcript)
		return
	}
	updates := progressUpdates(transcript)
	if len(updates) == 0 {
		fmt.Fprintln(out, "no progress updates")
		return
	}
	for i, update := range updates {
		printProgressUpdate(out, i+1, update)
	}
}

func printProgressUpdate(out io.Writer, index int, update string) {
	if index > 1 {
		fmt.Fprintln(out)
	}
	fmt.Fprintf(out, "#%d %s\n", index, update)
}

// Logs only treat standalone protocol tags as progress. The transcript parser
// is intentionally looser because it trims final status blocks.
var progressUpdateTagRE = regexp.MustCompile(`(?ims)^[ \t]*<progress_update\b[^>]*>\s*(.*?)\s*</progress_update>[ \t]*$`)

func progressUpdates(transcript string) []string {
	matches := progressUpdateTagRE.FindAllStringSubmatch(transcript, -1)
	updates := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		text := strings.TrimSpace(match[1])
		if text == "" {
			continue
		}
		updates = append(updates, text)
	}
	return updates
}

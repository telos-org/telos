package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/sessionapi"
)

// -- logs ---------------------------------------------------------------------

func cmdLogs(args []string) {
	fs := flag.NewFlagSet("logs", flag.ExitOnError)
	follow := fs.Bool("f", false, "Follow logs")
	verbose := fs.Bool("verbose", false, "Include detailed human-readable activity")
	jsonOutput := fs.Bool("json", false, "Print newline-delimited JSON events")
	raw := fs.Bool("raw", false, "Print the raw transcript or evidence events")
	tail := fs.Int("tail", defaultLogTail, "Show the most recent N activity rows")
	all := fs.Bool("all", false, "Show all activity rows")
	parseFlags(fs, args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: telos logs [-f] [--verbose|--json|--raw] [--tail N|--all] SESSION")
		os.Exit(1)
	}
	if enabledFlagCount(*verbose, *jsonOutput, *raw) > 1 {
		fmt.Fprintln(os.Stderr, "error: --verbose, --json, and --raw are mutually exclusive")
		os.Exit(1)
	}
	if *tail < 1 && !*all {
		fmt.Fprintln(os.Stderr, "error: --tail must be greater than zero")
		os.Exit(1)
	}
	sessionID := fs.Arg(0)
	options := logViewOptions{Verbose: *verbose, Tail: *tail, All: *all}

	if *follow {
		if session, err := getSessionFromAnywhere(sessionID); err == nil {
			if *raw {
				followTranscriptLogs(sessionID, true)
			} else {
				followSessionLogs(session, options, *jsonOutput)
			}
			return
		}
		if session, found, err := getCloudSessionIfConfigured(sessionID); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		} else if found {
			followCloudSessionLogs(session, options, *jsonOutput || *raw)
			return
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", localSessionNotFoundError(sessionID))
		os.Exit(1)
	}

	if session, err := getSessionFromAnywhere(sessionID); err == nil {
		if *raw {
			text, transcriptErr := getTranscriptFromAnywhere(sessionID)
			if transcriptErr != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", transcriptErr)
				os.Exit(1)
			}
			printLogs(os.Stdout, text, true)
			return
		}
		events, eventsErr := getEventsFromAnywhere(sessionID)
		if eventsErr != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", eventsErr)
			os.Exit(1)
		}
		if *jsonOutput {
			if eventsErr := printJSONLogEvents(os.Stdout, selectLogEvents(events, options)); eventsErr != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", eventsErr)
				os.Exit(1)
			}
			return
		}
		printStructuredLogs(os.Stdout, localLogHeader(session), events, options)
		return
	}

	if session, found, cloudErr := getCloudSessionIfConfigured(sessionID); cloudErr != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", cloudErr)
		os.Exit(1)
	} else if found {
		control, controlErr := cloud.ControlClient()
		if controlErr != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", controlErr)
			os.Exit(1)
		}
		events, eventsErr := control.GetSessionLogs(sessionID)
		if eventsErr != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", eventsErr)
			os.Exit(1)
		}
		if *jsonOutput || *raw {
			outputEvents := events
			if *jsonOutput {
				outputEvents = selectLogEvents(events, options)
			}
			if eventsErr := printJSONLogEvents(os.Stdout, outputEvents); eventsErr != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", eventsErr)
				os.Exit(1)
			}
			return
		}
		printStructuredLogs(os.Stdout, cloudLogHeader(session), events, options)
		return
	}

	fmt.Fprintf(os.Stderr, "error: %v\n", localSessionNotFoundError(sessionID))
	os.Exit(1)
}

func enabledFlagCount(values ...bool) int {
	count := 0
	for _, value := range values {
		if value {
			count++
		}
	}
	return count
}

func followTranscriptLogs(sessionID string, raw bool) {
	if err := followTranscript(sessionID, os.Stdout, time.Sleep, raw); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func followSessionLogs(session *sessionapi.Session, options logViewOptions, jsonOutput bool) {
	if err := pollSessionLogs(session, os.Stdout, time.Sleep, options, jsonOutput); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func followCloudSessionLogs(session *cloud.SessionRecord, options logViewOptions, jsonOutput bool) {
	control, err := cloud.ControlClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if !jsonOutput {
		printStructuredLogs(os.Stdout, cloudLogHeader(session), nil, options)
		fmt.Fprintln(os.Stdout)
	}
	if err := streamCloudSessionLogs(control, session.ID, os.Stdout, time.Sleep, options.Verbose, jsonOutput); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func streamCloudSessionLogs(
	control *cloud.Client,
	sessionID string,
	out io.Writer,
	sleep func(time.Duration),
	verbose bool,
	jsonOutput bool,
) error {
	for {
		streamErr := control.StreamSessionLogs(context.Background(), sessionID, func(event sessionapi.SessionEvent) error {
			printed, err := printStreamingLogEvent(out, event, verbose, jsonOutput)
			if err != nil {
				return err
			}
			if printed && !jsonOutput {
				_, _ = fmt.Fprintln(out)
			}
			return nil
		})
		if streamErr == nil {
			return nil
		}
		if streamErr != nil {
			if !transcriptNotReady(streamErr) {
				return streamErr
			}
		}

		session, err := control.GetSession(sessionID)
		if err != nil {
			if cloud.IsStatus(err, http.StatusNotFound) {
				// The control plane hard-deletes deployments once teardown
				// completes; a session vanishing mid-follow means it finished.
				return nil
			}
			return err
		}
		if cloudSessionStateTerminal(session.State) {
			return streamErr
		}
		sleep(2 * time.Second)
	}
}

func pollSessionLogs(
	session *sessionapi.Session,
	out io.Writer,
	sleep func(time.Duration),
	options logViewOptions,
	jsonOutput bool,
) error {
	events, err := getEventsFromAnywhere(session.SessionID)
	if err != nil {
		return err
	}
	if jsonOutput {
		if err := printJSONLogEvents(out, selectLogEvents(events, options)); err != nil {
			return err
		}
	} else {
		printStructuredLogs(out, localLogHeader(session), events, options)
	}
	seen := len(events)
	if session.Status.IsTerminal() {
		return nil
	}

	for {
		sleep(2 * time.Second)
		events, err = getEventsFromAnywhere(session.SessionID)
		if err != nil {
			return err
		}
		for _, event := range events[minimum(seen, len(events)):] {
			if _, err := printStreamingLogEvent(out, event, options.Verbose, jsonOutput); err != nil {
				return err
			}
		}
		seen = len(events)
		session, err = getSessionFromAnywhere(session.SessionID)
		if err != nil {
			return err
		}
		if session.Status.IsTerminal() {
			return nil
		}
	}
}

func printStreamingLogEvent(
	out io.Writer,
	event sessionapi.SessionEvent,
	verbose bool,
	jsonOutput bool,
) (bool, error) {
	if jsonOutput {
		return true, printJSONLogEvents(out, []sessionapi.SessionEvent{event})
	}
	row, ok := renderedLogRowFromEvent(event, verbose)
	if !ok {
		return false, nil
	}
	printRenderedLogRow(out, row)
	return true, nil
}

func minimum(left int, right int) int {
	if left < right {
		return left
	}
	return right
}

func cloudSessionStateTerminal(state string) bool {
	switch state {
	case "healthy", "failed", "deleted":
		return true
	default:
		return false
	}
}

func followTranscript(sessionID string, out io.Writer, sleep func(time.Duration), raw bool) error {
	var lastLen int
	var lastBlockCount int
	var lastProgressCount int
	var lastTranscriptErr error
	for {
		text, err := getTranscriptFromAnywhere(sessionID)
		if err == nil && raw && len(text) > lastLen {
			fmt.Fprint(out, text[lastLen:])
			lastLen = len(text)
		}
		if err == nil && !raw {
			blocks := logBlocks(text)
			if lastBlockCount < len(blocks) {
				lastProgressCount = printLogBlocks(out, blocks[lastBlockCount:], lastProgressCount)
				lastBlockCount = len(blocks)
			}
		}
		if err != nil {
			if !transcriptNotReady(err) {
				return err
			}
			lastTranscriptErr = err
		} else {
			lastTranscriptErr = nil
		}
		sess, err := getSessionFromAnywhere(sessionID)
		if err != nil {
			return err
		}
		if sess.Status.IsTerminal() {
			if raw && lastLen == 0 && lastTranscriptErr != nil {
				return lastTranscriptErr
			}
			if !raw && lastBlockCount == 0 {
				if lastTranscriptErr != nil {
					return lastTranscriptErr
				}
				fmt.Fprintln(out, "no session log entries")
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
	blocks := logBlocks(transcript)
	if len(blocks) == 0 {
		fmt.Fprintln(out, "no session log entries")
		return
	}
	printLogBlocks(out, blocks, 0)
}

func numericEventValue(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	default:
		return 0, false
	}
}

func printProgressUpdate(out io.Writer, index int, update string) {
	if index > 1 {
		fmt.Fprintln(out)
	}
	fmt.Fprintf(out, "#%d %s\n", index, update)
}

// Logs only treat standalone protocol tags as public log entries. This avoids
// turning inline examples into user-visible progress or review output.
var (
	progressUpdateTagRE = regexp.MustCompile(`(?ims)^[ \t]*<progress_update\b[^>]*>\s*(.*?)\s*</progress_update>[ \t]*$`)
	reviewTagRE         = regexp.MustCompile(`(?ims)^[ \t]*<review\b[^>]*>\s*(.*?)\s*</review>[ \t]*$`)
	summaryTagRE        = regexp.MustCompile(`(?ims)^[ \t]*<summary\b[^>]*>\s*(.*?)\s*</summary>[ \t]*$`)
	externalUpdateTagRE = regexp.MustCompile(`(?ims)^[ \t]*<external_update\b[^>]*>\s*(.*?)\s*</external_update>[ \t]*$`)
)

type logBlock struct {
	start int
	kind  string
	text  string
}

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

func logBlocks(transcript string) []logBlock {
	var blocks []logBlock
	blocks = appendLogBlocks(blocks, transcript, "progress_update", progressUpdateTagRE)
	blocks = appendLogBlocks(blocks, transcript, "review", reviewTagRE)
	blocks = appendLogBlocks(blocks, transcript, "summary", summaryTagRE)
	blocks = appendLogBlocks(blocks, transcript, "external_update", externalUpdateTagRE)
	sort.SliceStable(blocks, func(i, j int) bool {
		return blocks[i].start < blocks[j].start
	})
	return blocks
}

func appendLogBlocks(blocks []logBlock, transcript string, kind string, re *regexp.Regexp) []logBlock {
	matches := re.FindAllStringSubmatchIndex(transcript, -1)
	for _, match := range matches {
		if len(match) < 4 || match[2] < 0 || match[3] < 0 {
			continue
		}
		text := strings.TrimSpace(transcript[match[2]:match[3]])
		if text == "" {
			continue
		}
		blocks = append(blocks, logBlock{start: match[0], kind: kind, text: text})
	}
	return blocks
}

func printLogBlocks(out io.Writer, blocks []logBlock, progressCount int) int {
	printed := false
	seen := make(map[string]bool, len(blocks))
	for _, block := range blocks {
		key := block.kind + "\x00" + block.text
		if seen[key] {
			continue
		}
		seen[key] = true
		if printed {
			fmt.Fprintln(out)
		}
		switch block.kind {
		case "progress_update":
			progressCount++
			fmt.Fprintf(out, "#%d %s\n", progressCount, block.text)
		case "review":
			fmt.Fprintf(out, "Review\n%s\n", block.text)
		case "summary":
			fmt.Fprintf(out, "Summary\n%s\n", block.text)
		case "external_update":
			fmt.Fprintf(out, "External update\n%s\n", block.text)
		default:
			fmt.Fprintln(out, block.text)
		}
		printed = true
	}
	return progressCount
}

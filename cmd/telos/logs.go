package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
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
	verbose := fs.Bool("verbose", false, "Show verbose log events")
	parseFlags(fs, args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: telos logs [-f] [--verbose] SESSION")
		os.Exit(1)
	}
	sessionID := fs.Arg(0)

	if *follow {
		if !localSessionExists(sessionID) {
			if _, found, err := getCloudSessionIfConfigured(sessionID); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			} else if found {
				followCloudSessionLogs(sessionID, *verbose)
				return
			}
		}
		followLogs(sessionID, *verbose)
		return
	}

	text, err := getTranscriptFromAnywhere(sessionID)
	if err == nil {
		printLogs(os.Stdout, text, *verbose)
		return
	}

	if _, found, cloudErr := getCloudSessionIfConfigured(sessionID); cloudErr != nil {
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
		printCloudSessionLogEvents(os.Stdout, events, *verbose)
		return
	}

	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}

func followLogs(sessionID string, verbose bool) {
	if err := followTranscript(sessionID, os.Stdout, time.Sleep, verbose); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func followCloudSessionLogs(sessionID string, verbose bool) {
	control, err := cloud.ControlClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if err := streamCloudSessionLogs(control, sessionID, os.Stdout, time.Sleep, verbose); err != nil {
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
) error {
	var lastProgressCount int
	for {
		streamErr := control.StreamSessionLogs(context.Background(), sessionID, func(event sessionapi.SessionEvent) error {
			printed := printCloudSessionLogEvent(out, event, verbose, &lastProgressCount)
			if printed {
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
			return err
		}
		if cloudSessionStateTerminal(session.State) {
			return streamErr
		}
		sleep(2 * time.Second)
	}
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

func printCloudSessionLogEvents(out io.Writer, events []sessionapi.SessionEvent, verbose bool) {
	progressCount := 0
	printed := false
	for _, event := range events {
		if printed {
			fmt.Fprintln(out)
		}
		if printCloudSessionLogEvent(out, event, verbose, &progressCount) {
			printed = true
		}
	}
	if !printed {
		fmt.Fprintln(out, "no session log entries")
	}
}

func printCloudSessionLogEvent(out io.Writer, event sessionapi.SessionEvent, verbose bool, progressCount *int) bool {
	if verbose {
		data, err := json.Marshal(event)
		if err != nil {
			return false
		}
		fmt.Fprintln(out, string(data))
		return true
	}
	switch event.Event {
	case "agent_progress":
		kind, _ := event.Data["kind"].(string)
		text, _ := event.Data["text"].(string)
		if strings.TrimSpace(text) == "" {
			return false
		}
		block := logBlock{kind: kind, text: strings.TrimSpace(text)}
		*progressCount = printLogBlocks(out, []logBlock{block}, *progressCount)
		return true
	case "game_end":
		if result, _ := event.Data["game_result"].(string); result != "" {
			fmt.Fprintf(out, "Completed: %s\n", result)
			return true
		}
		if reason, _ := event.Data["completion_reason"].(string); reason != "" {
			fmt.Fprintf(out, "Completed: %s\n", reason)
			return true
		}
	}
	if message, _ := event.Data["message"].(string); strings.TrimSpace(message) != "" {
		fmt.Fprintln(out, strings.TrimSpace(message))
		return true
	}
	return false
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

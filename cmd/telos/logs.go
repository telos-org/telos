package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/sessionapi"
)

// -- logs ---------------------------------------------------------------------

func cmdLogs(args []string) {
	fs := newCommandFlagSet("logs", "telos logs SESSION [flags]")
	jsonOutput := fs.Bool("json", false, "Print newline-delimited JSON events")
	raw := fs.Bool("raw", false, "Print the raw transcript or evidence events")
	tail := fs.Int("tail", defaultLogTail, "Show the most recent N activity rows")
	all := fs.Bool("all", false, "Show all activity rows")
	contextValue := cloudContextFlag(fs)
	parseFlags(fs, args)
	contextOverride, err := cloudContextOverride(fs, *contextValue)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}

	requireArgCount(fs, 1, "one SESSION")
	if enabledFlagCount(*jsonOutput, *raw) > 1 {
		fmt.Fprintln(os.Stderr, "error: --json and --raw are mutually exclusive")
		os.Exit(2)
	}
	if *all && flagNameSet(fs, "tail") {
		fmt.Fprintln(os.Stderr, "error: --all and --tail are mutually exclusive")
		os.Exit(2)
	}
	if *raw && (*all || flagNameSet(fs, "tail")) {
		fmt.Fprintln(os.Stderr, "error: --raw cannot be combined with --all or --tail")
		os.Exit(2)
	}
	if *tail < 1 && !*all {
		fmt.Fprintln(os.Stderr, "error: --tail must be greater than zero")
		os.Exit(2)
	}
	sessionID := fs.Arg(0)
	if err := validateCloudSessionContext(sessionID, contextOverride); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	options := logViewOptions{Tail: *tail, All: *all}
	if contextOverride != "" {
		session, err := getCloudSession(sessionID, contextOverride)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		printCloudSessionLogs(session, options, *jsonOutput, *raw, contextOverride)
		return
	}

	if _, err := getSessionFromAnywhere(sessionID); err == nil {
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
		if !*jsonOutput {
			if transcript, ok := legacyTranscriptFallback(sessionID, events, eventsErr); ok {
				printLogs(os.Stdout, transcript, false)
				return
			}
		}
		if eventsErr != nil {
			if *jsonOutput && transcriptNotReady(eventsErr) {
				fmt.Fprintln(
					os.Stderr,
					"error: structured events are unavailable for this older session; omit --json for readable logs or use --raw for the transcript",
				)
				os.Exit(1)
			}
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
		printStructuredLogs(os.Stdout, events, options)
		return
	}

	if session, _, found, cloudErr := getCloudSessionIfConfigured(sessionID, ""); cloudErr != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", cloudErr)
		os.Exit(1)
	} else if found {
		printCloudSessionLogs(session, options, *jsonOutput, *raw, "")
		return
	}

	fmt.Fprintf(os.Stderr, "error: %v\n", localSessionNotFoundError(sessionID))
	os.Exit(1)
}

func printCloudSessionLogs(
	session *cloud.SessionRecord,
	options logViewOptions,
	jsonOutput bool,
	raw bool,
	contextOverride string,
) {
	control, err := cloud.ControlClientForContext(contextOverride)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	page, err := control.GetSessionLogPage(session.ID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if raw {
		if err := printRawJSONLogEvents(os.Stdout, page.RawEvents); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if jsonOutput {
		if err := printJSONLogEventsForContext(
			os.Stdout,
			selectLogEvents(page.Events, options),
			control.ContextName(),
		); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}
	printStructuredLogs(os.Stdout, page.Events, options)
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

func printRawJSONLogEvents(out io.Writer, events []json.RawMessage) error {
	for _, event := range events {
		if !json.Valid(event) {
			return errors.New("raw session log event is invalid JSON")
		}
		if _, err := out.Write(event); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(out); err != nil {
			return err
		}
	}
	return nil
}

func legacyTranscriptFallback(
	sessionID string,
	events []sessionapi.SessionEvent,
	eventsErr error,
) (string, bool) {
	if eventsErr == nil && len(events) > 0 {
		return "", false
	}
	transcript, err := getTranscriptFromAnywhere(sessionID)
	if err != nil || len(logBlocks(transcript)) == 0 {
		return "", false
	}
	return transcript, true
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
	for _, block := range blocks {
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

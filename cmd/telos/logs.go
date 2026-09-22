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
	"time"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/sessionapi"
)

// -- logs ---------------------------------------------------------------------

const maxCloudLogTail = 1000

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

	if session, err := getSessionFromAnywhere(sessionID); err == nil {
		options.Active = session.Status == sessionapi.StatusRunning
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
	status := cloudSessionDisplayStatus(*session)
	options.Active = status == "working" || status == "in_progress" || status == "running"
	control, err := cloud.ControlClientForContext(contextOverride)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	tail := options.Tail
	if raw || options.All || tail > maxCloudLogTail {
		tail = 0
	}
	page, err := control.GetSessionLogPage(session.ID, tail)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	// A raw event window can consist entirely of hidden tool activity. Page
	// only the human view; raw and JSON retain their event-tail contract.
	if !raw && !jsonOutput && tail > 0 {
		page, err = expandCloudHumanLogs(control, session.ID, page, options.Tail)
		if err != nil {
			options.Active = false
			fmt.Fprintf(os.Stderr, "Some progress updates could not be loaded: %v\n", err)
		}
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

func expandCloudHumanLogs(control *cloud.Client, sessionID string, initial *cloud.SessionLogPage, tail int) (*cloud.SessionLogPage, error) {
	combined := *initial
	page := initial
	var beforeRuntime, beforeControl *int64
	if initial.Cursors != nil {
		beforeRuntime = afterCloudLogHead(initial.Cursors.Runtime)
		beforeControl = afterCloudLogHead(initial.Cursors.Control)
	}
	seen := make(map[string]bool)
	fullHistory := false
	for {
		if page.Cursors != nil && page.Cursors.SessionKnown && page.Cursors.Session == nil {
			return &combined, errors.New("the runtime could not be reached; earlier activity may still be available")
		}
		if cloudLogSessionChanged(initial, page) {
			return &combined, errors.New("the session changed while loading earlier activity; run telos logs again to view the new session")
		}
		if fullHistory {
			return page, nil
		}
		keys, runtime, controlPlane := cloudLogPagePositions(page.RawEvents)
		if page != initial {
			if len(page.RawEvents) == 0 {
				return &combined, nil
			}
			advanced := runtime != nil && (beforeRuntime == nil || *runtime < *beforeRuntime) ||
				controlPlane != nil && (beforeControl == nil || *controlPlane < *beforeControl)
			if page.Cursors != nil && keys != nil && !advanced {
				return &combined, errors.New("the server did not return an older activity page")
			}
		}
		if page == initial || page.Cursors != nil && keys != nil {
			var events []sessionapi.SessionEvent
			var rawEvents []json.RawMessage
			for index, event := range page.Events {
				if keys != nil && seen[keys[index]] {
					continue
				}
				// Hidden tool activity need not accumulate while searching history.
				// Turn endings still matter to the quiet notice.
				_, visible := renderedLogRowFromEvent(event)
				if !visible && event.Event != "agent_complete" && event.Event != "agent_suspended" && event.Event != "game_end" {
					continue
				}
				if keys != nil {
					seen[keys[index]] = true
				}
				events = append(events, event)
				rawEvents = append(rawEvents, page.RawEvents[index])
			}
			if page == initial {
				combined.Events = nil
				combined.RawEvents = nil
			}
			combined.Events = append(events, combined.Events...)
			combined.RawEvents = append(rawEvents, combined.RawEvents...)
			sortCloudLogPage(&combined)
			if len(renderLogRows(combined.Events)) >= tail {
				return &combined, nil
			}
		}

		if runtime != nil && (beforeRuntime == nil || *runtime < *beforeRuntime) {
			beforeRuntime = runtime
		}
		if controlPlane != nil && (beforeControl == nil || *controlPlane < *beforeControl) {
			beforeControl = controlPlane
		}
		var err error
		if page.Cursors == nil || keys == nil {
			if page == initial && len(page.RawEvents) < tail {
				return &combined, nil
			}
			// Read legacy history once when durable cursors are unavailable.
			page, err = control.GetSessionLogPage(sessionID, 0)
			fullHistory = true
		} else {
			if beforeRuntime == nil && beforeControl == nil {
				return &combined, nil
			}
			page, err = control.GetSessionLogPageBefore(sessionID, maxCloudLogTail, beforeRuntime, beforeControl)
		}
		if err != nil {
			return &combined, err
		}
	}
}

func cloudLogSessionChanged(initial, next *cloud.SessionLogPage) bool {
	return initial.Cursors != nil && initial.Cursors.Session != nil && next.Cursors != nil && next.Cursors.Session != nil && *initial.Cursors.Session != *next.Cursors.Session
}

func sortCloudLogPage(page *cloud.SessionLogPage) {
	type timedEvent struct {
		event sessionapi.SessionEvent
		raw   json.RawMessage
		time  time.Time
	}
	ordered := make([]timedEvent, len(page.Events))
	for index, event := range page.Events {
		timestamp, _ := time.Parse(time.RFC3339Nano, eventTimestamp(event))
		ordered[index] = timedEvent{event: event, raw: page.RawEvents[index], time: timestamp}
	}
	sort.SliceStable(ordered, func(left, right int) bool {
		return ordered[left].time.Before(ordered[right].time)
	})
	for index, event := range ordered {
		page.Events[index], page.RawEvents[index] = event.event, event.raw
	}
}

func afterCloudLogHead(head *int64) *int64 {
	if head == nil {
		return nil
	}
	before := *head + 1
	if before <= 0 {
		return nil
	}
	return &before
}

func cloudLogPagePositions(events []json.RawMessage) (keys []string, runtime, control *int64) {
	keys = make([]string, 0, len(events))
	for _, raw := range events {
		var event struct {
			Runtime *int64 `json:"event_seq"`
			Control *int64 `json:"seq"`
		}
		if json.Unmarshal(raw, &event) != nil {
			return nil, nil, nil
		}
		switch {
		case event.Runtime != nil && *event.Runtime > 0:
			keys = append(keys, fmt.Sprintf("rt:%d", *event.Runtime))
			if runtime == nil || *event.Runtime < *runtime {
				runtime = event.Runtime
			}
		case event.Control != nil && *event.Control > 0:
			keys = append(keys, fmt.Sprintf("cp:%d", *event.Control))
			if control == nil || *event.Control < *control {
				control = event.Control
			}
		default:
			return nil, nil, nil
		}
	}
	return keys, runtime, control
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

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
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
	contextValue := cloudContextFlag(fs)
	parseFlags(fs, args)
	contextOverride, err := cloudContextOverride(fs, *contextValue)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: telos logs [-f] [--verbose|--json|--raw] [--tail N|--all] [--context CONTEXT] SESSION")
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
	if contextOverride != "" {
		session, err := getCloudSession(sessionID, contextOverride)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if *follow {
			if *raw {
				followCloudRawSessionLogs(session, contextOverride)
			} else {
				followCloudSessionLogs(session, options, *jsonOutput, contextOverride)
			}
			return
		}
		printCloudSessionLogs(session, options, *jsonOutput, *raw, contextOverride)
		return
	}

	if *follow {
		if session, err := getSessionFromAnywhere(sessionID); err == nil {
			if *raw {
				followTranscriptLogs(sessionID, true)
			} else {
				followSessionLogs(session, options, *jsonOutput)
			}
			return
		}
		if session, _, found, err := getCloudSessionIfConfigured(sessionID, ""); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		} else if found {
			if *raw {
				followCloudRawSessionLogs(session, "")
			} else {
				followCloudSessionLogs(session, options, *jsonOutput, "")
			}
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
		printStructuredLogs(os.Stdout, localLogHeader(session), events, options)
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
			resolvedCloudContext(control),
		); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}
	header := cloudLogHeader(session)
	header.Context = resolvedCloudContext(control)
	printStructuredLogs(os.Stdout, header, page.Events, options)
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
	if !jsonOutput {
		events, eventsErr := getEventsFromAnywhere(session.SessionID)
		if _, ok := legacyTranscriptFallback(session.SessionID, events, eventsErr); ok {
			if err := followTranscript(session.SessionID, os.Stdout, time.Sleep, false); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			return
		}
	}
	if err := pollSessionLogs(session, os.Stdout, time.Sleep, options, jsonOutput); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func followCloudSessionLogs(
	session *cloud.SessionRecord,
	options logViewOptions,
	jsonOutput bool,
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
	if jsonOutput {
		if err := printJSONLogEventsForContext(
			os.Stdout,
			selectLogEvents(page.Events, options),
			resolvedCloudContext(control),
		); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	} else {
		header := cloudLogHeader(session)
		header.Context = resolvedCloudContext(control)
		printStructuredLogs(os.Stdout, header, page.Events, options)
	}
	header := cloudLogHeader(session)
	header.Context = resolvedCloudContext(control)
	if err := streamCloudSessionLogsAfter(
		control,
		session.ID,
		os.Stdout,
		time.Sleep,
		options.Verbose,
		jsonOutput,
		page.RuntimeCursor,
		header,
		page.Events,
	); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func followCloudRawSessionLogs(session *cloud.SessionRecord, contextOverride string) {
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
	if err := printRawJSONLogEvents(os.Stdout, page.RawEvents); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if err := streamCloudRawSessionLogs(
		control,
		session.ID,
		os.Stdout,
		time.Sleep,
		page.RuntimeCursor,
		page.RawEvents,
	); err != nil {
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
	return streamCloudSessionLogsAfter(
		control,
		sessionID,
		out,
		sleep,
		verbose,
		jsonOutput,
		nil,
		logHeader{},
		nil,
	)
}

func streamCloudSessionLogsAfter(
	control *cloud.Client,
	sessionID string,
	out io.Writer,
	sleep func(time.Duration),
	verbose bool,
	jsonOutput bool,
	afterRuntime *int64,
	header logHeader,
	initialEvents []sessionapi.SessionEvent,
) error {
	events := append([]sessionapi.SessionEvent(nil), initialEvents...)
	currentStatus := deriveOverallLogStatus(header, events)
	runtimeCursor := copyLogCursor(afterRuntime)
	for {
		var replayCounts map[string]int
		if runtimeCursor == nil {
			replayCounts = sessionEventReplayCounts(events)
		}
		streamErr := control.StreamSessionLogsAfter(context.Background(), sessionID, runtimeCursor, func(event sessionapi.SessionEvent) error {
			if consumeSessionEventReplay(replayCounts, event) {
				return nil
			}
			_, err := printStreamingLogEvent(
				out,
				event,
				verbose,
				jsonOutput,
				resolvedCloudContext(control),
			)
			if err != nil {
				return err
			}
			events = append(events, event)
			runtimeCursor = advanceLogCursor(runtimeCursor, event.EventSeq)
			if !jsonOutput && header.SessionID != "" {
				nextStatus := deriveOverallLogStatus(header, events)
				if nextStatus.Label != currentStatus.Label {
					printStatusTransition(out, eventTimestamp(event), nextStatus)
				}
				currentStatus = nextStatus
			}
			return nil
		})
		if !retryableCloudLogStreamError(streamErr) {
			return streamErr
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
		header = cloudLogHeader(session)
		header.Context = resolvedCloudContext(control)
		if !jsonOutput && header.SessionID != "" {
			nextStatus := deriveOverallLogStatus(header, events)
			if nextStatus.Label != currentStatus.Label {
				printStatusTransition(out, header.UpdatedAt, nextStatus)
			}
			currentStatus = nextStatus
		}
		if cloudSessionStateTerminal(session.State) {
			return nil
		}
		sleep(2 * time.Second)
	}
}

func streamCloudRawSessionLogs(
	control *cloud.Client,
	sessionID string,
	out io.Writer,
	sleep func(time.Duration),
	afterRuntime *int64,
	initialEvents []json.RawMessage,
) error {
	events := append([]json.RawMessage(nil), initialEvents...)
	runtimeCursor := copyLogCursor(afterRuntime)
	for {
		var replayCounts map[string]int
		if runtimeCursor == nil {
			replayCounts = rawEventReplayCounts(events)
		}
		streamErr := control.StreamRawSessionLogsAfter(
			context.Background(),
			sessionID,
			runtimeCursor,
			func(event json.RawMessage) error {
				if consumeRawEventReplay(replayCounts, event) {
					return nil
				}
				if err := printRawJSONLogEvents(out, []json.RawMessage{event}); err != nil {
					return err
				}
				events = append(events, append(json.RawMessage(nil), event...))
				runtimeCursor = advanceLogCursor(runtimeCursor, rawLogEventSequence(event))
				return nil
			},
		)
		if !retryableCloudLogStreamError(streamErr) {
			return streamErr
		}

		session, err := control.GetSession(sessionID)
		if err != nil {
			if cloud.IsStatus(err, http.StatusNotFound) {
				return nil
			}
			return err
		}
		if cloudSessionStateTerminal(session.State) {
			return nil
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
	currentStatus := deriveOverallLogStatus(localLogHeader(session), events)
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
			if _, err := printStreamingLogEvent(out, event, options.Verbose, jsonOutput, ""); err != nil {
				return err
			}
		}
		seen = len(events)
		session, err = getSessionFromAnywhere(session.SessionID)
		if err != nil {
			return err
		}
		if !jsonOutput {
			nextStatus := deriveOverallLogStatus(localLogHeader(session), events)
			if nextStatus.Label != currentStatus.Label {
				timestamp := ""
				if len(events) > 0 {
					timestamp = eventTimestamp(events[len(events)-1])
				}
				printStatusTransition(out, timestamp, nextStatus)
			}
			currentStatus = nextStatus
		}
		if session.Status.IsTerminal() {
			return nil
		}
	}
}

func printStatusTransition(out io.Writer, timestamp string, status overallLogStatus) {
	printRenderedLogRow(out, renderedLogRow{
		Timestamp: timestamp,
		Phase:     "STATUS",
		Summary:   status.Label,
		Detail:    status.Reason,
	})
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

func sessionEventReplayCounts(events []sessionapi.SessionEvent) map[string]int {
	counts := make(map[string]int, len(events))
	for _, event := range events {
		key, ok := sessionEventReplayKey(event)
		if ok {
			counts[key]++
		}
	}
	return counts
}

func consumeSessionEventReplay(counts map[string]int, event sessionapi.SessionEvent) bool {
	if len(counts) == 0 {
		return false
	}
	key, ok := sessionEventReplayKey(event)
	if !ok || counts[key] == 0 {
		return false
	}
	if counts[key] == 1 {
		delete(counts, key)
	} else {
		counts[key]--
	}
	return true
}

func sessionEventReplayKey(event sessionapi.SessionEvent) (string, bool) {
	data, err := json.Marshal(event)
	return string(data), err == nil
}

func rawEventReplayCounts(events []json.RawMessage) map[string]int {
	counts := make(map[string]int, len(events))
	for _, event := range events {
		key, ok := rawEventReplayKey(event)
		if ok {
			counts[key]++
		}
	}
	return counts
}

func consumeRawEventReplay(counts map[string]int, event json.RawMessage) bool {
	if len(counts) == 0 {
		return false
	}
	key, ok := rawEventReplayKey(event)
	if !ok || counts[key] == 0 {
		return false
	}
	if counts[key] == 1 {
		delete(counts, key)
	} else {
		counts[key]--
	}
	return true
}

func rawEventReplayKey(event json.RawMessage) (string, bool) {
	var value any
	if err := json.Unmarshal(event, &value); err != nil {
		return "", false
	}
	data, err := json.Marshal(value)
	return string(data), err == nil
}

func rawLogEventSequence(event json.RawMessage) *int64 {
	var envelope struct {
		EventSeq *int64 `json:"event_seq"`
	}
	if json.Unmarshal(event, &envelope) != nil {
		return nil
	}
	return envelope.EventSeq
}

func copyLogCursor(cursor *int64) *int64 {
	if cursor == nil {
		return nil
	}
	value := *cursor
	return &value
}

func advanceLogCursor(current *int64, candidate *int64) *int64 {
	if candidate == nil || (current != nil && *candidate <= *current) {
		return current
	}
	value := *candidate
	return &value
}

func retryableCloudLogStreamError(err error) bool {
	if err == nil || transcriptNotReady(err) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var apiErr *cloud.APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusRequestTimeout ||
			apiErr.StatusCode == http.StatusTooManyRequests ||
			apiErr.StatusCode >= http.StatusInternalServerError
	}
	return false
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

func printStreamingLogEvent(
	out io.Writer,
	event sessionapi.SessionEvent,
	verbose bool,
	jsonOutput bool,
	contextName string,
) (bool, error) {
	if jsonOutput {
		return true, printJSONLogEventsForContext(
			out,
			[]sessionapi.SessionEvent{event},
			contextName,
		)
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
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "deleting", "failed", "deleted", "stopped":
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

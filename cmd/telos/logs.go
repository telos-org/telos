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

	"github.com/telos-org/telos/internal/agentsession"
	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/config"
	"github.com/telos-org/telos/internal/sessionapi"
)

// -- logs ---------------------------------------------------------------------

func cmdLogs(args []string) {
	fs := flag.NewFlagSet("logs", flag.ExitOnError)
	follow := fs.Bool("f", false, "Follow logs")
	env := fs.String("env", "", "Cloud environment")
	orgID := fs.String("org", "", "Organization ID")
	raw := fs.Bool("raw", false, "Show raw transcript")
	jsonOut := fs.Bool("json", false, "Emit protocol JSONL")
	poll := fs.Bool("poll", false, "Follow by polling the transcript instead of using the protocol stream")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: telos logs [-f] [--raw|--json] [--poll] SESSION")
		fmt.Fprintln(fs.Output(), "")
		fmt.Fprintln(fs.Output(), "terminal exit codes for -f: completed=0, incomplete=1, exhausted=2, policy_blocked=3, provider_failed=4, tool_failed=5, interrupted=130")
		fs.PrintDefaults()
	}
	parseFlags(fs, args)

	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(1)
	}
	sessionID := fs.Arg(0)

	if *follow {
		followLogs(sessionID, *env, logFollowOptions{rawTranscript: *raw, json: *jsonOut, poll: *poll})
		return
	}
	if *jsonOut {
		if err := printProtocolJSONL(sessionID, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	text, err := getTranscriptFromAnywhere(sessionID, *env)
	if err != nil {
		if *env == "" && config.IsConfigured() {
			text, deploymentErr := getDeploymentTranscriptFromCloud(sessionID, *orgID)
			if deploymentErr == nil {
				printLogs(os.Stdout, text, *raw)
				return
			}
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	printLogs(os.Stdout, text, *raw)
}

func getDeploymentTranscriptFromCloud(id string, orgID string) (string, error) {
	client, err := cloud.NewControlClientFromConfig()
	if err != nil {
		return "", err
	}
	applyOrgOverride(client, orgID)
	return client.GetDeploymentTranscript(id)
}

type logFollowOptions struct {
	rawTranscript bool
	json          bool
	poll          bool
}

func followLogs(sessionID, envID string, opts logFollowOptions) {
	exitCode, err := followProtocolLogs(context.Background(), sessionID, envID, os.Stdout, opts)
	if opts.poll || err != nil {
		fallbackErr := followTranscript(sessionID, envID, os.Stdout, time.Sleep, opts.rawTranscript)
		if fallbackErr == nil {
			os.Exit(0)
		}
		if err != nil && !opts.poll {
			fmt.Fprintf(os.Stderr, "warning: protocol stream unavailable, polling fallback failed: %v\n", fallbackErr)
		}
		err = fallbackErr
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}

func printProtocolJSONL(sessionID string, out io.Writer) error {
	events, err := store().ProtocolEvents(sessionID, sessionapi.ProtocolEventsOptions{})
	if err != nil {
		return err
	}
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)
	for _, event := range events {
		if err := enc.Encode(event); err != nil {
			return err
		}
	}
	return nil
}

func followProtocolLogs(ctx context.Context, sessionID, envID string, out io.Writer, opts logFollowOptions) (int, error) {
	if opts.poll {
		return 0, fmt.Errorf("protocol stream disabled")
	}
	if localSessionExists(sessionID) {
		return followLocalProtocolEvents(sessionID, out, time.Sleep, opts.json)
	}
	if root, ok := rootSessionContext(); ok {
		return streamProtocolClient(ctx, cloud.NewClient(root.endpoint, root.token), sessionID, out, opts.json)
	}
	if envID != "" || config.IsConfigured() {
		clients, err := cloudSessionClients(envID)
		if err != nil && envID != "" {
			return 0, err
		}
		var streamErr error = err
		for _, client := range clients {
			code, err := streamProtocolClient(ctx, client, sessionID, out, opts.json)
			if err == nil {
				return code, nil
			}
			streamErr = errors.Join(streamErr, err)
		}
		if streamErr != nil {
			return 0, streamErr
		}
	}
	return 0, localSessionNotFoundError(sessionID)
}

func followLocalProtocolEvents(sessionID string, out io.Writer, sleep func(time.Duration), jsonMode bool) (int, error) {
	var last int64
	for {
		events, err := store().ProtocolEvents(sessionID, sessionapi.ProtocolEventsOptions{SinceSequence: last})
		if err != nil {
			return 0, err
		}
		if code, terminal, err := renderProtocolEvents(out, events, jsonMode, &last); err != nil || terminal {
			return code, err
		}
		sleep(time.Second)
	}
}

func streamProtocolClient(ctx context.Context, client *cloud.Client, sessionID string, out io.Writer, jsonMode bool) (int, error) {
	var last int64
	var exitCode int
	var terminal bool
	err := client.StreamEvents(ctx, sessionID, func(raw map[string]any) error {
		data, err := json.Marshal(raw)
		if err != nil {
			return err
		}
		var event agentsession.Event
		if err := json.Unmarshal(data, &event); err != nil {
			return err
		}
		code, done, err := renderProtocolEvents(out, []agentsession.Event{event}, jsonMode, &last)
		if done {
			exitCode = code
			terminal = true
		}
		return err
	})
	if err != nil {
		return 0, err
	}
	if terminal {
		return exitCode, nil
	}
	return 0, nil
}

func renderProtocolEvents(out io.Writer, events []agentsession.Event, jsonMode bool, last *int64) (int, bool, error) {
	for _, event := range events {
		if event.Sequence > 0 && last != nil {
			*last = event.Sequence
		}
		if jsonMode {
			data, err := json.Marshal(event)
			if err != nil {
				return 0, false, err
			}
			fmt.Fprintln(out, string(data))
		} else {
			renderProtocolEvent(out, event)
		}
		if event.Type == agentsession.KindTerminal {
			payload, err := agentsession.Unmarshal[agentsession.TerminalPayload](&event)
			if err != nil {
				return 1, true, nil
			}
			return terminalExitCode(payload.TerminalState), true, nil
		}
	}
	return 0, false, nil
}

func renderProtocolEvent(out io.Writer, event agentsession.Event) {
	switch event.Type {
	case agentsession.KindAssistantText:
		if payload, err := agentsession.Unmarshal[agentsession.AssistantTextPayload](&event); err == nil && strings.TrimSpace(payload.Text) != "" {
			fmt.Fprintln(out, strings.TrimRight(payload.Text, "\n"))
		}
	case agentsession.KindToolCallStart:
		if payload, err := agentsession.Unmarshal[agentsession.ToolCallStartPayload](&event); err == nil {
			fmt.Fprintf(out, "Tool %s started\n", payload.ToolName)
		}
	case agentsession.KindToolCallResult:
		if payload, err := agentsession.Unmarshal[agentsession.ToolCallResultStreamPayload](&event); err == nil {
			status := "ok"
			if payload.IsError {
				status = "error"
			}
			fmt.Fprintf(out, "Tool %s %s", payload.ToolName, status)
			if len(payload.ChangedFiles) > 0 {
				fmt.Fprintf(out, " changed=%s", strings.Join(payload.ChangedFiles, ","))
			}
			fmt.Fprintln(out)
			if strings.TrimSpace(payload.Preview) != "" {
				fmt.Fprintln(out, strings.TrimRight(payload.Preview, "\n"))
			}
		}
	case agentsession.KindUsage:
		if payload, err := agentsession.Unmarshal[agentsession.UsagePayload](&event); err == nil {
			fmt.Fprintf(out, "Usage input=%d output=%d cache_read=%d cache_write=%d", payload.Input, payload.Output, payload.CacheRead, payload.CacheWrite)
			if !payload.CostUnavailable && payload.CostUSD > 0 {
				fmt.Fprintf(out, " cost=$%.4f", payload.CostUSD)
			}
			fmt.Fprintln(out)
		}
	case agentsession.KindCompaction:
		fmt.Fprintln(out, "Compaction completed")
	case agentsession.KindLifecycle:
		if payload, err := agentsession.Unmarshal[agentsession.LifecyclePayload](&event); err == nil {
			fmt.Fprintf(out, "Lifecycle %s\n", payload.State)
		}
	case agentsession.KindTerminal:
		if payload, err := agentsession.Unmarshal[agentsession.TerminalPayload](&event); err == nil {
			fmt.Fprintf(out, "Terminal %s\n", payload.TerminalState)
		}
	}
}

func terminalExitCode(state agentsession.TerminalState) int {
	switch state {
	case "", agentsession.TerminalCompleted:
		return 0
	case agentsession.TerminalIncomplete:
		return 1
	case agentsession.TerminalExhausted:
		return 2
	case agentsession.TerminalPolicyBlocked:
		return 3
	case agentsession.TerminalProviderFailed:
		return 4
	case agentsession.TerminalToolFailed:
		return 5
	case agentsession.TerminalInterrupted:
		return 130
	default:
		return 1
	}
}

func followTranscript(sessionID, envID string, out io.Writer, sleep func(time.Duration), raw bool) error {
	var lastLen int
	var lastBlockCount int
	var lastProgressCount int
	var lastTranscriptErr error
	for {
		text, err := getTranscriptFromAnywhere(sessionID, envID)
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
		sess, err := getSessionFromAnywhere(sessionID, envID)
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

// Logs only treat standalone protocol tags as public log entries. This avoids
// turning inline examples into user-visible progress or review output.
var (
	progressUpdateTagRE = regexp.MustCompile(`(?ims)^[ \t]*<progress_update\b[^>]*>\s*(.*?)\s*</progress_update>[ \t]*$`)
	reviewTagRE         = regexp.MustCompile(`(?ims)^[ \t]*<review\b[^>]*>\s*(.*?)\s*</review>[ \t]*$`)
	summaryTagRE        = regexp.MustCompile(`(?ims)^[ \t]*<summary\b[^>]*>\s*(.*?)\s*</summary>[ \t]*$`)
)

type logBlock struct {
	start int
	kind  string
	text  string
}

func logBlocks(transcript string) []logBlock {
	var blocks []logBlock
	blocks = appendLogBlocks(blocks, transcript, "progress_update", progressUpdateTagRE)
	blocks = appendLogBlocks(blocks, transcript, "review", reviewTagRE)
	blocks = appendLogBlocks(blocks, transcript, "summary", summaryTagRE)
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
		default:
			fmt.Fprintln(out, block.text)
		}
		printed = true
	}
	return progressCount
}

package main

import (
	"encoding/json"
	"io"
	"time"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/sessionapi"
)

// logFollowInterval is how often telos logs --follow checks for new activity.
var logFollowInterval = 2 * time.Second

// logFollowPrinter prints activity that arrives after the first view, in the
// same format, without repeating the last row it printed.
type logFollowPrinter struct {
	out         io.Writer
	jsonOutput  bool
	contextName string
	last        *renderedLogRow
}

func newLogFollowPrinter(
	out io.Writer,
	jsonOutput bool,
	contextName string,
	printed []sessionapi.SessionEvent,
) *logFollowPrinter {
	printer := &logFollowPrinter{out: out, jsonOutput: jsonOutput, contextName: contextName}
	if rows := renderLogRows(printed); len(rows) > 0 {
		printer.last = &rows[len(rows)-1]
	}
	return printer
}

func (p *logFollowPrinter) print(events []sessionapi.SessionEvent) error {
	if p.jsonOutput {
		return printJSONLogEventsForContext(p.out, events, p.contextName)
	}
	for _, row := range renderLogRows(events) {
		if p.last != nil && sameLogMessage(*p.last, row) {
			continue
		}
		printRenderedLogRow(p.out, row)
		p.last = &row
	}
	return nil
}

// cloudLogFollower tracks the newest event already printed from each Cloud
// log stream: the Goal's runtime numbers its events with event_seq, and Cloud
// numbers its own with seq.
type cloudLogFollower struct {
	session string
	runtime int64
	control int64
	unkeyed map[string]bool
}

// newCloudLogFollower starts after the events in page, which the first view
// already printed.
func newCloudLogFollower(page *cloud.SessionLogPage) *cloudLogFollower {
	follower := &cloudLogFollower{unkeyed: map[string]bool{}}
	follower.fresh(page)
	return follower
}

// fresh returns the events in page that have not been printed yet.
func (f *cloudLogFollower) fresh(page *cloud.SessionLogPage) []sessionapi.SessionEvent {
	if page.Cursors != nil && page.Cursors.Session != nil && *page.Cursors.Session != f.session {
		// A restarted runtime numbers its events from the start again.
		f.session = *page.Cursors.Session
		f.runtime = 0
	}
	runtime, control := f.runtime, f.control
	var events []sessionapi.SessionEvent
	for index, raw := range page.RawEvents {
		var position struct {
			Runtime *int64 `json:"event_seq"`
			Control *int64 `json:"seq"`
		}
		_ = json.Unmarshal(raw, &position)
		switch {
		case position.Runtime != nil && *position.Runtime > 0:
			if *position.Runtime <= runtime {
				continue
			}
			f.runtime = max(f.runtime, *position.Runtime)
		case position.Control != nil && *position.Control > 0:
			if *position.Control <= control {
				continue
			}
			f.control = max(f.control, *position.Control)
		default:
			if f.unkeyed[string(raw)] {
				continue
			}
			f.unkeyed[string(raw)] = true
		}
		events = append(events, page.Events[index])
	}
	sortCloudLogEvents(events)
	return events
}

// followCloudLogs prints new activity until the Goal stops or is deleted.
func followCloudLogs(
	control *cloud.Client,
	goalID string,
	follower *cloudLogFollower,
	printer *logFollowPrinter,
) error {
	for {
		time.Sleep(logFollowInterval)
		page, err := control.GetSessionLogPage(goalID, maxCloudLogTail)
		if err != nil {
			return err
		}
		if err := printer.print(follower.fresh(page)); err != nil {
			return err
		}
		goal, err := control.GetSession(goalID)
		if cloud.IsStatus(err, 404) {
			return nil
		}
		if err != nil {
			return err
		}
		if cloudSessionDisplayStatus(*goal) == "stopped" {
			return nil
		}
	}
}

// followLocalLogs prints events appended to a local Goal's log until it stops.
func followLocalLogs(goalID string, printed int, printer *logFollowPrinter) error {
	for {
		time.Sleep(logFollowInterval)
		events, err := getEventsFromAnywhere(goalID)
		if err != nil {
			return err
		}
		if len(events) > printed {
			if err := printer.print(events[printed:]); err != nil {
				return err
			}
			printed = len(events)
		}
		goal, err := getSessionFromAnywhere(goalID)
		if err != nil {
			return err
		}
		if !localGoalActive(*goal) {
			return nil
		}
	}
}

func localGoalActive(goal sessionapi.Session) bool {
	return goal.Status == sessionapi.StatusPending || goal.Status == sessionapi.StatusRunning
}

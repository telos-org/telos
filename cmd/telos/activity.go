package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"reflect"
	"slices"
	"time"

	"github.com/telos-org/telos/internal/activity"
	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/sessionapi"
)

func printActivity(out io.Writer, entries []activity.Entry) {
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		stamp := ""
		if entry.Timestamp != nil {
			stamp = *entry.Timestamp
		}
		detail := ""
		if entry.Body != nil {
			detail = *entry.Body
		}
		level := "INFO"
		if entry.Tone == "warning" {
			level = "WARN"
		}
		if entry.Tone == "danger" {
			level = "ERROR"
		}
		printRenderedLogRow(out, renderedLogRow{Timestamp: stamp, Level: level, Summary: entry.Headline, Detail: detail})
	}
}

func activitySession(positions cloud.ActivityPositions) string {
	if positions.Session == nil {
		return ""
	}
	return *positions.Session
}

func readActivityPages(ctx context.Context, control *cloud.Client, goalID string, first *cloud.ActivityPage, count int) ([]activity.Entry, error) {
	entries := append([]activity.Entry{}, first.Entries...)
	page := first
	seen := map[string]bool{}
	for page.NextCursor != nil && (count == 0 || len(entries) < count) {
		before := *page.NextCursor
		if seen[before] {
			return nil, errors.New("activity history did not advance")
		}
		seen[before] = true
		limit := 1000
		if count > 0 {
			limit = min(limit, count-len(entries))
		}
		var err error
		page, err = control.ActivityPage(ctx, goalID, cloud.ActivityPageOptions{Limit: limit, Before: before})
		if err != nil {
			return nil, err
		}
		if activitySession(first.Cursors) != activitySession(page.Cursors) {
			return nil, errors.New("goal activity changed; run logs again")
		}
		entries = append(entries, page.Entries...)
	}
	return entries, nil
}

// Remove the pre-Activity fallback once the minimum supported cloud has Activity.
func printCloudActivity(control *cloud.Client, goalID string, options logViewOptions, follow bool) (bool, error) {
	limit := min(options.Tail, 1000)
	if options.All {
		limit = 1000
	}
	page, err := control.ActivityPage(context.Background(), goalID, cloud.ActivityPageOptions{Limit: limit})
	if cloud.IsStatus(err, 404) || cloud.IsStatus(err, 405) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	count := options.Tail
	if options.All {
		count = 0
	}
	var entries []activity.Entry
	if options.All && page.History == "recent" {
		// Sequence-less runtimes need the manager export for complete history,
		// but readable output still uses the canonical Activity projection.
		export, err := control.GetSessionLogPage(goalID, 0)
		if err != nil {
			return true, err
		}
		if export.Cursors != nil && export.Cursors.Session != nil && *export.Cursors.Session != activitySession(page.Cursors) {
			return true, errors.New("goal activity changed; run logs again")
		}
		goal, err := control.GetSession(goalID)
		if err != nil {
			return true, err
		}
		costs := goal.Inference == nil || (goal.Inference.Source != "subscription" && goal.Inference.Source != "byok")
		for i := len(export.RawEvents) - 1; i >= 0; i-- {
			var record map[string]any
			if err := json.Unmarshal(export.RawEvents[i], &record); err != nil {
				return true, err
			}
			if entry := activity.Project(record, fmt.Sprintf("export:%d", i), costs); entry != nil {
				entries = append(entries, *entry)
			}
		}
		if export.Cursors != nil {
			page.Cursors.Control = export.Cursors.Control
		}
	} else {
		entries, err = readActivityPages(context.Background(), control, goalID, page, count)
		if err != nil {
			return true, err
		}
	}
	if len(entries) == 0 {
		fmt.Fprintln(os.Stdout, "No activity yet.")
	} else {
		printActivity(os.Stdout, entries)
	}
	if page.History == "recent" && !options.All {
		fmt.Fprintln(os.Stderr, "Only recent runtime activity is available. Use --raw for its full evidence.")
	}
	if !follow {
		return true, nil
	}
	return true, followActivity(control, goalID, page)
}

// Compare complete rendered snapshots, never individual messages. Multiplicity
// and order remain meaningful even when a runtime has no durable identities.
func runtimeSnapshot(entries []activity.Entry) []activity.Entry {
	result := []activity.Entry{}
	for _, entry := range entries {
		if entry.Seq == nil {
			entry.ID, entry.EventSeq = "", nil
			result = append(result, entry)
		}
	}
	return result
}

// HTTP catch-up and live frames represent the same bounded replacement snapshot.
func printRuntimeSnapshot(out io.Writer, entries, previous []activity.Entry) []activity.Entry {
	snapshot := runtimeSnapshot(entries)
	if reflect.DeepEqual(previous, snapshot) {
		return previous
	}
	fmt.Fprintln(out, "\nRecent runtime snapshot (replaces the previous runtime snapshot):")
	if len(snapshot) == 0 {
		fmt.Fprintln(out, "No displayable runtime activity in this snapshot.")
	} else {
		printActivity(out, snapshot)
	}
	return snapshot
}

type activityUpdate struct {
	Entries  []activity.Entry
	Cursors  cloud.ActivityPositions
	Snapshot []activity.Entry
}

// Finish every required read before the follow loop commits replay positions.
func readActivityUpdate(ctx context.Context, control *cloud.Client, goalID string, positions cloud.ActivityPositions) (activityUpdate, error) {
	session := activitySession(positions)
	latest, err := control.ActivityPage(ctx, goalID, cloud.ActivityPageOptions{Limit: 1000, AfterRuntime: positions.Runtime, AfterControl: positions.Control, Session: session})
	// A restore invalidates only the runtime lower bound. Re-read before
	// acknowledging any position from the replacement session.
	if cloud.IsStatus(err, 409) || (err == nil && latest.Cursors.Session != nil && *latest.Cursors.Session != session) {
		latest, err = control.ActivityPage(ctx, goalID, cloud.ActivityPageOptions{Limit: 1000, AfterControl: positions.Control})
	}
	if err != nil {
		return activityUpdate{}, err
	}
	entries, err := readActivityPages(ctx, control, goalID, latest, 0)
	if err != nil {
		return activityUpdate{}, err
	}
	if latest.History == "recent" {
		controls := entries[:0]
		for _, entry := range entries {
			if entry.Seq != nil {
				controls = append(controls, entry)
			}
		}
		entries = controls
	}
	return activityUpdate{Entries: entries, Cursors: latest.Cursors, Snapshot: latest.RuntimeSnapshot}, nil
}

func followActivity(control *cloud.Client, goalID string, page *cloud.ActivityPage) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	positions := page.Cursors
	lastSnapshot := runtimeSnapshot(page.Entries)
	// Fetch new entries on goal notifications and after a reconnect. HTTP uses
	// the same projection as live frames and also includes control-plane activity.
	catchUp := func() error {
		update, err := readActivityUpdate(ctx, control, goalID, positions)
		if err != nil {
			return err
		}
		// Commit replay positions only after every required read succeeds.
		if update.Cursors.Session != nil {
			if activitySession(positions) != *update.Cursors.Session {
				lastSnapshot = nil
			}
			positions = update.Cursors
		} else {
			// A control-only outage response has no authority to reset runtime replay.
			positions.Control = update.Cursors.Control
		}
		if update.Snapshot != nil {
			lastSnapshot = printRuntimeSnapshot(os.Stdout, update.Snapshot, lastSnapshot)
		}
		printActivity(os.Stdout, update.Entries)
		return nil
	}
	done := errors.New("goal stopped")
	for ctx.Err() == nil {
		err := control.FollowActivity(ctx, goalID, activitySession(positions), positions.Runtime, func(frame cloud.ActivityFrame) error {
			switch frame.Type {
			case "access":
				if !frame.Allowed {
					return &cloud.APIError{StatusCode: 403, Detail: "goal access revoked"}
				}
			case "runtime":
				if frame.Initializing {
					return nil
				}
				if frame.Session != activitySession(positions) {
					lastSnapshot = nil
					positions.Session = &frame.Session
					positions.Runtime = nil
				}
				if frame.Snapshot {
					fresh := append([]activity.Entry{}, frame.Events...)
					slices.Reverse(fresh)
					lastSnapshot = printRuntimeSnapshot(os.Stdout, fresh, lastSnapshot)
					positions.Runtime = nil
					return nil
				}
				if positions.Runtime != nil && frame.Sequence <= *positions.Runtime {
					return nil
				}
				if positions.Runtime == nil || frame.CoveredAfter > *positions.Runtime {
					return catchUp()
				}
				fresh := []activity.Entry{}
				for i := len(frame.Events) - 1; i >= 0; i-- {
					entry := frame.Events[i]
					if entry.EventSeq != nil && *entry.EventSeq > *positions.Runtime {
						fresh = append(fresh, entry)
					}
				}
				printActivity(os.Stdout, fresh)
				sequence := frame.Sequence
				positions.Runtime = &sequence
			case "ready", "changed":
				goal, err := control.GetSession(goalID)
				if cloud.IsStatus(err, 404) {
					return done
				}
				if err != nil {
					return err
				}
				// Observe stop before the final read, so no last update can fall
				// between catch-up and the decision to leave the stream.
				if err := catchUp(); err != nil {
					return err
				}
				if cloudSessionDisplayStatus(*goal) == "stopped" {
					return done
				}
			}
			return nil
		})
		if errors.Is(err, done) || ctx.Err() != nil {
			return nil
		}
		if cloud.IsStatus(err, 401) || cloud.IsStatus(err, 403) || cloud.IsStatus(err, 404) {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Second):
		}
		if err := catchUp(); err != nil {
			return err
		}
	}
	return nil
}

func localActivity(events []sessionapi.SessionEvent) []activity.Entry {
	entries := []activity.Entry{}
	for i := len(events) - 1; i >= 0; i-- {
		raw, _ := json.Marshal(events[i])
		var record map[string]any
		_ = json.Unmarshal(raw, &record)
		if entry := activity.Project(record, fmt.Sprintf("local:%d", i), true); entry != nil {
			entries = append(entries, *entry)
		}
	}
	return entries
}
func printLocalActivity(events []sessionapi.SessionEvent, options logViewOptions) {
	entries := localActivity(events)
	if !options.All && len(entries) > options.Tail {
		entries = entries[:options.Tail]
	}
	if len(entries) == 0 {
		fmt.Fprintln(os.Stdout, "No activity yet.")
		return
	}
	printActivity(os.Stdout, entries)
}

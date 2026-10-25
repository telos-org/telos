package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/activity"
	"github.com/telos-org/telos/internal/cloud"
)

func displayEntry(seq int64) activity.Entry {
	text := fmt.Sprintf("Update %d", seq)
	return activity.Entry{ID: fmt.Sprintf("rt:%d", seq), EventSeq: &seq, Kind: "goal", Headline: text, Tone: "neutral", Phase: "build"}
}
func TestReadableActivityUsesDisplayCursorsAndSharedLiveTransport(t *testing.T) {
	session := "run"
	head := int64(3)
	cp := int64(0)
	next := "older"
	degraded := false
	statusRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Error("missing authentication")
		}
		switch r.URL.Path {
		case "/api/deployments/goal/activity":
			page := cloud.ActivityPage{History: "paged", Cursors: cloud.ActivityPositions{Runtime: &head, Control: &cp, Session: &session}, Entries: []activity.Entry{}}
			switch {
			case r.URL.Query().Get("before") == "older":
				page.Entries = []activity.Entry{displayEntry(1)}
			case r.URL.Query().Get("before") == "outage-older":
				seq := int64(2)
				page.History = "recent"
				page.Cursors.Runtime, page.Cursors.Session = nil, nil
				page.Entries = []activity.Entry{{ID: "cp:2", Seq: &seq, Kind: "lifecycle", Headline: "Recovered older control", Tone: "neutral"}}
			case r.URL.Query().Has("after_rt"):
				switch r.URL.Query().Get("after_rt") {
				case "3":
					// This update and control event occurred between the history
					// response and subscription. No changed notification will replay them.
					head = 4
					cp = 1
					page.Entries = []activity.Entry{{ID: "cp:1", Seq: &cp, Kind: "lifecycle", Headline: "Provisioned", Tone: "neutral"}, displayEntry(4)}
				case "6":
					head = 6
					if r.URL.Query().Get("after_cp") != fmt.Sprint(cp) {
						t.Errorf("control watermark did not advance: %s", r.URL)
					}
					if !degraded {
						degraded = true
						page.History = "recent"
						page.Cursors.Runtime = nil
						page.Cursors.Session = nil
						cp = 3
						older := "outage-older"
						page.NextCursor = &older
						page.Entries = []activity.Entry{{ID: "cp:3", Seq: &cp, Kind: "lifecycle", Headline: "Recovered newer control", Tone: "neutral"}}
					}
				default:
					t.Errorf("runtime watermark regressed or skipped hidden events: %s", r.URL)
				}
			default:
				page.Entries = []activity.Entry{displayEntry(3), displayEntry(2)}
				page.NextCursor = &next
			}
			_ = json.NewEncoder(w).Encode(page)
		case "/api/live":
			if r.URL.Query().Get("view") != "activity" || r.URL.Query().Get("after_rt") != "3" {
				t.Errorf("wrong replay request: %s", r.URL)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			for _, frame := range []any{
				cloud.ActivityFrame{Type: "ready"},
				// The reader buffered this before the ready catch-up completed.
				cloud.ActivityFrame{Type: "runtime", Session: session, Sequence: 3, CoveredAfter: 2, Events: []activity.Entry{displayEntry(3)}},
				cloud.ActivityFrame{Type: "runtime", Session: session, Sequence: 5, CoveredAfter: 4, Events: []activity.Entry{displayEntry(4)}},
				cloud.ActivityFrame{Type: "runtime", Session: session, Sequence: 6, CoveredAfter: 5, Events: []activity.Entry{displayEntry(6)}},
				cloud.ActivityFrame{Type: "changed", Topics: []string{"goal"}},
				cloud.ActivityFrame{Type: "changed", Topics: []string{"goal"}},
			} {
				data, _ := json.Marshal(frame)
				fmt.Fprintf(w, "data: %s\n\n", data)
			}
		case "/api/deployments/goal":
			statusRequests++
			if statusRequests < 3 {
				fmt.Fprint(w, `{"id":"goal","state":"running"}`)
			} else {
				fmt.Fprint(w, `{"id":"goal","state":"stopped"}`)
			}
		default:
			t.Errorf("unexpected route %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	output := captureStdout(t, func() {
		handled, err := printCloudActivity(cloud.NewClient(server.URL, "token"), "goal", logViewOptions{Tail: 3}, true)
		if err != nil || !handled {
			t.Fatalf("handled=%v err=%v", handled, err)
		}
	})
	for _, n := range []int{1, 2, 3, 4, 6} {
		if strings.Count(output, fmt.Sprintf("Update %d", n)) != 1 {
			t.Fatalf("missing/duplicate output: %s", output)
		}
	}
	if strings.Index(output, "Update 1") > strings.Index(output, "Update 3") {
		t.Fatalf("history order reversed: %s", output)
	}
	if strings.Count(output, "Provisioned") != 1 {
		t.Fatalf("subscription gap lost control activity: %s", output)
	}
	if strings.Count(output, "Recovered older control") != 1 || strings.Count(output, "Recovered newer control") != 1 {
		t.Fatalf("control-only paging lost or duplicated activity: %s", output)
	}
}

func TestActivityFollowReplaysReplacementSessionBeforeAcknowledging(t *testing.T) {
	oldSession := "old"
	newSession := "new:restore:1"
	oldHead := int64(100)
	newHead := int64(3)
	cp := int64(0)
	recovered := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/live":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"type\":\"ready\"}\n\n")
		case "/api/deployments/goal/activity":
			if r.URL.Query().Get("runtime_session") == oldSession {
				http.Error(w, `{"detail":"Activity history changed"}`, 409)
				return
			}
			if r.URL.Query().Has("after_rt") {
				t.Errorf("old runtime bound sent to replacement: %s", r.URL)
			}
			recovered = true
			_ = json.NewEncoder(w).Encode(cloud.ActivityPage{History: "paged", Entries: []activity.Entry{displayEntry(3), displayEntry(2), displayEntry(1)}, Cursors: cloud.ActivityPositions{Session: &newSession, Runtime: &newHead, Control: &cp}})
		case "/api/deployments/goal":
			fmt.Fprint(w, `{"id":"goal","state":"stopped"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	output := captureStdout(t, func() {
		err := followActivity(cloud.NewClient(server.URL, "token"), "goal", &cloud.ActivityPage{Cursors: cloud.ActivityPositions{Session: &oldSession, Runtime: &oldHead, Control: &cp}})
		if err != nil {
			t.Fatal(err)
		}
	})
	if !recovered {
		t.Fatal("replacement session was not recovered")
	}
	for _, n := range []int{1, 2, 3} {
		if strings.Count(output, fmt.Sprintf("Update %d", n)) != 1 {
			t.Fatalf("replacement activity skipped: %s", output)
		}
	}
}

func TestActivityFollowPresentsChangedSnapshotsWithoutDeduplicatingRows(t *testing.T) {
	session := "legacy"
	cp := int64(0)
	row := activity.Entry{ID: "initial", Kind: "goal", Headline: "Repeated update", Tone: "neutral", Phase: "build"}
	initial := []activity.Entry{row, row}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/live":
			w.Header().Set("Content-Type", "text/event-stream")
			for sequence, count := range []int{2, 2, 3, 3} {
				entries := []activity.Entry{}
				for i := 0; i < count; i++ {
					entry := row
					entry.ID = fmt.Sprintf("snapshot:%d:%d", sequence, i)
					entries = append(entries, entry)
				}
				frame, _ := json.Marshal(cloud.ActivityFrame{Type: "runtime", Session: session, Sequence: int64(sequence + 1), Snapshot: true, Events: entries})
				fmt.Fprintf(w, "data: %s\n\n", frame)
			}
			fmt.Fprint(w, "data: {\"type\":\"changed\"}\n\n")
		case "/api/deployments/goal/activity":
			_ = json.NewEncoder(w).Encode(cloud.ActivityPage{History: "recent", Entries: []activity.Entry{row, row, row}, Cursors: cloud.ActivityPositions{Session: &session, Control: &cp}})
		case "/api/deployments/goal":
			fmt.Fprint(w, `{"id":"goal","state":"stopped"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	output := captureStdout(t, func() {
		if err := followActivity(cloud.NewClient(server.URL, "token"), "goal", &cloud.ActivityPage{History: "recent", Entries: initial, Cursors: cloud.ActivityPositions{Session: &session, Control: &cp}}); err != nil {
			t.Fatal(err)
		}
	})
	if strings.Count(output, "replaces the previous runtime snapshot") != 1 || strings.Count(output, "Repeated update") != 3 {
		t.Fatalf("snapshot revisions or repeated rows were misrepresented: %s", output)
	}
}

func TestRecentActivityAllUsesCanonicalRowsAndSharedFollow(t *testing.T) {
	session := "run"
	cp := int64(0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/deployments/goal/activity":
			page := cloud.ActivityPage{History: "recent", Cursors: cloud.ActivityPositions{Session: &session, Control: &cp}, Entries: []activity.Entry{}}
			_ = json.NewEncoder(w).Encode(page)
		case "/api/deployments/goal/logs":
			if r.URL.RawQuery != "" {
				t.Errorf("--all must fetch full evidence: %s", r.URL)
			}
			fmt.Fprint(w, `{"events":[{"event":"budget_exceeded","data":{"session_cost_usd":12.34,"cap_usd":50}},{"event":"agent_progress","data":{"audience":"user","text":"Same update"}},{"event":"agent_progress","data":{"audience":"user","text":"Same update"}},{"event":"agent_progress","data":{"audience":"agent","text":"Hidden tool"}},{"event":"agent_suspended","data":{"error":"Credentials rejected","action":"Update credentials"}}],"cursors":{"session":"run","cp":0}}`)
		case "/api/live":
			if r.URL.Query().Get("view") != "activity" {
				t.Errorf("wrong follow transport: %s", r.URL)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"type\":\"ready\"}\n\n")
		case "/api/deployments/goal":
			fmt.Fprint(w, `{"id":"goal","state":"stopped","inference":{"source":"subscription"}}`)
		default:
			t.Errorf("unexpected route %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	output := captureStdout(t, func() {
		handled, err := printCloudActivity(cloud.NewClient(server.URL, "token"), "goal", logViewOptions{All: true}, true)
		if err != nil || !handled {
			t.Fatalf("handled=%v err=%v", handled, err)
		}
	})
	if strings.Count(output, "Same update") != 2 || strings.Contains(output, "Hidden tool") || strings.Contains(output, "$12.34") || !strings.Contains(output, "Execution suspended") || !strings.Contains(output, "Update credentials") {
		t.Fatalf("readable export lost Activity semantics: %s", output)
	}
}

func TestActivityFollowReadsFinalSnapshotBeforeStopping(t *testing.T) {
	for _, notification := range []string{"ready", "changed"} {
		t.Run(notification, func(t *testing.T) {
			session := "legacy"
			cp := int64(1)
			final := activity.Entry{ID: "snapshot:1", Kind: "goal", Headline: "Final update", Tone: "neutral", Phase: "build"}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/live":
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(w, "data: {\"type\":%q}\n\n", notification)
					// The stop notification is delivered before this final frame.
					frame, _ := json.Marshal(cloud.ActivityFrame{Type: "runtime", Session: session, Snapshot: true, Events: []activity.Entry{final, final}})
					fmt.Fprintf(w, "data: %s\n\n", frame)
				case "/api/deployments/goal/activity":
					entries := []activity.Entry{{ID: "cp:1", Seq: &cp, Kind: "lifecycle", Headline: "Stopped", Tone: "neutral", Phase: "deploy"}}
					_ = json.NewEncoder(w).Encode(cloud.ActivityPage{History: "recent", Entries: entries, RuntimeSnapshot: []activity.Entry{final, final}, Cursors: cloud.ActivityPositions{Session: &session, Control: &cp, Snapshot: true}})
				case "/api/deployments/goal":
					fmt.Fprint(w, `{"id":"goal","state":"stopped"}`)
				default:
					t.Errorf("unexpected route %s", r.URL)
				}
			}))
			defer server.Close()
			output := captureStdout(t, func() {
				if err := followActivity(cloud.NewClient(server.URL, "token"), "goal", &cloud.ActivityPage{History: "recent", Cursors: cloud.ActivityPositions{Session: &session}}); err != nil {
					t.Fatal(err)
				}
			})
			if strings.Count(output, "Final update") != 2 || strings.Count(output, "Stopped") != 1 || strings.Count(output, "replaces the previous runtime snapshot") != 1 {
				t.Fatalf("final runtime/control activity was lost or duplicated: %s", output)
			}
		})
	}
}

package sessionapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// execFixture drives the Exec routes of a real store over HTTP.
type execFixture struct {
	t     *testing.T
	store *FileStore
	base  string // .../api/sessions/
}

func newExecFixture(t *testing.T) *execFixture {
	t.Helper()
	store := newExecTestStore(t)
	mux := http.NewServeMux()
	RegisterRoutes(mux, store, AllowAllAuthorizer{}, RuntimeIdentity{})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &execFixture{t: t, store: store, base: srv.URL + "/api/sessions/"}
}

// request sends body as JSON to a route under /api/sessions/ and returns the
// status and the raw response. It is safe to call from any goroutine.
func (f *execFixture) request(method, route string, body any) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, f.base+route, reader)
	if err != nil {
		return 0, nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return resp.StatusCode, data, err
}

// call is request for the root session, with the response decoded into out.
func (f *execFixture) call(method, route string, body, out any) int {
	f.t.Helper()
	status, data, err := f.request(method, execTestSessionID+route, body)
	if err != nil {
		f.t.Fatalf("%s %s: %v", method, route, err)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			f.t.Fatalf("%s %s: status %d, decode %q: %v", method, route, status, data, err)
		}
	}
	return status
}

func (f *execFixture) pause(paused bool) {
	f.t.Helper()
	if status := f.call("PUT", "/pause", SessionPauseRequest{Paused: &paused}, nil); status != http.StatusOK {
		f.t.Fatalf("pause %v = %d", paused, status)
	}
}

// start opens a takeover that must become active at once, and returns it with
// its route.
func (f *execFixture) start(startID string) (ExecTakeover, string) {
	f.t.Helper()
	var takeover ExecTakeover
	status := f.call("POST", "/exec/start", ExecStartRequest{StartID: startID, Reason: "Fix repeated cart fetching", Operator: "operator@example.com"}, &takeover)
	if status != http.StatusOK || takeover.State != ExecActive || !execIDRE.MatchString(takeover.ExecID) {
		f.t.Fatalf("start = %d %+v", status, takeover)
	}
	return takeover, "/exec/" + takeover.ExecID
}

// refused sends a request that must fail with want, and returns its body.
func (f *execFixture) refused(method, route string, body any, want int) map[string]string {
	f.t.Helper()
	var failure map[string]string
	if status := f.call(method, route, body, &failure); status != want || failure["detail"] == "" {
		f.t.Fatalf("%s %s = %d %v, want %d with a detail", method, route, status, failure, want)
	}
	return failure
}

func (f *execFixture) journal(execID string) (string, *execJournal) {
	f.t.Helper()
	path := filepath.Join(f.store.Root, execTestSessionID, "takeover", execID, execJournalName)
	return path, mustReadExecJournal(f.t, path)
}

// openEpochUnderLiveWorker makes the session look mid-turn: its epoch is open
// and a worker holds the runner lock. It returns a function that closes the
// epoch as held, which is what a worker does once the hold stops its turn.
func (f *execFixture) openEpochUnderLiveWorker() (closeEpoch func()) {
	f.t.Helper()
	manifest := f.store.manifestPath(execTestSessionID)
	if _, err := MutateManifest(manifest, func(m *Manifest) error {
		m.Epochs = append(m.Epochs, NewEpoch(m, 1, tsNow(), nil))
		return nil
	}); err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(holdRunnerLock(f.t, f.store.sessionDir(execTestSessionID)))
	return func() {
		f.t.Helper()
		if _, err := MutateManifest(manifest, func(m *Manifest) error {
			now, held := tsNow(), EpochHeld
			m.Epochs[0].FinishedAt, m.Epochs[0].Result = &now, &held
			return nil
		}); err != nil {
			f.t.Fatal(err)
		}
	}
}

func execEventNames(j *execJournal) string {
	names := make([]string, len(j.Events))
	for i, event := range j.Events {
		names[i] = event.Event
	}
	return strings.Join(names, " ")
}

func TestExecSessionOverHTTP(t *testing.T) {
	f := newExecFixture(t)

	f.refused("GET", "/exec", nil, http.StatusNotFound)
	f.pause(true)
	takeover, route := f.start("start-1")
	if takeover.StartID != "start-1" || takeover.Reason != "Fix repeated cart fetching" || takeover.Operator != "operator@example.com" || takeover.StartedAt == "" {
		t.Fatalf("takeover = %+v", takeover)
	}
	var status ExecTakeover
	if code := f.call("GET", "/exec", nil, &status); code != http.StatusOK || status != takeover {
		t.Fatalf("status = %d %+v, want %+v", code, status, takeover)
	}
	// A lost answer: the same start_id returns the same takeover.
	if again, _ := f.start("start-1"); again != takeover {
		t.Fatalf("repeated start = %+v, want %+v", again, takeover)
	}

	var ended ExecEndResponse
	end := ExecEndRequest{Summary: "  The cart loads once per page view\n", Operator: "operator@example.com"}
	if code := f.call("POST", route+"/end", end, &ended); code != http.StatusOK || ended != (ExecEndResponse{ExecID: takeover.ExecID, StartID: "start-1", State: ExecEnded}) {
		t.Fatalf("end = %d %+v", code, ended)
	}
	path, j := f.journal(takeover.ExecID)
	if execEventNames(j) != "session_started end_requested session_ended" {
		t.Fatalf("journal events = %s", execEventNames(j))
	}
	if saved := j.find(execEventEndRequested); saved.Summary != "The cart loads once per page view" || saved.Operator != "operator@example.com" {
		t.Fatalf("end_requested = %+v", saved)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("journal mode = %v, %v", info.Mode(), err)
	}

	// Repeating End changes nothing and answers the same.
	var repeated ExecEndResponse
	if code := f.call("POST", route+"/end", ExecEndRequest{Summary: "Another summary"}, &repeated); code != http.StatusOK || repeated != ended {
		t.Fatalf("repeated end = %d %+v", code, repeated)
	}
	if _, j := f.journal(takeover.ExecID); len(j.Events) != 3 || j.find(execEventEndRequested).Summary != "The cart loads once per page view" {
		t.Fatalf("repeated end changed the journal: %+v", j.Events)
	}
	if code := f.call("GET", "/exec", nil, &status); code != http.StatusOK || status.ExecID != takeover.ExecID || status.State != ExecEnded {
		t.Fatalf("status after end = %d %+v", code, status)
	}

	// A start_id belongs to its takeover for good: it never opens another.
	var spent ExecTakeover
	if code := f.call("POST", "/exec/start", execStartRequest("start-1"), &spent); code != http.StatusOK || spent.ExecID != takeover.ExecID || spent.State != ExecEnded {
		t.Fatalf("start with a spent start_id = %d %+v", code, spent)
	}
	next, _ := f.start("start-2")
	if next.ExecID == takeover.ExecID || !strings.HasPrefix(next.ExecID, "exec_000002_") {
		t.Fatalf("next takeover = %+v", next)
	}
}

func TestExecRejectsRequestsItCannotServe(t *testing.T) {
	f := newExecFixture(t)
	root := execTestSessionID
	writeExecTestSession(t, f.store.Root, "sess_child", KindTask, &root)
	start := execStartRequest("start-1")

	// Start needs a paused root session.
	if detail := f.refused("POST", "/exec/start", start, http.StatusConflict)["detail"]; !strings.Contains(detail, "not paused") {
		t.Fatalf("start before pause: %q", detail)
	}
	f.pause(true)
	for name, tc := range map[string]struct {
		route  string
		body   any
		status int
	}{
		"missing session": {"sess_missing/exec/start", start, http.StatusNotFound},
		"child session":   {"sess_child/exec/start", start, http.StatusBadRequest},
		"no start_id":     {root + "/exec/start", ExecStartRequest{Reason: "Fix", Operator: "ada@example.com"}, http.StatusBadRequest},
		"long start_id":   {root + "/exec/start", execStartRequest(strings.Repeat("é", execStartIDMaxRunes+1)), http.StatusBadRequest},
		"no reason":       {root + "/exec/start", ExecStartRequest{StartID: "start-1", Reason: " ", Operator: "ada@example.com"}, http.StatusBadRequest},
		"long reason":     {root + "/exec/start", ExecStartRequest{StartID: "start-1", Reason: strings.Repeat("é", execReasonMaxRunes+1), Operator: "ada@example.com"}, http.StatusBadRequest},
		"no operator":     {root + "/exec/start", ExecStartRequest{StartID: "start-1", Reason: "Fix"}, http.StatusBadRequest},
		"long operator":   {root + "/exec/start", ExecStartRequest{StartID: "start-1", Reason: "Fix", Operator: strings.Repeat("é", execOperatorMaxRunes+1)}, http.StatusBadRequest},
		"unknown field":   {root + "/exec/start", map[string]string{"start_id": "start-1", "reason": "Fix", "operator": "ada@example.com", "resume": "no"}, http.StatusBadRequest},
		"no body":         {root + "/exec/start", nil, http.StatusBadRequest},
	} {
		if status, data, err := f.request("POST", tc.route, tc.body); err != nil || status != tc.status {
			t.Errorf("start, %s = %d %s %v, want %d", name, status, data, err, tc.status)
		}
	}
	f.refused("GET", "/exec", nil, http.StatusNotFound) // nothing above opened a takeover

	// The limits count characters, and the longest values are accepted.
	longest := ExecStartRequest{StartID: strings.Repeat("é", execStartIDMaxRunes), Reason: strings.Repeat("é", execReasonMaxRunes), Operator: strings.Repeat("é", execOperatorMaxRunes)}
	var long ExecTakeover
	if code := f.call("POST", "/exec/start", longest, &long); code != http.StatusOK || long.Reason != longest.Reason {
		t.Fatalf("start with the longest values = %d", code)
	}
	if code := f.call("POST", "/exec/"+long.ExecID+"/end", ExecEndRequest{Summary: "Done"}, nil); code != http.StatusOK {
		t.Fatalf("end = %d", code)
	}

	takeover, route := f.start("start-1")
	for name, tc := range map[string]struct {
		route  string
		body   any
		status int
	}{
		"no summary":         {route + "/end", ExecEndRequest{Summary: " "}, http.StatusBadRequest},
		"long operator":      {route + "/end", ExecEndRequest{Summary: "Done", Operator: strings.Repeat("é", execOperatorMaxRunes+1)}, http.StatusBadRequest},
		"unknown field":      {route + "/end", map[string]any{"summary": "Done", "resume": false}, http.StatusBadRequest},
		"unknown takeover":   {"/exec/exec_000009_0123456789ab/end", ExecEndRequest{Summary: "Done"}, http.StatusNotFound},
		"malformed takeover": {"/exec/..%2f../end", ExecEndRequest{Summary: "Done"}, http.StatusNotFound},
	} {
		if status, data, err := f.request("POST", root+tc.route, tc.body); err != nil || status != tc.status {
			t.Errorf("end, %s = %d %.200s %v, want %d", name, status, data, err, tc.status)
		}
	}
	if _, j := f.journal(takeover.ExecID); execEventNames(j) != "session_started" {
		t.Fatalf("a rejected request was recorded: %s", execEventNames(j))
	}
}

func TestExecEndRejectsOverlongSummary(t *testing.T) {
	f := newExecFixture(t)
	f.pause(true)
	takeover, route := f.start("start-1")

	tooLong := strings.Repeat("é", execSummaryMaxRunes+1)
	if detail := f.refused("POST", route+"/end", ExecEndRequest{Summary: tooLong}, http.StatusBadRequest)["detail"]; !strings.Contains(detail, "at most 4000") {
		t.Fatalf("overlong summary = %q", detail)
	}
	if _, j := f.journal(takeover.ExecID); j.find(execEventEndRequested) != nil {
		t.Fatalf("a refused summary was saved: %s", execEventNames(j))
	}
	var ended ExecEndResponse
	longest := strings.Repeat("é", execSummaryMaxRunes)
	if code := f.call("POST", route+"/end", ExecEndRequest{Summary: "  " + longest + "\n"}, &ended); code != http.StatusOK || ended.State != ExecEnded {
		t.Fatalf("end with the longest summary = %d %+v", code, ended)
	}
	if _, j := f.journal(takeover.ExecID); j.find(execEventEndRequested).Summary != longest {
		t.Fatal("the saved summary is not the one sent")
	}
}

func TestExecStartWaitsForAgentTurnToStop(t *testing.T) {
	f := newExecFixture(t)
	f.pause(true)
	closeEpoch := f.openEpochUnderLiveWorker()

	start := execStartRequest("start-1")
	var starting, status ExecTakeover
	if code := f.call("POST", "/exec/start", start, &starting); code != http.StatusAccepted || starting.State != ExecStarting || !execIDRE.MatchString(starting.ExecID) {
		t.Fatalf("start during a turn = %d %+v", code, starting)
	}
	if code := f.call("POST", "/exec/start", start, &status); code != http.StatusAccepted || status != starting {
		t.Fatalf("repeated start during a turn = %d %+v", code, status)
	}
	if code := f.call("GET", "/exec", nil, &status); code != http.StatusOK || status != starting {
		t.Fatalf("status during a turn = %d %+v", code, status)
	}
	if conflict := f.refused("POST", "/exec/start", execStartRequest("start-2"), http.StatusConflict); conflict["exec_id"] != starting.ExecID {
		t.Fatalf("competing start while starting = %v", conflict)
	}
	// A pause lifted while Start waits ends the wait: Start needs the pause.
	f.pause(false)
	// The takeover stays open, and the refusal names it so the caller can end it.
	if lifted := f.refused("POST", "/exec/start", start, http.StatusConflict); !strings.Contains(lifted["detail"], "not paused") || lifted["exec_id"] != starting.ExecID {
		t.Fatalf("start after the pause lifted: %v", lifted)
	}
	f.pause(true)

	// The hold stops the turn: the worker closes its epoch and waits, still alive.
	closeEpoch()
	if code := f.call("POST", "/exec/start", start, &status); code != http.StatusOK || status.ExecID != starting.ExecID || status.State != ExecActive {
		t.Fatalf("start after the turn stopped = %d %+v", code, status)
	}
	if _, j := f.journal(starting.ExecID); execEventNames(j) != "session_started" {
		t.Fatalf("journal events = %s", execEventNames(j))
	}
}

func TestExecEndsTakeoverThatNeverBecameActive(t *testing.T) {
	f := newExecFixture(t)
	f.pause(true)
	f.openEpochUnderLiveWorker()

	var starting ExecTakeover
	if code := f.call("POST", "/exec/start", execStartRequest("start-1"), &starting); code != http.StatusAccepted {
		t.Fatalf("start = %d %+v", code, starting)
	}
	var ended ExecEndResponse
	if code := f.call("POST", "/exec/"+starting.ExecID+"/end", ExecEndRequest{Summary: "The agents did not stop"}, &ended); code != http.StatusOK || ended.State != ExecEnded {
		t.Fatalf("end = %d %+v", code, ended)
	}
	if _, j := f.journal(starting.ExecID); execEventNames(j) != "end_requested session_ended" {
		t.Fatalf("journal events = %s", execEventNames(j))
	}
	// It is out of the way of the next Start, and its own start_id stays ended.
	var next, spent ExecTakeover
	if code := f.call("POST", "/exec/start", execStartRequest("start-2"), &next); code != http.StatusAccepted || next.ExecID == starting.ExecID {
		t.Fatalf("next start = %d %+v", code, next)
	}
	if code := f.call("POST", "/exec/start", execStartRequest("start-1"), &spent); code != http.StatusOK || spent.ExecID != starting.ExecID || spent.State != ExecEnded {
		t.Fatalf("ended start_id = %d %+v", code, spent)
	}
}

func TestExecCompetingStartIsRejected(t *testing.T) {
	f := newExecFixture(t)
	f.pause(true)
	first, route := f.start("start-a")
	if conflict := f.refused("POST", "/exec/start", execStartRequest("start-b"), http.StatusConflict); conflict["exec_id"] != first.ExecID {
		t.Fatalf("competing start = %v, want exec_id %s", conflict, first.ExecID)
	}
	if code := f.call("POST", route+"/end", ExecEndRequest{Summary: "Done"}, nil); code != http.StatusOK {
		t.Fatalf("end = %d", code)
	}

	// Of several callers that start at once, one opens the takeover. The
	// others are told which takeover is in their way.
	const callers = 8
	type outcome struct {
		status int
		body   map[string]string
	}
	outcomes := make([]outcome, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, data, _ := f.request("POST", execTestSessionID+"/exec/start", execStartRequest("racer-"+strconv.Itoa(i)))
			outcomes[i].status = status
			_ = json.Unmarshal(data, &outcomes[i].body)
		}()
	}
	wg.Wait()
	winner := ""
	for _, outcome := range outcomes {
		if outcome.status == http.StatusOK {
			if winner != "" {
				t.Fatalf("two starts opened takeovers: %+v", outcomes)
			}
			winner = outcome.body["exec_id"]
		}
	}
	for _, outcome := range outcomes {
		if winner == "" || (outcome.status != http.StatusOK && (outcome.status != http.StatusConflict || outcome.body["exec_id"] != winner)) {
			t.Fatalf("competing starts = %+v", outcomes)
		}
	}
}

// A journal that can't be read is the user's own doing: they share the
// machine with it. Nothing can be done with that takeover, and it does not
// hold the goal against a new one.
func TestExecUnreadableJournalIsInNobodysWay(t *testing.T) {
	f := newExecFixture(t)
	f.pause(true)
	first, _ := f.start("start-1")
	if code := f.call("POST", "/exec/"+first.ExecID+"/end", ExecEndRequest{Summary: "Done"}, nil); code != http.StatusOK {
		t.Fatalf("end = %d", code)
	}
	damaged, route := f.start("start-2")
	path, _ := f.journal(damaged.ExecID)
	if err := os.WriteFile(path, []byte(`{"exec_id":`), 0o600); err != nil {
		t.Fatal(err)
	}

	if detail := f.refused("POST", route+"/end", ExecEndRequest{Summary: "Done"}, http.StatusConflict)["detail"]; !strings.Contains(detail, "can't be read") {
		t.Fatalf("end with an unreadable journal = %q", detail)
	}
	// Status reports the most recent takeover it can read.
	var status ExecTakeover
	if code := f.call("GET", "/exec", nil, &status); code != http.StatusOK || status.ExecID != first.ExecID || status.State != ExecEnded {
		t.Fatalf("status = %d %+v", code, status)
	}
	next, _ := f.start("start-3")
	if !strings.HasPrefix(next.ExecID, "exec_000003_") {
		t.Fatalf("next takeover = %+v", next)
	}
	if data, _ := os.ReadFile(path); string(data) != `{"exec_id":` {
		t.Fatalf("an unreadable journal was rewritten: %q", data)
	}
}

// A crash between End's two writes leaves the takeover ending. It still
// holds the session, and a repeated End finishes it.
func TestExecEndContinuesAfterCrashBetweenItsWrites(t *testing.T) {
	f := newExecFixture(t)
	f.pause(true)
	takeover, route := f.start("start-1")
	path, j := f.journal(takeover.ExecID)
	if err := appendExecEvent(path, j, execEvent{Event: execEventEndRequested, Timestamp: tsNow(), Summary: "The first summary"}); err != nil {
		t.Fatal(err)
	}

	var status ExecTakeover
	if code := f.call("GET", "/exec", nil, &status); code != http.StatusOK || status.ExecID != takeover.ExecID || status.State != ExecEnding {
		t.Fatalf("status = %d %+v", code, status)
	}
	if code := f.call("POST", "/exec/start", execStartRequest("start-1"), &status); code != http.StatusOK || status.ExecID != takeover.ExecID || status.State != ExecEnding {
		t.Fatalf("start with the same start_id = %d %+v", code, status)
	}
	if conflict := f.refused("POST", "/exec/start", execStartRequest("start-2"), http.StatusConflict); conflict["exec_id"] != takeover.ExecID {
		t.Fatalf("competing start = %v", conflict)
	}
	var ended ExecEndResponse
	if code := f.call("POST", route+"/end", ExecEndRequest{Summary: "A later summary"}, &ended); code != http.StatusOK || ended.State != ExecEnded {
		t.Fatalf("end = %d %+v", code, ended)
	}
	if _, j := f.journal(takeover.ExecID); execEventNames(j) != "session_started end_requested session_ended" || j.find(execEventEndRequested).Summary != "The first summary" {
		t.Fatalf("journal = %s, summary %q", execEventNames(j), j.find(execEventEndRequested).Summary)
	}
}

// A blocked goal is where a person is most needed. Start asks only for the
// pause.
func TestExecStartsOnBlockedAndPausedSession(t *testing.T) {
	f := newExecFixture(t)
	if _, err := MutateManifest(f.store.manifestPath(execTestSessionID), func(m *Manifest) error {
		m.Issue = &Issue{Status: IssueBlocked, Reason: ReasonSpecUnsatisfiable, Message: "The spec asks for two things at once.", Since: tsNow()}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if detail := f.refused("POST", "/exec/start", execStartRequest("start-1"), http.StatusConflict)["detail"]; !strings.Contains(detail, "not paused") {
		t.Fatalf("start on a blocked goal that is not paused: %q", detail)
	}
	f.pause(true)
	f.start("start-1")
}

// A takeover's ID has room for 999,999 takeovers. Past that, Start refuses
// instead of opening a takeover that no other route can find.
func TestExecStartRefusesWhenTakeoverIDsAreUsedUp(t *testing.T) {
	f := newExecFixture(t)
	f.pause(true)
	last := "exec_999999_0123456789ab"
	path := filepath.Join(f.store.Root, execTestSessionID, "takeover", last, execJournalName)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	ended := &execJournal{ExecID: last, SessionID: execTestSessionID, StartID: "start-old", Reason: "Fix", StartedAt: tsNow(), Events: []execEvent{{Event: execEventSessionEnded}}}
	if err := writeExecJournal(path, ended); err != nil {
		t.Fatal(err)
	}
	if detail := f.refused("POST", "/exec/start", execStartRequest("start-1"), http.StatusConflict)["detail"]; !strings.Contains(detail, "used up") {
		t.Fatalf("start = %q", detail)
	}
	if entries, err := os.ReadDir(filepath.Dir(filepath.Dir(path))); err != nil || len(entries) != 1 {
		t.Fatalf("a refused start left a takeover: %v, %v", entries, err)
	}
}

// execlessStore is a Store that does not support Exec.
type execlessStore struct{ Store }

func TestExecRoutesAnswerNotImplementedWithoutAnExecStore(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, execlessStore{newExecTestStore(t)}, AllowAllAuthorizer{}, RuntimeIdentity{})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	for _, tc := range []struct{ method, route string }{
		{"GET", "/exec"},
		{"POST", "/exec/start"},
		{"POST", "/exec/exec_000001_0123456789ab/end"},
	} {
		req, _ := http.NewRequest(tc.method, srv.URL+"/api/sessions/"+execTestSessionID+tc.route, strings.NewReader("{}"))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotImplemented {
			t.Errorf("%s %s = %d, want 501", tc.method, tc.route, resp.StatusCode)
		}
	}
}

func TestExecRoutesRequireOperator(t *testing.T) {
	store := newExecTestStore(t)
	mux := http.NewServeMux()
	RegisterRoutes(mux, store, NewBearerAuthorizer(store, "operator-token"), RuntimeIdentity{})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	call := func(method, route, token, body string) int {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+"/api/sessions/"+execTestSessionID+route, strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	if code := call("PUT", "/pause", "operator-token", `{"paused":true}`); code != http.StatusOK {
		t.Fatalf("pause = %d", code)
	}
	if code := call("POST", "/exec/start", "operator-token", `{"start_id":"start-1","reason":"Fix","operator":"ada@example.com"}`); code != http.StatusOK {
		t.Fatalf("operator start = %d", code)
	}
	takeover, err := store.ExecStatus(execTestSessionID)
	if err != nil {
		t.Fatal(err)
	}
	// The session's own token belongs to its agents. They may not take over.
	agentToken := "tok_" + execTestSessionID
	for _, tc := range []struct{ method, route, body string }{
		{"GET", "/exec", ""},
		{"POST", "/exec/start", `{"start_id":"start-1","reason":"Fix","operator":"ada@example.com"}`},
		{"POST", "/exec/" + takeover.ExecID + "/end", `{"summary":"Done"}`},
	} {
		if code := call(tc.method, tc.route, agentToken, tc.body); code != http.StatusForbidden {
			t.Errorf("agent token %s %s = %d, want 403", tc.method, tc.route, code)
		}
		if code := call(tc.method, tc.route, "", tc.body); code != http.StatusUnauthorized {
			t.Errorf("no token %s %s = %d, want 401", tc.method, tc.route, code)
		}
	}
	if _, j, err := store.loadExec(execTestSessionID, takeover.ExecID); err != nil || execEventNames(j) != "session_started" {
		t.Fatalf("a refused request changed the takeover: %v", err)
	}
}

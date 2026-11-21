package telosd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/telos-org/telos/internal/cli"
	"github.com/telos-org/telos/internal/game"
	"github.com/telos-org/telos/internal/sessionapi"
)

// heldTurnAgent stands in for the Pi agent. Its turn runs until a hold asks
// it to stop, and then returns only when the test releases it.
type heldTurnAgent struct {
	started, stopSeen, release chan struct{}
	once, seen                 sync.Once
}

func (a *heldTurnAgent) ExecuteTurn(_ string, role string, ts *game.TurnState) game.TurnResult {
	a.once.Do(func() { close(a.started) })
	for !ts.StopRequested() {
		time.Sleep(5 * time.Millisecond)
	}
	a.seen.Do(func() { close(a.stopSeen) })
	<-a.release
	return game.TurnResult{Role: role, Status: game.StatusContinue, Logs: "stopped", Error: "local_interrupted:stop_requested", Recoverable: true}
}

func (a *heldTurnAgent) CheckpointWorkspace(string) bool { return false }

// newExecGoal creates a root session behind the store telosd serves the
// Sessions API from, and returns that store with the session's ID and
// directory.
func newExecGoal(t *testing.T) (*controllerReconciler, string, string) {
	t.Helper()
	base := sessionapi.NewFileStore(t.TempDir(), sessionapi.RuntimeCloud)
	store := newControllerReconciler(base, &recordingSubstrate{}, nil, cloudControllerDefaults())
	markdown := "---\nversion: 0.1.0\nname: shop\nplatform: cloud\n---\n# Shop\n"
	created, err := store.UpdateSpec("shop", sessionapi.SessionSpecUpdateRequest{SpecMarkdown: markdown})
	if err != nil {
		t.Fatal(err)
	}
	return store, created.Session.SessionID, *created.Session.SessionDir
}

// telosd serves the Sessions API from the reconciler, not from the file store
// it wraps. This drives an Exec takeover through that wiring, with the bearer
// authorizer telosd uses.
func TestExecTakeoverThroughReconcilerRoutes(t *testing.T) {
	store, id, sessionDir := newExecGoal(t)
	mux := http.NewServeMux()
	sessionapi.RegisterRoutes(mux, store, sessionapi.NewBearerAuthorizer(store.FileStore, "operator-token"), sessionapi.RuntimeIdentity{})
	srv := httptest.NewServer(withCloudCORS(mux.ServeHTTP))
	defer srv.Close()
	manifest, err := sessionapi.ReadManifest(filepath.Join(sessionDir, "session.json"))
	if err != nil {
		t.Fatal(err)
	}

	call := func(method, route, token, body string, out any) int {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+"/api/sessions/"+id+route, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		if out != nil {
			if err := json.Unmarshal(data, out); err != nil {
				t.Fatalf("%s %s: status %d, decode %q: %v", method, route, resp.StatusCode, data, err)
			}
		}
		return resp.StatusCode
	}

	// The goal's own agents hold the session token. They may not take over.
	start := `{"start_id":"start-1","reason":"Fix","operator":"ada@example.com"}`
	if code := call("POST", "/exec/start", manifest.Access.APIToken, start, nil); code != http.StatusForbidden {
		t.Fatalf("agent token start = %d, want 403", code)
	}
	if code := call("POST", "/exec/start", "operator-token", start, nil); code != http.StatusConflict {
		t.Fatalf("start before pause = %d, want 409", code)
	}
	if code := call("PUT", "/pause", "operator-token", `{"paused":true}`, nil); code != http.StatusOK {
		t.Fatalf("pause = %d", code)
	}
	var takeover, status sessionapi.ExecTakeover
	if code := call("POST", "/exec/start", "operator-token", start, &takeover); code != http.StatusOK || takeover.State != sessionapi.ExecActive || takeover.Operator != "ada@example.com" {
		t.Fatalf("start = %d %+v", code, takeover)
	}
	if code := call("GET", "/exec", "operator-token", "", &status); code != http.StatusOK || status != takeover {
		t.Fatalf("status = %d %+v", code, status)
	}
	var ended sessionapi.ExecEndResponse
	if code := call("POST", "/exec/"+takeover.ExecID+"/end", "operator-token", `{"summary":"Fixed"}`, &ended); code != http.StatusOK || ended.State != sessionapi.ExecEnded || ended.StartID != "start-1" {
		t.Fatalf("end = %d %+v", code, ended)
	}
	// Ending a takeover leaves the pause alone: lifting it is the caller's.
	var session sessionapi.Session
	if code := call("GET", "", "operator-token", "", &session); code != http.StatusOK || !session.Paused {
		t.Fatalf("session after end = %d, paused %v", code, session.Paused)
	}
}

// Start may finish only when no agent turn is running. This runs the real
// worker and game loop, which are what open and close the epoch Start reads.
func TestExecStartWaitsForWorkerToStopItsTurn(t *testing.T) {
	store, id, sessionDir := newExecGoal(t)

	agent := &heldTurnAgent{started: make(chan struct{}), stopSeen: make(chan struct{}), release: make(chan struct{})}
	stop := make(chan os.Signal, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = runSessionWorker(sessionDir, false, func(dir string) (*game.PVGResult, error) {
			return cli.RunLocalSessionWithExecutor(dir, agent)
		}, make(chan os.Signal, 1), stop)
	}()
	defer func() {
		agent.seen.Do(func() { close(agent.stopSeen) })
		select {
		case <-agent.release:
		default:
			close(agent.release)
		}
		stop <- syscall.SIGTERM
		<-done
	}()
	wait := func(what string, event <-chan struct{}) {
		t.Helper()
		select {
		case <-event:
		case <-time.After(30 * time.Second):
			t.Fatalf("timed out waiting for %s", what)
		}
	}
	start := sessionapi.ExecStartRequest{StartID: "start-1", Reason: "Fix", Operator: "ada@example.com"}

	wait("the agent turn to start", agent.started)
	if _, err := store.ExecStart(id, start); err == nil || !strings.Contains(err.Error(), "not paused") {
		t.Fatalf("start on a running goal = %v", err)
	}
	if _, err := store.Pause(id, true); err != nil {
		t.Fatal(err)
	}
	// The turn has seen the hold but has not returned yet: Start waits.
	wait("the agent turn to see the hold", agent.stopSeen)
	takeover, err := store.ExecStart(id, start)
	if err != nil || takeover.State != sessionapi.ExecStarting {
		t.Fatalf("start while the turn is stopping = %+v, %v", takeover, err)
	}

	// The turn returns, the game closes its epoch as held, and Start finishes.
	close(agent.release)
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		again, err := store.ExecStart(id, start)
		if err != nil || again.ExecID != takeover.ExecID {
			t.Fatalf("repeated start = %+v, %v", again, err)
		}
		if again.State == sessionapi.ExecActive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("start never became active: %+v", again)
		}
	}
	manifest, err := sessionapi.ReadManifest(filepath.Join(sessionDir, "session.json"))
	if err != nil || manifest.OpenEpoch() != nil || manifest.LastEpoch() == nil || *manifest.LastEpoch().Result != sessionapi.EpochHeld {
		t.Fatalf("epoch after the hold = %+v, %v", manifest.LastEpoch(), err)
	}
}

// countingAgent counts the turns it is asked for.
type countingAgent struct{ turns atomic.Int32 }

func (a *countingAgent) ExecuteTurn(_ string, role string, _ *game.TurnState) game.TurnResult {
	a.turns.Add(1)
	return game.TurnResult{Role: role, Status: game.StatusContinue, Error: "local_interrupted:stop_requested", Recoverable: true}
}

func (a *countingAgent) CheckpointWorkspace(string) bool { return false }

// A worker can pass its hold check just before the pause, and open its epoch
// just after Start answered that no agent turn is running. That game sees the
// hold before its first turn: it asks the agents for nothing and closes its
// epoch as held.
func TestGameThatStartsAfterExecStartRunsNoAgentTurn(t *testing.T) {
	store, id, sessionDir := newExecGoal(t)
	if _, err := store.Pause(id, true); err != nil {
		t.Fatal(err)
	}
	takeover, err := store.ExecStart(id, sessionapi.ExecStartRequest{StartID: "start-1", Reason: "Fix", Operator: "ada@example.com"})
	if err != nil || takeover.State != sessionapi.ExecActive {
		t.Fatalf("start = %+v, %v", takeover, err)
	}

	agent := &countingAgent{}
	result, err := cli.RunLocalSessionWithExecutor(sessionDir, agent)
	if err != nil || result.GameResult != game.GameStopped {
		t.Fatalf("game on a paused goal = %+v, %v", result, err)
	}
	if turns := agent.turns.Load(); turns != 0 {
		t.Fatalf("the game asked the agents for %d turns during a takeover", turns)
	}
	manifest, err := sessionapi.ReadManifest(filepath.Join(sessionDir, "session.json"))
	if err != nil || manifest.OpenEpoch() != nil || manifest.LastEpoch() == nil || *manifest.LastEpoch().Result != sessionapi.EpochHeld {
		t.Fatalf("epoch after the game = %+v, %v", manifest.LastEpoch(), err)
	}
}

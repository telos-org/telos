package telosd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/telos-org/telos/internal/sessionapi"
)

const (
	testCheckpointSessionID   = "sess_deployment_123"
	testCheckpointOperationID = "snap_operation_a"
	testCheckpointOperationB  = "snap_operation_b"
)

func newTestCheckpointSafePoint(t *testing.T) *checkpointSafePoint {
	t.Helper()
	safePoint, err := newCheckpointSafePoint(t.TempDir(), testCheckpointSessionID)
	if err != nil {
		t.Fatal(err)
	}
	safePoint.prepareTimeout = 75 * time.Millisecond
	safePoint.pollInterval = time.Millisecond
	return safePoint
}

func checkpointStatus(t *testing.T, safePoint *checkpointSafePoint) string {
	t.Helper()
	var status string
	if err := safePoint.withLock(func() error {
		state, err := safePoint.readStateLocked()
		status = state.Status
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return status
}

func checkpointStateValue(t *testing.T, safePoint *checkpointSafePoint) checkpointState {
	t.Helper()
	var state checkpointState
	if err := safePoint.withLock(func() error {
		var err error
		state, err = safePoint.readStateLocked()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestCheckpointPrepareTimeoutDoesNotKillWorkAndRetryCompletes(t *testing.T) {
	safePoint := newTestCheckpointSafePoint(t)
	lease, err := safePoint.acquire()
	if err != nil {
		t.Fatal(err)
	}

	status, inFlight, err := safePoint.prepare(context.Background(), testCheckpointOperationID)
	if err != nil {
		t.Fatal(err)
	}
	if status != "timeout" || inFlight != 1 {
		t.Fatalf("prepare: got status=%q in_flight=%d", status, inFlight)
	}
	if lease.file == nil {
		t.Fatal("prepare timeout released or killed the admitted work lease")
	}
	if _, err := safePoint.acquire(); !errors.Is(err, errCheckpointAdmissionClosed) {
		t.Fatalf("new admission while preparing: got %v", err)
	}

	lease.release()
	status, inFlight, err = safePoint.prepare(context.Background(), testCheckpointOperationID)
	if err != nil {
		t.Fatal(err)
	}
	if status != "prepared" || inFlight != 0 {
		t.Fatalf("retry prepare: got status=%q in_flight=%d", status, inFlight)
	}
}

func TestCheckpointPrepareRejectsUnreadyWorkersWithoutClosingAdmission(t *testing.T) {
	safePoint := newTestCheckpointSafePoint(t)
	safePoint.prepareCheck = func() error {
		return errCheckpointWorkersNotReady
	}

	status, inFlight, err := safePoint.prepare(context.Background(), testCheckpointOperationID)
	if !errors.Is(err, errCheckpointWorkersNotReady) || status != "error" || inFlight != 0 {
		t.Fatalf("prepare: status=%q in_flight=%d err=%v", status, inFlight, err)
	}
	if got := checkpointStatus(t, safePoint); got != "open" {
		t.Fatalf("rejected prepare closed admission: %q", got)
	}
	lease, err := safePoint.acquire()
	if err != nil {
		t.Fatalf("admission after rejected prepare: %v", err)
	}
	lease.release()
}

func TestCheckpointPrepareRechecksWorkersBeforeCommittingPrepared(t *testing.T) {
	safePoint := newTestCheckpointSafePoint(t)
	checks := 0
	safePoint.prepareCheck = func() error {
		checks++
		if checks == 2 {
			return errCheckpointWorkersNotReady
		}
		return nil
	}

	status, _, err := safePoint.prepare(context.Background(), testCheckpointOperationID)
	if !errors.Is(err, errCheckpointWorkersNotReady) || status != "preparing" {
		t.Fatalf("prepare readiness race: status=%q err=%v", status, err)
	}
	if checks != 2 {
		t.Fatalf("readiness checks: got %d want 2", checks)
	}
	state := checkpointStateValue(t, safePoint)
	if state.Status != "preparing" || state.OperationID != testCheckpointOperationID {
		t.Fatalf("readiness race committed prepared state: %#v", state)
	}
	if _, err := safePoint.acquire(); !errors.Is(err, errCheckpointAdmissionClosed) {
		t.Fatalf("readiness race reopened admission: %v", err)
	}

	safePoint.prepareCheck = func() error { return nil }
	status, _, err = safePoint.prepare(context.Background(), testCheckpointOperationID)
	if err != nil || status != "prepared" {
		t.Fatalf("same-owner retry: status=%q err=%v", status, err)
	}
}

func TestCheckpointPrepareAndResumeAreIdempotentAcrossRestart(t *testing.T) {
	root := t.TempDir()
	safePoint, err := newCheckpointSafePoint(root, testCheckpointSessionID)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		status, inFlight, err := safePoint.prepare(context.Background(), testCheckpointOperationID)
		if err != nil {
			t.Fatal(err)
		}
		if status != "prepared" || inFlight != 0 {
			t.Fatalf("prepare %d: got status=%q in_flight=%d", i, status, inFlight)
		}
	}

	restarted, err := newCheckpointSafePoint(root, testCheckpointSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.acquire(); !errors.Is(err, errCheckpointAdmissionClosed) {
		t.Fatalf("restart admission: got %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := restarted.resume(testCheckpointOperationID); err != nil {
			t.Fatal(err)
		}
	}
	lease, err := restarted.acquire()
	if err != nil {
		t.Fatalf("admission after resume: %v", err)
	}
	lease.release()
}

func TestCheckpointOperationOwnershipRejectsDelayedResume(t *testing.T) {
	safePoint := newTestCheckpointSafePoint(t)
	if status, _, err := safePoint.prepare(context.Background(), testCheckpointOperationID); err != nil || status != "prepared" {
		t.Fatalf("prepare A: status=%q err=%v", status, err)
	}
	if err := safePoint.resume(testCheckpointOperationID); err != nil {
		t.Fatalf("resume A: %v", err)
	}
	if status, _, err := safePoint.prepare(context.Background(), testCheckpointOperationB); err != nil || status != "prepared" {
		t.Fatalf("prepare B: status=%q err=%v", status, err)
	}
	if err := safePoint.resume(testCheckpointOperationID); !errors.Is(err, errCheckpointOperationMismatch) {
		t.Fatalf("stale resume A: got %v", err)
	}
	state := checkpointStateValue(t, safePoint)
	if state.Status != "prepared" || state.OperationID != testCheckpointOperationB {
		t.Fatalf("stale resume changed B ownership: %#v", state)
	}
	if _, err := safePoint.acquire(); !errors.Is(err, errCheckpointAdmissionClosed) {
		t.Fatalf("stale resume reopened admission: %v", err)
	}
	if err := safePoint.resume(testCheckpointOperationB); err != nil {
		t.Fatalf("resume B: %v", err)
	}
}

func TestCheckpointCompletedOwnerCannotPrepareAgain(t *testing.T) {
	safePoint := newTestCheckpointSafePoint(t)
	if _, _, err := safePoint.prepare(context.Background(), testCheckpointOperationID); err != nil {
		t.Fatal(err)
	}
	if err := safePoint.resume(testCheckpointOperationID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := safePoint.prepare(context.Background(), testCheckpointOperationID); !errors.Is(err, errCheckpointOperationCompleted) {
		t.Fatalf("completed operation prepared again: %v", err)
	}
	state := checkpointStateValue(t, safePoint)
	if state.Status != "open" || state.OperationID != testCheckpointOperationID {
		t.Fatalf("completed prepare changed state: %#v", state)
	}
	lease, err := safePoint.acquire()
	if err != nil {
		t.Fatalf("completed prepare closed admission: %v", err)
	}
	lease.release()
}

func TestCheckpointSameOwnerIsIdempotentAcrossRestart(t *testing.T) {
	root := t.TempDir()
	safePoint, err := newCheckpointSafePoint(root, testCheckpointSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := safePoint.prepare(context.Background(), testCheckpointOperationID); err != nil {
		t.Fatal(err)
	}
	restarted, err := newCheckpointSafePoint(root, testCheckpointSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if status, _, err := restarted.prepare(context.Background(), testCheckpointOperationID); err != nil || status != "prepared" {
		t.Fatalf("retry prepare after restart: status=%q err=%v", status, err)
	}
	if err := restarted.resume(testCheckpointOperationID); err != nil {
		t.Fatal(err)
	}
	restartedAgain, err := newCheckpointSafePoint(root, testCheckpointSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := restartedAgain.resume(testCheckpointOperationID); err != nil {
		t.Fatalf("retry resume after restart: %v", err)
	}
	state := checkpointStateValue(t, restartedAgain)
	if state.Status != "open" || state.OperationID != testCheckpointOperationID {
		t.Fatalf("resumed state: %#v", state)
	}
}

func TestCheckpointPreparingStateSurvivesRestart(t *testing.T) {
	root := t.TempDir()
	safePoint, err := newCheckpointSafePoint(root, testCheckpointSessionID)
	if err != nil {
		t.Fatal(err)
	}
	safePoint.prepareTimeout = 20 * time.Millisecond
	safePoint.pollInterval = time.Millisecond
	lease, err := safePoint.acquire()
	if err != nil {
		t.Fatal(err)
	}
	status, _, err := safePoint.prepare(context.Background(), testCheckpointOperationID)
	if err != nil || status != "timeout" {
		t.Fatalf("prepare: status=%q err=%v", status, err)
	}
	lease.release()

	restarted, err := newCheckpointSafePoint(root, testCheckpointSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.acquire(); !errors.Is(err, errCheckpointAdmissionClosed) {
		t.Fatalf("restart admission: got %v", err)
	}
	status, _, err = restarted.prepare(context.Background(), testCheckpointOperationID)
	if err != nil || status != "prepared" {
		t.Fatalf("restart prepare: status=%q err=%v", status, err)
	}
}

func TestCheckpointLeaseDrainsAcrossWorkerProcessCrash(t *testing.T) {
	root := t.TempDir()
	safePoint, err := newCheckpointSafePoint(root, testCheckpointSessionID)
	if err != nil {
		t.Fatal(err)
	}
	safePoint.prepareTimeout = 50 * time.Millisecond
	safePoint.pollInterval = time.Millisecond
	readyPath := filepath.Join(root, "worker-ready")
	cmd := exec.Command(os.Args[0], "-test.run=^TestCheckpointLeaseSubprocessHelper$")
	cmd.Env = append(os.Environ(),
		"TELOS_CHECKPOINT_TEST_HELPER=1",
		"TELOS_CHECKPOINT_TEST_ROOT="+root,
		"TELOS_CHECKPOINT_TEST_READY="+readyPath,
	)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(readyPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker helper did not acquire lease: %s", output.String())
		}
		time.Sleep(5 * time.Millisecond)
	}

	status, inFlight, err := safePoint.prepare(context.Background(), testCheckpointOperationID)
	if err != nil || status != "timeout" || inFlight != 1 {
		t.Fatalf("prepare with worker process: status=%q in_flight=%d err=%v", status, inFlight, err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("expected killed worker helper to exit unsuccessfully")
	}
	cmd.Process = nil

	status, inFlight, err = safePoint.prepare(context.Background(), testCheckpointOperationID)
	if err != nil || status != "prepared" || inFlight != 0 {
		t.Fatalf("prepare after worker crash: status=%q in_flight=%d err=%v", status, inFlight, err)
	}
}

func TestCheckpointLeaseSubprocessHelper(t *testing.T) {
	if os.Getenv("TELOS_CHECKPOINT_TEST_HELPER") != "1" {
		return
	}
	safePoint, err := newCheckpointSafePoint(os.Getenv("TELOS_CHECKPOINT_TEST_ROOT"), testCheckpointSessionID)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := safePoint.acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer lease.release()
	if err := os.WriteFile(os.Getenv("TELOS_CHECKPOINT_TEST_READY"), []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func TestCheckpointPrepareAtomicallyClosesConcurrentAdmission(t *testing.T) {
	safePoint := newTestCheckpointSafePoint(t)
	safePoint.prepareTimeout = time.Second
	lease, err := safePoint.acquire()
	if err != nil {
		t.Fatal(err)
	}

	prepared := make(chan error, 1)
	go func() {
		status, _, err := safePoint.prepare(context.Background(), testCheckpointOperationID)
		if err == nil && status != "prepared" {
			err = errors.New("prepare did not complete")
		}
		prepared <- err
	}()
	deadline := time.Now().Add(time.Second)
	for checkpointStatus(t, safePoint) != "preparing" {
		if time.Now().After(deadline) {
			t.Fatal("prepare did not close admission")
		}
		time.Sleep(time.Millisecond)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < cap(errs); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := safePoint.acquire()
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if !errors.Is(err, errCheckpointAdmissionClosed) {
			t.Fatalf("concurrent admission: got %v", err)
		}
	}
	lease.release()
	if err := <-prepared; err != nil {
		t.Fatal(err)
	}
}

func TestCheckpointWorkerCleanupRemainsInFlight(t *testing.T) {
	safePoint := newTestCheckpointSafePoint(t)
	lease, err := safePoint.acquire()
	if err != nil {
		t.Fatal(err)
	}
	cleanupStarted := make(chan struct{})
	cleanupRelease := make(chan struct{})
	cleanupDone := make(chan struct{})
	go func() {
		finishCheckpointWork(lease, func() {
			close(cleanupStarted)
			<-cleanupRelease
		})
		close(cleanupDone)
	}()
	<-cleanupStarted

	status, inFlight, err := safePoint.prepare(context.Background(), testCheckpointOperationID)
	if err != nil || status != "timeout" || inFlight != 1 {
		t.Fatalf("prepare during cleanup: status=%q in_flight=%d err=%v", status, inFlight, err)
	}
	close(cleanupRelease)
	select {
	case <-cleanupDone:
	case <-time.After(time.Second):
		t.Fatal("worker cleanup did not finish")
	}
	status, inFlight, err = safePoint.prepare(context.Background(), testCheckpointOperationID)
	if err != nil || status != "prepared" || inFlight != 0 {
		t.Fatalf("prepare after cleanup: status=%q in_flight=%d err=%v", status, inFlight, err)
	}
}

func TestCheckpointRootReconciliationRemainsInFlight(t *testing.T) {
	safePoint := newTestCheckpointSafePoint(t)
	workStarted := make(chan struct{})
	workRelease := make(chan struct{})
	workDone := make(chan error, 1)
	go func() {
		workDone <- withCheckpointLease(safePoint, func() error {
			close(workStarted)
			<-workRelease
			return nil
		})
	}()
	<-workStarted

	status, inFlight, err := safePoint.prepare(context.Background(), testCheckpointOperationID)
	if err != nil || status != "timeout" || inFlight != 1 {
		t.Fatalf("prepare during root reconciliation: status=%q in_flight=%d err=%v", status, inFlight, err)
	}
	close(workRelease)
	if err := <-workDone; err != nil {
		t.Fatal(err)
	}
	status, inFlight, err = safePoint.prepare(context.Background(), testCheckpointOperationID)
	if err != nil || status != "prepared" || inFlight != 0 {
		t.Fatalf("prepare after root reconciliation: status=%q in_flight=%d err=%v", status, inFlight, err)
	}
}

func TestCheckpointWorkerAdmissionWaitsForResumeAndCanStop(t *testing.T) {
	safePoint := newTestCheckpointSafePoint(t)
	if _, _, err := safePoint.prepare(context.Background(), testCheckpointOperationID); err != nil {
		t.Fatal(err)
	}
	wake := make(chan os.Signal, 1)
	stop := make(chan os.Signal, 1)
	type result struct {
		lease   *checkpointLease
		stopped bool
		err     error
	}
	resultCh := make(chan result, 1)
	go func() {
		lease, stopped, err := waitForCheckpointAdmission(safePoint, wake, stop)
		resultCh <- result{lease: lease, stopped: stopped, err: err}
	}()
	select {
	case got := <-resultCh:
		t.Fatalf("worker admitted while prepared: %+v", got)
	case <-time.After(20 * time.Millisecond):
	}
	if err := safePoint.resume(testCheckpointOperationID); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-resultCh:
		if got.err != nil || got.stopped || got.lease == nil {
			t.Fatalf("worker admission after resume: %+v", got)
		}
		got.lease.release()
	case <-time.After(time.Second):
		t.Fatal("worker did not resume admission")
	}

	if _, _, err := safePoint.prepare(context.Background(), testCheckpointOperationB); err != nil {
		t.Fatal(err)
	}
	stop <- os.Interrupt
	lease, stopped, err := waitForCheckpointAdmission(safePoint, wake, stop)
	if err != nil || !stopped || lease != nil {
		t.Fatalf("stopped worker wait: lease=%v stopped=%v err=%v", lease, stopped, err)
	}
}

func TestCheckpointRetryTimerRaceIsBounded(t *testing.T) {
	const iterations = 5000
	done := make(chan error, 1)
	go func() {
		for i := 0; i < iterations; i++ {
			wake := make(chan os.Signal, 1)
			wake <- syscall.SIGUSR1
			if waitForCheckpointRetry(0, wake, nil) {
				done <- errors.New("wake was treated as stop")
				return
			}
		}
		done <- nil
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timer/wake race blocked while draining a stopped Go 1.26 timer")
	}

	stop := make(chan os.Signal, 1)
	stop <- os.Interrupt
	if !waitForCheckpointRetry(time.Hour, nil, stop) {
		t.Fatal("stop signal was not observed")
	}
}

type blockingCheckpointStore struct {
	sessionapi.Store
	createStarted chan struct{}
	createRelease chan struct{}
	updateStarted chan struct{}
	updateRelease chan struct{}
	stopStarted   chan struct{}
	stopRelease   chan struct{}
}

func (s *blockingCheckpointStore) Create(sessionapi.SessionCreateRequest) (*sessionapi.Session, error) {
	close(s.createStarted)
	<-s.createRelease
	return &sessionapi.Session{SessionID: "sess_created"}, nil
}

func (s *blockingCheckpointStore) UpdateSpec(string, sessionapi.SessionSpecUpdateRequest) (*sessionapi.SessionSpecUpdateResponse, error) {
	close(s.updateStarted)
	<-s.updateRelease
	return &sessionapi.SessionSpecUpdateResponse{}, nil
}

func (s *blockingCheckpointStore) Stop(string) (*sessionapi.Session, error) {
	close(s.stopStarted)
	<-s.stopRelease
	return &sessionapi.Session{SessionID: "sess_stopped"}, nil
}

func TestCheckpointStoreCountsMutationsAsInFlight(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(checkpointStore, *blockingCheckpointStore) <-chan error
		wait func(*blockingCheckpointStore)
		done func(*blockingCheckpointStore)
	}{
		{
			name: "create",
			run: func(store checkpointStore, base *blockingCheckpointStore) <-chan error {
				result := make(chan error, 1)
				go func() { _, err := store.Create(sessionapi.SessionCreateRequest{}); result <- err }()
				return result
			},
			wait: func(base *blockingCheckpointStore) { <-base.createStarted },
			done: func(base *blockingCheckpointStore) { close(base.createRelease) },
		},
		{
			name: "update",
			run: func(store checkpointStore, base *blockingCheckpointStore) <-chan error {
				result := make(chan error, 1)
				go func() { _, err := store.UpdateSpec("demo", sessionapi.SessionSpecUpdateRequest{}); result <- err }()
				return result
			},
			wait: func(base *blockingCheckpointStore) { <-base.updateStarted },
			done: func(base *blockingCheckpointStore) { close(base.updateRelease) },
		},
		{
			name: "stop",
			run: func(store checkpointStore, base *blockingCheckpointStore) <-chan error {
				result := make(chan error, 1)
				go func() { _, err := store.Stop("demo"); result <- err }()
				return result
			},
			wait: func(base *blockingCheckpointStore) { <-base.stopStarted },
			done: func(base *blockingCheckpointStore) { close(base.stopRelease) },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			safePoint := newTestCheckpointSafePoint(t)
			base := &blockingCheckpointStore{
				createStarted: make(chan struct{}), createRelease: make(chan struct{}),
				updateStarted: make(chan struct{}), updateRelease: make(chan struct{}),
				stopStarted: make(chan struct{}), stopRelease: make(chan struct{}),
			}
			result := tc.run(checkpointStore{Store: base, safePoint: safePoint}, base)
			tc.wait(base)
			status, inFlight, err := safePoint.prepare(context.Background(), testCheckpointOperationID)
			if err != nil || status != "timeout" || inFlight != 1 {
				t.Fatalf("prepare: status=%q in_flight=%d err=%v", status, inFlight, err)
			}
			tc.done(base)
			if err := <-result; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCheckpointRoutesRequireOperatorAndExactRuntimeSession(t *testing.T) {
	safePoint := newTestCheckpointSafePoint(t)
	store := sessionapi.NewFileStore(filepath.Join(t.TempDir(), "sessions"), sessionapi.RuntimeCloud)
	access, err := sessionapi.NewScopedToken("sess_child", sessionapi.KindTask)
	if err != nil {
		t.Fatal(err)
	}
	childDir := filepath.Join(store.Root, "sess_child")
	if err := os.MkdirAll(childDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := sessionapi.Manifest{SessionID: "sess_child", SessionKind: sessionapi.KindTask, Access: access}
	if err := sessionapi.WriteManifest(filepath.Join(childDir, "session.json"), &manifest); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	registerCheckpointRoutes(mux, safePoint, sessionapi.NewBearerAuthorizer(store, "operator-secret"))

	tests := []struct {
		name      string
		token     string
		sessionID string
		want      int
		wantCode  string
	}{
		{name: "missing auth before scope check", sessionID: "sess_other", want: http.StatusUnauthorized, wantCode: "unauthorized"},
		{name: "invalid token", token: "wrong", sessionID: testCheckpointSessionID, want: http.StatusUnauthorized, wantCode: "unauthorized"},
		{name: "task token", token: access.APIToken, sessionID: testCheckpointSessionID, want: http.StatusForbidden, wantCode: "forbidden"},
		{name: "wrong runtime session", token: "operator-secret", sessionID: "sess_other", want: http.StatusConflict, wantCode: "checkpoint_session_mismatch"},
		{name: "operator", token: "operator-secret", sessionID: testCheckpointSessionID, want: http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(checkpointRequest{
				SessionID:   tc.sessionID,
				OperationID: testCheckpointOperationID,
			})
			req := httptest.NewRequest(http.MethodPost, "/internal/checkpoint/prepare", bytes.NewReader(body))
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			res := httptest.NewRecorder()
			mux.ServeHTTP(res, req)
			if res.Code != tc.want {
				t.Fatalf("status: got %d want %d body=%s", res.Code, tc.want, res.Body.String())
			}
			if tc.wantCode != "" {
				var body struct {
					Error struct {
						Code      string `json:"code"`
						Message   string `json:"message"`
						Retryable bool   `json:"retryable"`
					} `json:"error"`
				}
				if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if body.Error.Code != tc.wantCode || body.Error.Message == "" || body.Error.Retryable {
					t.Fatalf("typed error: %#v", body.Error)
				}
			} else {
				var body checkpointResponse
				if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if body.Status != "prepared" || body.SessionID != testCheckpointSessionID ||
					body.OperationID != testCheckpointOperationID || body.InFlight != 0 {
					t.Fatalf("prepare response: %#v", body)
				}
			}
		})
	}

	for i := 0; i < 2; i++ {
		body, _ := json.Marshal(checkpointRequest{
			SessionID:   testCheckpointSessionID,
			OperationID: testCheckpointOperationID,
		})
		req := httptest.NewRequest(http.MethodPost, "/internal/checkpoint/resume", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer operator-secret")
		res := httptest.NewRecorder()
		mux.ServeHTTP(res, req)
		if res.Code != http.StatusOK {
			t.Fatalf("resume %d: status=%d body=%s", i, res.Code, res.Body.String())
		}
		var response checkpointResponse
		if err := json.Unmarshal(res.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Status != "resumed" || response.SessionID != testCheckpointSessionID ||
			response.OperationID != testCheckpointOperationID {
			t.Fatalf("resume response %d: %#v", i, response)
		}
	}
}

func TestCheckpointRouteRejectsStaleResumeWithoutReopening(t *testing.T) {
	safePoint := newTestCheckpointSafePoint(t)
	if _, _, err := safePoint.prepare(context.Background(), testCheckpointOperationID); err != nil {
		t.Fatal(err)
	}
	if err := safePoint.resume(testCheckpointOperationID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := safePoint.prepare(context.Background(), testCheckpointOperationB); err != nil {
		t.Fatal(err)
	}
	store := sessionapi.NewFileStore(filepath.Join(t.TempDir(), "sessions"), sessionapi.RuntimeCloud)
	mux := http.NewServeMux()
	registerCheckpointRoutes(mux, safePoint, sessionapi.NewBearerAuthorizer(store, "operator-secret"))
	body, _ := json.Marshal(checkpointRequest{
		SessionID:   testCheckpointSessionID,
		OperationID: testCheckpointOperationID,
	})
	request := httptest.NewRequest(http.MethodPost, "/internal/checkpoint/resume", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer operator-secret")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)

	if response.Code != http.StatusConflict {
		t.Fatalf("stale resume status=%d body=%s", response.Code, response.Body.String())
	}
	var failure struct {
		Error struct {
			Code      string `json:"code"`
			Retryable bool   `json:"retryable"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil {
		t.Fatal(err)
	}
	if failure.Error.Code != "checkpoint_operation_mismatch" || failure.Error.Retryable {
		t.Fatalf("stale resume failure: %#v", failure.Error)
	}
	state := checkpointStateValue(t, safePoint)
	if state.Status != "prepared" || state.OperationID != testCheckpointOperationB {
		t.Fatalf("stale route resume changed state: %#v", state)
	}
}

func TestCheckpointPrepareTimeoutIsTypedAndRetryable(t *testing.T) {
	safePoint := newTestCheckpointSafePoint(t)
	lease, err := safePoint.acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer lease.release()

	store := sessionapi.NewFileStore(filepath.Join(t.TempDir(), "sessions"), sessionapi.RuntimeCloud)
	mux := http.NewServeMux()
	registerCheckpointRoutes(mux, safePoint, sessionapi.NewBearerAuthorizer(store, "operator-secret"))
	body, _ := json.Marshal(checkpointRequest{
		SessionID:   testCheckpointSessionID,
		OperationID: testCheckpointOperationID,
	})
	req := httptest.NewRequest(http.MethodPost, "/internal/checkpoint/prepare", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer operator-secret")
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, req)

	if res.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var response struct {
		Error struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			Retryable bool   `json:"retryable"`
		} `json:"error"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error.Code != "checkpoint_prepare_timeout" ||
		!response.Error.Retryable || response.Error.Message == "" {
		t.Fatalf("typed timeout error: %#v", response.Error)
	}
	if got := checkpointStatus(t, safePoint); got != "preparing" {
		t.Fatalf("timeout reopened checkpoint admission: %q", got)
	}
}

func TestCheckpointSafePointCapabilityRequiresCloudClaimAndPersistence(t *testing.T) {
	t.Setenv("TELOS_SESSION_ID", "")
	local, err := checkpointSafePointForConfig(Config{Mode: ModeLocal, Root: t.TempDir()})
	if err != nil || local != nil {
		t.Fatalf("local safe point: manager=%v err=%v", local, err)
	}
	cloud, err := checkpointSafePointForConfig(Config{Mode: ModeCloud, Root: t.TempDir()})
	if err != nil || cloud != nil {
		t.Fatalf("unclaimed cloud safe point: manager=%v err=%v", cloud, err)
	}
	t.Setenv("TELOS_SESSION_ID", testCheckpointSessionID)
	cloud, err = checkpointSafePointForConfig(Config{Mode: ModeCloud, Root: t.TempDir()})
	if err != nil || cloud == nil {
		t.Fatalf("claimed cloud safe point: manager=%v err=%v", cloud, err)
	}
	if _, err := os.Stat(cloud.statePath()); err != nil {
		t.Fatalf("durable open state was not initialized: %v", err)
	}
	capabilities := setRuntimeCapability(
		[]string{"existing", checkpointSafePointCapability},
		checkpointSafePointCapability,
		true,
	)
	if len(capabilities) != 2 || capabilities[1] != checkpointSafePointCapability {
		t.Fatalf("capabilities: %#v", capabilities)
	}
	disabled := setRuntimeCapability(capabilities, checkpointSafePointCapability, false)
	if len(disabled) != 1 || disabled[0] != "existing" {
		t.Fatalf("disabled capabilities: %#v", disabled)
	}
}

func TestCheckpointCapabilityUsesCanonicalHealthPayload(t *testing.T) {
	store := sessionapi.NewFileStore(filepath.Join(t.TempDir(), "sessions"), sessionapi.RuntimeCloud)
	mux := http.NewServeMux()
	runtime := sessionapi.RuntimeIdentity{
		Version:      "v0.1.4",
		TelosdDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Capabilities: setRuntimeCapability(
			[]string{sessionapi.CapabilityEpochFinalizedEventsV1},
			checkpointSafePointCapability,
			true,
		),
	}
	sessionapi.RegisterRoutes(mux, store, sessionapi.AllowAllAuthorizer{}, runtime)
	request := httptest.NewRequest(http.MethodGet, "/api/healthz", nil)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)

	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	capabilities, ok := body["capabilities"].([]any)
	if !ok || len(capabilities) != 2 ||
		capabilities[0] != sessionapi.CapabilityEpochFinalizedEventsV1 ||
		capabilities[1] != sessionapi.CapabilityCheckpointSafePoint {
		t.Fatalf("canonical capabilities: %#v", body["capabilities"])
	}
	if _, stale := body["runtime_capabilities"]; stale {
		t.Fatalf("health payload revived stale runtime_capabilities key: %#v", body)
	}
}

func TestCheckpointSafePointRejectsMismatchedPersistedClaim(t *testing.T) {
	root := t.TempDir()
	safePoint, err := newCheckpointSafePoint(root, testCheckpointSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := safePoint.prepare(context.Background(), testCheckpointOperationID); err != nil {
		t.Fatal(err)
	}
	if _, err := newCheckpointSafePoint(root, "sess_different"); err == nil {
		t.Fatal("expected persisted deployment claim mismatch to fail closed")
	}
}

func TestCheckpointStateLossFailsAdmissionClosed(t *testing.T) {
	safePoint := newTestCheckpointSafePoint(t)
	if err := os.Remove(safePoint.statePath()); err != nil {
		t.Fatal(err)
	}
	if _, err := safePoint.acquire(); err == nil {
		t.Fatal("expected missing durable state to refuse admission")
	}
	status, _, err := safePoint.prepare(context.Background(), testCheckpointOperationID)
	if err == nil || status != "error" {
		t.Fatalf("prepare with missing durable state: status=%q err=%v", status, err)
	}
}

func TestCheckpointStateLossFailsClosedAcrossRestart(t *testing.T) {
	root := t.TempDir()
	safePoint, err := newCheckpointSafePoint(root, testCheckpointSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(safePoint.statePath()); err != nil {
		t.Fatal(err)
	}
	if _, err := newCheckpointSafePoint(root, testCheckpointSessionID); err == nil {
		t.Fatal("restart recreated missing checkpoint state and reopened admission")
	}
}

func TestCheckpointInitializationMarkerSurvivesSubdirectoryLoss(t *testing.T) {
	root := t.TempDir()
	safePoint, err := newCheckpointSafePoint(root, testCheckpointSessionID)
	if err != nil {
		t.Fatal(err)
	}
	markerInfo, err := os.Lstat(safePoint.markerPath())
	if err != nil {
		t.Fatalf("initialization marker: %v", err)
	}
	if !markerInfo.Mode().IsRegular() || filepath.Dir(safePoint.markerPath()) != root {
		t.Fatalf("initialization marker is not a regular file outside state dir: %s (%s)", safePoint.markerPath(), markerInfo.Mode())
	}
	if err := os.RemoveAll(safePoint.root); err != nil {
		t.Fatal(err)
	}
	if _, err := newCheckpointSafePoint(root, testCheckpointSessionID); err == nil {
		t.Fatal("missing checkpoint directory was recreated despite durable marker")
	}
}

func TestCheckpointPartialInitializationFailsClosed(t *testing.T) {
	root := t.TempDir()
	safePoint, err := newCheckpointSafePoint(root, testCheckpointSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(safePoint.markerPath()); err != nil {
		t.Fatal(err)
	}
	if _, err := newCheckpointSafePoint(root, testCheckpointSessionID); err == nil {
		t.Fatal("existing state directory without marker was treated as fresh initialization")
	}
}

func TestCheckpointMalformedStateFailsClosedAcrossRestart(t *testing.T) {
	root := t.TempDir()
	safePoint, err := newCheckpointSafePoint(root, testCheckpointSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(safePoint.statePath(), []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newCheckpointSafePoint(root, testCheckpointSessionID); err == nil {
		t.Fatal("malformed state was accepted across restart")
	}
}

func TestCheckpointStorageRejectsSymlinks(t *testing.T) {
	for _, name := range []string{"marker", "state", "lock", "directory"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			safePoint, err := newCheckpointSafePoint(root, testCheckpointSessionID)
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(root, "untrusted-target")
			if name == "directory" {
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.RemoveAll(safePoint.root); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, safePoint.root); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(target, []byte("untrusted"), 0o600); err != nil {
					t.Fatal(err)
				}
				path := safePoint.statePath()
				if name == "marker" {
					path = safePoint.markerPath()
				} else if name == "lock" {
					path = safePoint.lockPath()
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := newCheckpointSafePoint(root, testCheckpointSessionID); err == nil {
				t.Fatalf("%s symlink was accepted", name)
			}
		})
	}
}

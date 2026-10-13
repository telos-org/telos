package sessionapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func blockedSession(t *testing.T, kind SessionKind) (*FileStore, *Session) {
	t.Helper()
	store := NewFileStore(t.TempDir(), RuntimeCloud)
	markdown := "---\nversion: 0.1.0\nname: recovery-test\nplatform: cloud\n---\n# Recovery\n"
	session, err := store.Create(SessionCreateRequest{SpecMarkdown: &markdown, SessionKind: &kind})
	if err != nil {
		t.Fatal(err)
	}
	_, err = MutateManifest(store.manifestPath(session.SessionID), func(m *Manifest) error {
		m.Epochs = append(m.Epochs, NewEpoch(m, 1, tsNow(), nil))
		m.Recovery = &SessionRecovery{EpochID: 1, Error: "saved progress is corrupt; operator restart required"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return store, session
}

func TestRecoveryOverridesLiveWorkerAndNewerDesiredSpec(t *testing.T) {
	store, session := blockedSession(t, KindController)
	lock, err := os.OpenFile(filepath.Join(*session.SessionDir, "runner.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	_, err = MutateManifest(store.manifestPath(session.SessionID), func(m *Manifest) error {
		m.Runner = &Runner{PID: os.Getpid()}
		version := 2
		m.CurrentSpecVersion = &version
		m.PackageDigest = strPtr("sha256:new-desired")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(*session.Specs[0].EvidencePath, []byte("{\"event\":\"round_start\",\"round\":9,\"role\":\"verifier\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	current, err := store.Get(session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != StatusFailed || current.Recovery == nil || ptrOr(current.Error, "") != current.Recovery.Error || current.CurrentRole != nil || current.CurrentRound != nil {
		t.Fatalf("blocked state hidden by live runner: %+v", current)
	}
	if got := current.Reconciliation; got == nil || got.State != ReconciliationFailed || got.PackageDigest != "sha256:new-desired" || got.Error != current.Recovery.Error {
		t.Fatalf("blocked state hidden by newer desired spec: %+v", got)
	}
	items := SessionListItems([]Session{*current})
	if items[0].Recovery == nil || items[0].AsSession().Recovery == nil {
		t.Fatal("list contract discarded recovery")
	}
	workers, err := store.ListRootWorkerSessions()
	if err != nil || len(workers) != 0 {
		t.Fatalf("blocked root should not be supervised: %+v, %v", workers, err)
	}
	markdown := "---\nversion: 0.1.0\nname: recovery-test\nplatform: cloud\n---\n# Recovery\n"
	if _, err := store.Create(SessionCreateRequest{SpecMarkdown: &markdown}); !errors.Is(err, ErrConflict) {
		t.Fatalf("blocked root allowed replacement: %v", err)
	}
}

func TestRestartHTTPRequiresOperatorAndAcknowledgedEpoch(t *testing.T) {
	store, session := blockedSession(t, KindController)
	manifest, err := ReadManifest(store.manifestPath(session.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	RegisterRoutes(mux, store, NewBearerAuthorizer(store, "operator-token"), RuntimeIdentity{})
	request := func(token, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/api/sessions/"+session.SessionID+"/restart", strings.NewReader(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("restart returned %d, want %d: %s", w.Code, status, w.Body.String())
		}
		return w
	}
	request("", `{"epoch_id":1}`, http.StatusUnauthorized)
	request(manifest.Access.APIToken, `{"epoch_id":1}`, http.StatusForbidden)
	for _, body := range []string{`{}`, `{"epoch_id":1.5}`, `{"epoch_id":1,"reset":true}`, `{"epoch_id":1} {}`} {
		request("operator-token", body, http.StatusBadRequest)
	}
	request("operator-token", `{"epoch_id":2}`, http.StatusConflict)
	for _, operation := range []string{"restart_requested", "unchanged"} {
		w := request("operator-token", `{"epoch_id":1}`, http.StatusOK)
		var response SessionRestartResponse
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Operation != operation || response.Session.Recovery == nil || !response.Session.Recovery.RestartRequested {
			t.Fatalf("restart result = %+v", response)
		}
	}
	manifest, err = ReadManifest(store.manifestPath(session.SessionID))
	if err != nil || len(manifest.Epochs) != 1 || manifest.OpenEpoch().ID != 1 {
		t.Fatalf("API must leave handoff to worker ownership: %+v, %v", manifest, err)
	}
	workers, err := store.ListRootWorkerSessions()
	if err != nil || len(workers) != 1 {
		t.Fatalf("durable requested restart should be supervised: %+v, %v", workers, err)
	}
	_, err = MutateManifest(store.manifestPath(session.SessionID), func(m *Manifest) error {
		m.Epochs[0].FinishedAt = strPtr(tsNow())
		m.Epochs[0].CompletionReason = strPtr("recovery_restart")
		m.Epochs = append(m.Epochs, NewEpoch(m, 2, tsNow(), nil))
		m.Recovery = &SessionRecovery{EpochID: 2, Error: "different blocked attempt"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	w := request("operator-token", `{"epoch_id":1}`, http.StatusOK)
	var response SessionRestartResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Operation != "unchanged" || response.Session.Recovery.EpochID != 2 || response.Session.Recovery.RestartRequested {
		t.Fatalf("old acknowledgment reset newer blocked attempt: %+v", response)
	}
}

func TestBlockedTaskCanBeStoppedAndCannotBeRestarted(t *testing.T) {
	store, session := blockedSession(t, KindTask)
	manifest, err := ReadManifest(store.manifestPath(session.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	RegisterRoutes(mux, store, NewBearerAuthorizer(store, "operator-token"), RuntimeIdentity{})
	r := httptest.NewRequest(http.MethodPost, "/api/sessions/"+session.SessionID+"/stop", nil)
	r.Header.Set("Authorization", "Bearer "+manifest.Access.APIToken)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("scoped task stop: %d %s", w.Code, w.Body.String())
	}
	stopped, err := store.Get(session.SessionID)
	if err != nil || stopped.Status != StatusStopped || ptrOr(stopped.Error, "") != "stopped by operator" {
		t.Fatalf("stopped task = %+v, %v", stopped, err)
	}
	manifest, err = ReadManifest(store.manifestPath(session.SessionID))
	if err != nil || !manifest.IsStopped() || manifest.OpenEpoch() != nil {
		t.Fatalf("stop did not seal blocked attempt: %+v, %v", manifest, err)
	}
	if _, err := store.Restart(session.SessionID, SessionRestartRequest{EpochID: 1}); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("restart ignored operator stop: %v", err)
	}
}

func TestRestartRejectsIneligibleSessions(t *testing.T) {
	for _, scenario := range []string{"task", "child controller", "stopped", "healthy"} {
		t.Run(scenario, func(t *testing.T) {
			store, session := blockedSession(t, KindController)
			_, err := MutateManifest(store.manifestPath(session.SessionID), func(m *Manifest) error {
				switch scenario {
				case "task":
					m.SessionKind = KindTask
				case "child controller":
					m.ParentSessionID = strPtr("sess-parent")
				case "stopped":
					m.DesiredStatus = DesiredStatusStopped
				case "healthy":
					m.Recovery = nil
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(store.manifestPath(session.SessionID))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.Restart(session.SessionID, SessionRestartRequest{EpochID: 1}); err == nil {
				t.Fatal("ineligible session accepted restart")
			}
			after, err := os.ReadFile(store.manifestPath(session.SessionID))
			if err != nil || string(after) != string(before) {
				t.Fatalf("rejected restart changed session: %v", err)
			}
		})
	}
}

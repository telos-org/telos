package telosd

import (
	"errors"
	"testing"

	"github.com/telos-org/telos/internal/sessionapi"
)

type recordingSubstrate struct {
	applies  []recordedApply
	stops    []string
	applyErr error
	stopErr  error
}

type recordedApply struct {
	sessionID  string
	wakeReason string
}

func (s *recordingSubstrate) Apply(session *sessionapi.Session, wakeReason string) error {
	s.applies = append(s.applies, recordedApply{sessionID: session.SessionID, wakeReason: wakeReason})
	if s.applyErr != nil {
		return s.applyErr
	}
	return nil
}

func (s *recordingSubstrate) Stop(session *sessionapi.Session) error {
	s.stops = append(s.stops, session.SessionID)
	if s.stopErr != nil {
		return s.stopErr
	}
	return nil
}

func TestCloudSessionStoreAppliesAndStopsWorkers(t *testing.T) {
	base := sessionapi.NewFileStore(t.TempDir(), sessionapi.RuntimeCloud)
	substrate := &recordingSubstrate{}
	store := newCloudSessionStore(base, substrate)
	markdown := "---\nversion: v0\nname: postgres\nplatform: cloud\n---\n# Postgres\n"

	session, err := store.Create(sessionapi.SessionCreateRequest{SpecMarkdown: &markdown})
	if err != nil {
		t.Fatal(err)
	}
	if len(substrate.applies) != 1 {
		t.Fatalf("applies: got %d", len(substrate.applies))
	}
	if substrate.applies[0].sessionID != session.SessionID || substrate.applies[0].wakeReason != "controller_started" {
		t.Fatalf("create apply: got %+v", substrate.applies[0])
	}
	assertManagedSessionDefaults(t, session)

	updated := "---\nversion: v0\nname: postgres\nplatform: cloud\ninterval: 5m\n---\n# Postgres\n"
	if _, err := store.UpdateSpec("postgres", sessionapi.SessionSpecUpdateRequest{SpecMarkdown: updated}); err != nil {
		t.Fatal(err)
	}
	if len(substrate.applies) != 2 {
		t.Fatalf("applies after update: got %d", len(substrate.applies))
	}
	if substrate.applies[1].sessionID != session.SessionID || substrate.applies[1].wakeReason != "spec_updated" {
		t.Fatalf("update apply: got %+v", substrate.applies[1])
	}

	if _, err := store.Stop(session.SessionID); err != nil {
		t.Fatal(err)
	}
	if len(substrate.stops) != 1 || substrate.stops[0] != session.SessionID {
		t.Fatalf("stops: got %+v", substrate.stops)
	}
}

func TestCloudSessionStoreDefaultsSpecPutCreatedSessions(t *testing.T) {
	base := sessionapi.NewFileStore(t.TempDir(), sessionapi.RuntimeCloud)
	substrate := &recordingSubstrate{}
	store := newCloudSessionStore(base, substrate)
	markdown := "---\nversion: v0\nname: postgres\nplatform: cloud\n---\n# Postgres\n"

	response, err := store.UpdateSpec("postgres", sessionapi.SessionSpecUpdateRequest{SpecMarkdown: markdown})
	if err != nil {
		t.Fatal(err)
	}
	if response.Operation != "created" {
		t.Fatalf("operation = %q want created", response.Operation)
	}
	if response.Session == nil {
		t.Fatal("expected created session")
	}
	assertManagedSessionDefaults(t, response.Session)
	if len(substrate.applies) != 1 {
		t.Fatalf("applies: got %d", len(substrate.applies))
	}
	if substrate.applies[0].wakeReason != "controller_started" {
		t.Fatalf("wake reason: got %+v", substrate.applies[0])
	}
}

func TestCloudSessionStoreRemovesSessionWhenWorkerApplyFails(t *testing.T) {
	base := sessionapi.NewFileStore(t.TempDir(), sessionapi.RuntimeCloud)
	substrate := &recordingSubstrate{applyErr: errors.New("worker launch failed")}
	store := newCloudSessionStore(base, substrate)
	markdown := "---\nversion: v0\nname: postgres\nplatform: cloud\n---\n# Postgres\n"

	_, err := store.Create(sessionapi.SessionCreateRequest{SpecMarkdown: &markdown})
	if err == nil {
		t.Fatal("expected worker launch error")
	}
	sessions, listErr := base.List()
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(sessions) != 0 {
		t.Fatalf("orphan sessions: got %+v", sessions)
	}
	if len(substrate.stops) != 1 {
		t.Fatalf("worker cleanup stops: got %+v", substrate.stops)
	}
}

func TestCloudSessionStoreLeavesFileStateRunningWhenWorkerStopFails(t *testing.T) {
	base := sessionapi.NewFileStore(t.TempDir(), sessionapi.RuntimeCloud)
	substrate := &recordingSubstrate{stopErr: errors.New("worker stop failed")}
	store := newCloudSessionStore(base, substrate)
	markdown := "---\nversion: v0\nname: postgres\nplatform: cloud\n---\n# Postgres\n"

	session, err := store.Create(sessionapi.SessionCreateRequest{SpecMarkdown: &markdown})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Stop(session.SessionID)
	if err == nil {
		t.Fatal("expected worker stop error")
	}
	current, err := base.Get(session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status == sessionapi.StatusStopped {
		t.Fatal("file state was marked stopped despite substrate stop failure")
	}
}

func assertManagedSessionDefaults(t *testing.T, session *sessionapi.Session) {
	t.Helper()
	if got, _ := session.Config["model"].(string); got != defaultCloudSessionModel {
		t.Fatalf("model = %q want %q", got, defaultCloudSessionModel)
	}
	got, ok := intConfigValue(session.Config, "agent_timeout_sec")
	if !ok || got != defaultCloudAgentTimeoutSec {
		t.Fatalf("agent_timeout_sec = %v want %d", session.Config["agent_timeout_sec"], defaultCloudAgentTimeoutSec)
	}
}

func intConfigValue(config map[string]any, key string) (int, bool) {
	switch value := config[key].(type) {
	case int:
		return value, true
	case float64:
		return int(value), value == float64(int(value))
	default:
		return 0, false
	}
}

package telosd

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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
	store := newCloudSessionStore(base, substrate, nil)
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
	store := newCloudSessionStore(base, substrate, nil)
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

func TestCloudSessionStoreSkipsApplyForUnchangedSpecPut(t *testing.T) {
	base := sessionapi.NewFileStore(t.TempDir(), sessionapi.RuntimeCloud)
	substrate := &recordingSubstrate{}
	store := newCloudSessionStore(base, substrate, nil)
	markdown := "---\nversion: v0\nname: postgres\nplatform: cloud\n---\n# Postgres\n"

	session, err := store.Create(sessionapi.SessionCreateRequest{SpecMarkdown: &markdown})
	if err != nil {
		t.Fatal(err)
	}
	if len(substrate.applies) != 1 {
		t.Fatalf("initial applies: got %d", len(substrate.applies))
	}
	response, err := store.UpdateSpec("postgres", sessionapi.SessionSpecUpdateRequest{SpecMarkdown: markdown})
	if err != nil {
		t.Fatal(err)
	}
	if response.Operation != "unchanged" {
		t.Fatalf("operation = %q want unchanged", response.Operation)
	}
	if response.Session == nil || response.Session.SessionID != session.SessionID {
		t.Fatalf("session: got %#v", response.Session)
	}
	if len(substrate.applies) != 1 {
		t.Fatalf("unchanged update should not apply worker: %+v", substrate.applies)
	}
}

func TestCloudSessionStoreMaterializesPackageDigestUpdates(t *testing.T) {
	base := sessionapi.NewFileStore(t.TempDir(), sessionapi.RuntimeCloud)
	base.PackageRoot = t.TempDir()
	substrate := &recordingSubstrate{}
	pkg := buildMaterializerTestPackage(t, "postgres")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer runtime-token" {
			t.Fatalf("Authorization = %q", got)
		}
		_, _ = w.Write(pkg.Bytes)
	}))
	defer server.Close()
	t.Setenv("TELOS_PACKAGE_BUNDLE_BASE_URL", server.URL)
	materializer := newApplyPackageMaterializer(base.PackageRoot, "runtime-token")
	materializer.client = server.Client()
	store := newCloudSessionStore(base, substrate, materializer)
	markdown := "---\nversion: v0\nname: postgres\nplatform: cloud\n---\n# Postgres\n"

	if _, err := store.Create(sessionapi.SessionCreateRequest{SpecMarkdown: &markdown}); err != nil {
		t.Fatal(err)
	}
	response, err := store.UpdateSpec("postgres", sessionapi.SessionSpecUpdateRequest{
		PackageDigest: pkg.Digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Session == nil || len(response.Session.SpecVersions) == 0 {
		t.Fatalf("updated session package digest: %#v", response.Session)
	}
	lastVersion := response.Session.SpecVersions[len(response.Session.SpecVersions)-1]
	if got := lastVersion["package_digest"]; got != pkg.Digest {
		t.Fatalf("updated session package digest: got %#v want %q", got, pkg.Digest)
	}
	path, err := sessionapi.PackagePathForDigest(base.PackageRoot, pkg.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if err := sessionapi.VerifyPackageDigest(path, pkg.Digest); err != nil {
		t.Fatalf("VerifyPackageDigest: %v", err)
	}
	if len(substrate.applies) != 2 || substrate.applies[1].wakeReason != "spec_updated" {
		t.Fatalf("applies: %+v", substrate.applies)
	}
}

func TestCloudSessionStoreProjectsSpecUpdates(t *testing.T) {
	base := sessionapi.NewFileStore(t.TempDir(), sessionapi.RuntimeCloud)
	substrate := &recordingSubstrate{}
	store := newCloudSessionStore(base, substrate, nil)
	markdown := "---\nversion: v0\nname: postgres\nplatform: cloud\n---\n# Postgres\n"
	session, err := store.Create(sessionapi.SessionCreateRequest{SpecMarkdown: &markdown})
	if err != nil {
		t.Fatal(err)
	}
	updated := "---\nversion: v0\nname: postgres\nplatform: cloud\ninterval: 5m\n---\n# Postgres v2\n"

	if _, err := store.UpdateSpec("postgres", sessionapi.SessionSpecUpdateRequest{SpecMarkdown: updated}); err != nil {
		t.Fatal(err)
	}

	transcript, err := store.Transcript(session.SessionID)
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	for _, want := range []string{
		"## External Update",
		"<external_update>",
		"from version 1 to 2",
		"Current spec path: `",
	} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("transcript missing %q:\n%s", want, transcript)
		}
	}
	events, err := store.Events(session.SessionID)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var found bool
	for _, event := range events {
		if event.Event == "external_update" {
			found = true
			if got := event.Data["current_spec_version"]; got != float64(2) {
				t.Fatalf("current_spec_version: got %#v", got)
			}
		}
	}
	if !found {
		t.Fatalf("missing external_update event: %#v", events)
	}
}

func TestCloudSessionStoreRemovesSessionWhenWorkerApplyFails(t *testing.T) {
	base := sessionapi.NewFileStore(t.TempDir(), sessionapi.RuntimeCloud)
	substrate := &recordingSubstrate{applyErr: errors.New("worker launch failed")}
	store := newCloudSessionStore(base, substrate, nil)
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
	store := newCloudSessionStore(base, substrate, nil)
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
	if got, _ := session.Config["thinking"].(string); got != defaultCloudSessionThinking {
		t.Fatalf("thinking = %q want %q", got, defaultCloudSessionThinking)
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

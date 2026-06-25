package telosd

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

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
	store := newCloudSessionStore(base, routeHandleResolver{}, substrate)
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

	updated := "---\nversion: v0\nname: postgres\nplatform: cloud\ninterval: 5m\n---\n# Postgres\n"
	if _, err := store.UpdateSpec(session.SessionID, sessionapi.SessionSpecUpdateRequest{SpecMarkdown: updated}); err != nil {
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

func TestCloudSessionStoreRemovesSessionWhenWorkerApplyFails(t *testing.T) {
	base := sessionapi.NewFileStore(t.TempDir(), sessionapi.RuntimeCloud)
	substrate := &recordingSubstrate{applyErr: errors.New("worker launch failed")}
	store := newCloudSessionStore(base, routeHandleResolver{}, substrate)
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
	store := newCloudSessionStore(base, routeHandleResolver{}, substrate)
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

func TestCloudSessionStoreAddsHTTPProductHandle(t *testing.T) {
	base := sessionapi.NewFileStore(t.TempDir(), sessionapi.RuntimeCloud)
	store := newCloudSessionStore(base, routeHandleResolver{
		read: func(context.Context) ([]publicRoute, error) {
			return []publicRoute{
				{
					Namespace: "ns-postgres",
					Data: map[string]string{
						"protocol":       "tcp",
						"product_handle": "postgres-db.usetelos.ai",
					},
				},
				{
					Namespace: "ns-postgres",
					Data: map[string]string{
						"protocol":       "http",
						"product_handle": "postgres.usetelos.ai",
					},
				},
			}, nil
		},
	}, nil)
	markdown := "---\nversion: v0\nname: postgres\nplatform: cloud\n---\n# Postgres\n"

	session, err := store.Create(sessionapi.SessionCreateRequest{SpecMarkdown: &markdown})
	if err != nil {
		t.Fatal(err)
	}
	if session.ArtifactURI == nil || *session.ArtifactURI != "https://postgres.usetelos.ai" {
		t.Fatalf("artifact_uri: got %#v", session.ArtifactURI)
	}
}

func TestCloudSessionStoreAddsDashboardURL(t *testing.T) {
	base := sessionapi.NewFileStore(t.TempDir(), sessionapi.RuntimeCloud)
	store := newCloudSessionStore(base, routeHandleResolver{
		read: func(context.Context) ([]publicRoute, error) {
			return []publicRoute{
				{
					Namespace: "ns-auth",
					Data: map[string]string{
						"type":           "service",
						"product_handle": "auth-service.usetelos.ai",
					},
				},
				{
					Namespace: "ns-auth",
					Data: map[string]string{
						"type":           "dashboard",
						"product_handle": "dashboard-auth.usetelos.ai",
					},
				},
			}, nil
		},
	}, nil)
	markdown := "---\nversion: v0\nname: auth\nplatform: cloud\n---\n# Auth\n"

	session, err := store.Create(sessionapi.SessionCreateRequest{SpecMarkdown: &markdown})
	if err != nil {
		t.Fatal(err)
	}
	if session.ArtifactURI == nil || *session.ArtifactURI != "https://auth-service.usetelos.ai" {
		t.Fatalf("artifact_uri: got %#v", session.ArtifactURI)
	}
	if session.DashboardURL == nil || *session.DashboardURL != "https://dashboard-auth.usetelos.ai" {
		t.Fatalf("dashboard_url: got %#v", session.DashboardURL)
	}
}

func TestCloudSessionStoreAddsTerminalProductHandle(t *testing.T) {
	base := sessionapi.NewFileStore(t.TempDir(), sessionapi.RuntimeCloud)
	store := newCloudSessionStore(base, routeHandleResolver{
		read: func(context.Context) ([]publicRoute, error) {
			return []publicRoute{
				{
					Namespace: "ns-postgres",
					Data:      map[string]string{"product_handle": "postgres.usetelos.ai"},
				},
			}, nil
		},
	}, nil)
	name := "postgres"
	kind := sessionapi.KindController
	session := sessionapi.Session{
		SessionID:   "sess_1",
		SessionKind: &kind,
		SpecName:    &name,
		Status:      sessionapi.StatusCompleted,
	}

	store.enrich(&session, store.routes())

	if session.ArtifactURI == nil || *session.ArtifactURI != "https://postgres.usetelos.ai" {
		t.Fatalf("artifact_uri: got %#v", session.ArtifactURI)
	}
}

func TestCloudSessionStoreLeavesTaskHandleEmpty(t *testing.T) {
	base := sessionapi.NewFileStore(t.TempDir(), sessionapi.RuntimeCloud)
	store := newCloudSessionStore(base, routeHandleResolver{
		read: func(context.Context) ([]publicRoute, error) {
			return []publicRoute{
				{
					Namespace: "ns-postgres",
					Data:      map[string]string{"product_handle": "postgres.usetelos.ai"},
				},
			}, nil
		},
	}, nil)
	parentID := "sess_controller"
	markdown := "---\nversion: v0\nname: postgres\nplatform: cloud\n---\n# Postgres\n"

	session, err := store.Create(sessionapi.SessionCreateRequest{
		SpecMarkdown:    &markdown,
		ParentSessionID: &parentID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if session.SessionKind == nil || *session.SessionKind != sessionapi.KindTask {
		t.Fatalf("session kind: got %#v", session.SessionKind)
	}
	if session.ArtifactURI != nil {
		t.Fatalf("task session got artifact_uri: %q", *session.ArtifactURI)
	}
}

func TestRouteHandleResolverMatchesRouteMetadata(t *testing.T) {
	sessionID := "sess_controller"
	name := "postgres"
	kind := sessionapi.KindController
	resolver := routeHandleResolver{
		read: func(context.Context) ([]publicRoute, error) {
			return []publicRoute{
				{
					Namespace: "ns-other",
					Labels:    map[string]string{"telos.ai/controller": sessionID},
					Data:      map[string]string{"hostname": "postgres.usetelos.ai"},
				},
			}, nil
		},
	}
	routes, err := resolver.Routes(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	handle := productHandleFor(routes, sessionapi.Session{
		SessionID:   sessionID,
		SessionKind: &kind,
		SpecName:    &name,
		Status:      sessionapi.StatusRunning,
	})

	if handle != "postgres.usetelos.ai" {
		t.Fatalf("handle: got %q", handle)
	}
}

func TestProductHandleRequiresUnambiguousBrowserRoute(t *testing.T) {
	name := "auth"
	kind := sessionapi.KindController
	handle := productHandleFor(
		[]publicRoute{
			{
				Namespace: "ns-auth",
				Data:      map[string]string{"hostname": "auth.usetelos.ai"},
			},
			{
				Namespace: "ns-auth",
				Data:      map[string]string{"hostname": "dashboard-auth.usetelos.ai"},
			},
		},
		sessionapi.Session{
			SessionID:   "sess_auth",
			SessionKind: &kind,
			SpecName:    &name,
			Status:      sessionapi.StatusRunning,
		},
	)

	if handle != "" {
		t.Fatalf("ambiguous handle: got %q", handle)
	}
}

func TestProductHandlePrefersProductRouteOverDashboardRoute(t *testing.T) {
	name := "auth"
	kind := sessionapi.KindController
	handle := productHandleFor(
		[]publicRoute{
			{
				Namespace: "ns-auth",
				Data: map[string]string{
					"type":     "service",
					"hostname": "auth.usetelos.ai",
				},
			},
			{
				Namespace: "ns-auth",
				Data: map[string]string{
					"type":     "dashboard",
					"hostname": "dashboard-auth.usetelos.ai",
				},
			},
		},
		sessionapi.Session{
			SessionID:   "sess_auth",
			SessionKind: &kind,
			SpecName:    &name,
			Status:      sessionapi.StatusFailed,
		},
	)

	if handle != "auth.usetelos.ai" {
		t.Fatalf("handle: got %q", handle)
	}
}

func TestParsePublicRoutes(t *testing.T) {
	routes, err := parsePublicRoutes([]byte(`{
	  "items": [
	    {
	      "metadata": {
	        "namespace": "ns-postgres",
	        "name": "dashboard-route",
	        "labels": {"telos.ai/public-route": "primary"}
	      },
	      "data": {
	        "service": "http://dashboard.ns-postgres.svc.cluster.local:8080",
	        "hostname": "postgres.usetelos.ai"
	      }
	    }
	  ]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 {
		t.Fatalf("routes: got %d", len(routes))
	}
	if routes[0].Namespace != "ns-postgres" {
		t.Fatalf("namespace: got %q", routes[0].Namespace)
	}
	if got := routeNamespaces(routes[0].Data); len(got) != 1 || got[0] != "ns-postgres" {
		t.Fatalf("route namespaces: got %#v", got)
	}
}

func TestReadPublicRoutesUsesContextTimeoutOnly(t *testing.T) {
	original := kubectlOutput
	t.Cleanup(func() { kubectlOutput = original })

	var gotTimeout time.Duration
	var gotArgs []string
	kubectlOutput = func(_ context.Context, timeout time.Duration, args ...string) ([]byte, error) {
		gotTimeout = timeout
		gotArgs = append([]string(nil), args...)
		return []byte(`{"items":[]}`), nil
	}

	routes, err := readPublicRoutes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 0 {
		t.Fatalf("routes: got %#v", routes)
	}
	if gotTimeout != 2*time.Second {
		t.Fatalf("timeout: got %s", gotTimeout)
	}
	wantArgs := []string{"get", "cm", "-A", "-l", "telos.ai/public-route=primary", "-o", "json"}
	if !slices.Equal(gotArgs, wantArgs) {
		t.Fatalf("kubectl args: got %#v want %#v", gotArgs, wantArgs)
	}
}

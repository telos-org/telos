package telosd

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/telos-org/telos/internal/sessionapi"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
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
	if session.ServiceURL == nil || *session.ServiceURL != "https://postgres.usetelos.ai" {
		t.Fatalf("service_url: got %#v", session.ServiceURL)
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
	if session.ServiceURL == nil || *session.ServiceURL != "https://auth-service.usetelos.ai" {
		t.Fatalf("service_url: got %#v", session.ServiceURL)
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

	if session.ServiceURL == nil || *session.ServiceURL != "https://postgres.usetelos.ai" {
		t.Fatalf("service_url: got %#v", session.ServiceURL)
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
	if session.ServiceURL != nil {
		t.Fatalf("task session got service_url: %q", *session.ServiceURL)
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
				Data:      map[string]string{"hostname": "auth-a.usetelos.ai"},
			},
			{
				Namespace: "ns-auth",
				Data:      map[string]string{"hostname": "auth-b.usetelos.ai"},
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

func TestProductHandleIgnoresRouteWithDifferentProductPrefix(t *testing.T) {
	name := "auth"
	kind := sessionapi.KindController
	session := sessionapi.Session{
		SessionID:   "sess_auth",
		SessionKind: &kind,
		SpecName:    &name,
		Status:      sessionapi.StatusRunning,
	}
	routes := []publicRoute{
		{
			Namespace: "ns-auth",
			Data: map[string]string{
				"type":     "app",
				"prefix":   "authprobe-test",
				"hostname": "authprobe-test-rkz5f.usetelos.ai",
			},
		},
		{
			Namespace: "ns-auth",
			Data: map[string]string{
				"type":     "dashboard",
				"prefix":   "dashboard-authprobe-test",
				"hostname": "dashboard-authprobe-test-rkz5f.usetelos.ai",
			},
		},
	}

	if handle := productHandleFor(routes, session); handle != "" {
		t.Fatalf("service handle: got %q", handle)
	}
	if handle := dashboardHandleFor(routes, session); handle != "" {
		t.Fatalf("dashboard handle: got %q", handle)
	}
}

func TestRouteHandlesMatchProductPrefix(t *testing.T) {
	name := "auth"
	kind := sessionapi.KindController
	session := sessionapi.Session{
		SessionID:   "sess_auth",
		SessionKind: &kind,
		SpecName:    &name,
		Status:      sessionapi.StatusRunning,
	}
	routes := []publicRoute{
		{
			Namespace: "ns-auth",
			Data: map[string]string{
				"type":     "app",
				"prefix":   "auth",
				"hostname": "auth-ga84s.usetelos.ai",
			},
		},
		{
			Namespace: "ns-auth",
			Data: map[string]string{
				"type":     "dashboard",
				"prefix":   "dashboard-auth",
				"hostname": "dashboard-auth-n6ryk.usetelos.ai",
			},
		},
	}

	if handle := productHandleFor(routes, session); handle != "auth-ga84s.usetelos.ai" {
		t.Fatalf("service handle: got %q", handle)
	}
	if handle := dashboardHandleFor(routes, session); handle != "dashboard-auth-n6ryk.usetelos.ai" {
		t.Fatalf("dashboard handle: got %q", handle)
	}
}

func TestRouteHandlesPreferExplicitPublicRouteLabels(t *testing.T) {
	name := "auth"
	kind := sessionapi.KindController
	session := sessionapi.Session{
		SessionID:   "sess_auth",
		SessionKind: &kind,
		SpecName:    &name,
		Status:      sessionapi.StatusRunning,
	}
	routes := []publicRoute{
		{
			Namespace: "ns-auth",
			Labels:    map[string]string{publicRouteLabel: "service"},
			Data:      map[string]string{"hostname": "auth.usetelos.ai"},
		},
		{
			Namespace: "ns-auth",
			Labels:    map[string]string{publicRouteLabel: "dashboard"},
			Data:      map[string]string{"hostname": "dashboard-auth.usetelos.ai"},
		},
	}

	if handle := productHandleFor(routes, session); handle != "auth.usetelos.ai" {
		t.Fatalf("service handle: got %q", handle)
	}
	if handle := dashboardHandleFor(routes, session); handle != "dashboard-auth.usetelos.ai" {
		t.Fatalf("dashboard handle: got %q", handle)
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

func TestReadPublicRoutesFromClient(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "auth-route",
				Namespace: "ns-auth",
				Labels:    map[string]string{publicRouteLabel: "primary"},
			},
			Data: map[string]string{
				"type":     "app",
				"hostname": "auth.usetelos.ai",
			},
		},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "dashboard-route",
				Namespace: "ns-auth",
				Labels:    map[string]string{publicRouteLabel: "dashboard"},
			},
			Data: map[string]string{
				"hostname": "dashboard-auth.usetelos.ai",
			},
		},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "private-route",
				Namespace: "ns-auth",
				Labels:    map[string]string{"app": "auth"},
			},
			Data: map[string]string{
				"hostname": "private.usetelos.ai",
			},
		},
	)

	routes, err := readPublicRoutesFromClient(context.Background(), client)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 2 {
		t.Fatalf("routes: got %#v", routes)
	}
	handles := []string{routeHandle(routes[0].Data), routeHandle(routes[1].Data)}
	slices.Sort(handles)
	if !slices.Equal(handles, []string{"auth.usetelos.ai", "dashboard-auth.usetelos.ai"}) {
		t.Fatalf("handles: got %#v", handles)
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
	wantArgs := []string{"get", "cm", "-A", "-l", "telos.ai/public-route in (primary,service,dashboard)", "-o", "json"}
	if !slices.Equal(gotArgs, wantArgs) {
		t.Fatalf("kubectl args: got %#v want %#v", gotArgs, wantArgs)
	}
}

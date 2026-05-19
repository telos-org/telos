package telosd

import (
	"context"
	"testing"

	"github.com/telos-org/telos-go/internal/sessionapi"
)

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
	})
	markdown := "---\nversion: v0\nname: postgres\nplatform: cloud\n---\n# Postgres\n"

	session, err := store.Create(sessionapi.SessionCreateRequest{SpecMarkdown: &markdown})
	if err != nil {
		t.Fatal(err)
	}
	if session.ArtifactURI == nil || *session.ArtifactURI != "https://postgres.usetelos.ai" {
		t.Fatalf("artifact_uri: got %#v", session.ArtifactURI)
	}
}

func TestCloudSessionStoreSkipsTerminalProductHandle(t *testing.T) {
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
	})
	name := "postgres"
	kind := sessionapi.KindController
	session := sessionapi.Session{
		SessionID:   "sess_1",
		SessionKind: &kind,
		SpecName:    &name,
		Status:      sessionapi.StatusCompleted,
	}

	store.enrich(&session, store.routes())

	if session.ArtifactURI != nil {
		t.Fatalf("terminal session got artifact_uri: %q", *session.ArtifactURI)
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
	})
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

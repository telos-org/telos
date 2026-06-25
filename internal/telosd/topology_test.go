package telosd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/telos-org/telos/internal/sessionapi"
)

func TestScanClusterSummarizesNamespaces(t *testing.T) {
	restore := stubKubectl(t, map[string]string{
		"config current-context": "kind-telos\n",
		"get namespaces -o json": `{
			"items": [
				{"metadata": {"name": "default"}},
				{"metadata": {"name": "ns-postgres"}}
			]
		}`,
		"get pods -A -o json": `{
			"items": [
				{
					"metadata": {
						"name": "postgres-0",
						"namespace": "ns-postgres",
						"labels": {"app": "postgres"}
					},
					"status": {
						"phase": "Running",
						"conditions": [{"type": "Ready", "status": "True"}],
						"containerStatuses": [{"restartCount": 1, "image": "postgres:16"}]
					}
				},
				{
					"metadata": {"name": "verify-smoke", "namespace": "ns-postgres"},
					"status": {"phase": "Succeeded"}
				}
			]
		}`,
		"get svc -A -o json": `{
			"items": [
				{
					"metadata": {"name": "postgres", "namespace": "ns-postgres"},
					"spec": {
						"type": "ClusterIP",
						"clusterIP": "10.0.0.10",
						"ports": [{"name": "pg", "port": 5432, "targetPort": "postgres", "protocol": "TCP"}]
					}
				}
			]
		}`,
		"get cm -A -l telos.ai/public-route in (primary,service,dashboard) -o json": `{
			"items": [
				{
					"metadata": {"namespace": "ns-postgres", "name": "dashboard-route"},
					"data": {
						"type": "dashboard",
						"hostname": "postgres-ui.usetelos.ai",
						"service": "http://dashboard.ns-postgres.svc.cluster.local:8080"
					}
				}
			]
		}`,
	})
	defer restore()

	cluster := scanCluster(context.Background())

	if cluster.Name != "kind-telos" || cluster.Context != "kind-telos" {
		t.Fatalf("identity: got %#v context %#v", cluster.Name, cluster.Context)
	}
	if !cluster.Reachable {
		t.Fatal("cluster should be reachable")
	}
	if len(cluster.Namespaces) != 1 {
		t.Fatalf("namespaces: got %d", len(cluster.Namespaces))
	}
	namespace := cluster.Namespaces[0]
	if namespace.Namespace != "ns-postgres" || namespace.Name != "postgres" {
		t.Fatalf("namespace: got %#v", namespace)
	}
	if namespace.Health.Status != "healthy" || namespace.Health.Ready != 1 || namespace.Health.Total != 1 {
		t.Fatalf("health: got %#v", namespace.Health)
	}
	if namespace.DashboardURL == nil || *namespace.DashboardURL != "https://postgres-ui.usetelos.ai" {
		t.Fatalf("dashboard: got %#v", namespace.DashboardURL)
	}
	if len(namespace.Pods) != 2 || namespace.Pods[0].Image == nil || *namespace.Pods[0].Image != "postgres:16" {
		t.Fatalf("pods: got %#v", namespace.Pods)
	}
	if len(namespace.Services) != 1 || namespace.Services[0].Ports[0].Port != 5432 {
		t.Fatalf("services: got %#v", namespace.Services)
	}
}

func TestClusterRouteRequiresClusterReadScope(t *testing.T) {
	restore := stubKubectl(t, map[string]string{
		"get namespaces -o json": `{"items": []}`,
	})
	defer restore()

	mux := http.NewServeMux()
	registerTopologyRoutes(mux, sessionapi.AllowAllAuthorizer{})
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/clusters", nil)

	mux.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status: got %d", res.Code)
	}
	if !strings.Contains(res.Body.String(), `"clusters"`) {
		t.Fatalf("body: %s", res.Body.String())
	}
}

func stubKubectl(t *testing.T, responses map[string]string) func() {
	t.Helper()
	previous := kubectlOutput
	kubectlOutput = func(_ context.Context, _ time.Duration, args ...string) ([]byte, error) {
		key := strings.Join(args, " ")
		if response, ok := responses[key]; ok {
			return []byte(response), nil
		}
		return []byte(`{"items":[]}`), nil
	}
	return func() {
		kubectlOutput = previous
	}
}

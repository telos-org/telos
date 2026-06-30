package telosd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestReconcileTunnelRoutesPublishesEnvAndProductRoutes(t *testing.T) {
	t.Setenv("TELOS_CF_ZONE_ID", "zone_123")
	cloudflareRequests := []map[string]any{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "Bearer cf-token" {
			t.Fatalf("authorization header: got %q", auth)
		}
		if !strings.Contains(r.URL.Path, "/zones/zone_123/dns_records") {
			t.Fatalf("unexpected path: %s", r.URL.String())
		}
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]any{"result": []any{}})
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		body["method"] = r.Method
		cloudflareRequests = append(cloudflareRequests, body)
		_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"id": "record_123"}})
	}))
	defer server.Close()

	previousBaseURL := cloudflareAPIBaseURL
	cloudflareAPIBaseURL = server.URL
	defer func() { cloudflareAPIBaseURL = previousBaseURL }()

	client := fake.NewSimpleClientset(
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "cloudflare-api", Namespace: cloudflaredNamespace},
			Data:       map[string][]byte{"token": []byte("cf-token")},
		},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "env-tunnel", Namespace: cloudflaredNamespace},
			Data: map[string]string{
				"tunnel_id":  "tunnel_123",
				"env_handle": "fresh-env.usetelos.ai",
			},
		},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "cloudflared", Namespace: cloudflaredNamespace},
			Spec: appsv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{}},
				},
			},
		},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "dashboard-route",
				Namespace: "ns-postgres",
				Labels:    map[string]string{"telos.ai/public-route": "primary"},
			},
			Data: map[string]string{
				"target_service": "dashboard.ns-postgres.svc.cluster.local",
				"target_port":    "8080",
				"type":           "dashboard",
				"hostname":       "postgres.usetelos.ai",
			},
		},
	)

	if err := reconcileTunnelRoutes(context.Background(), client, server.Client()); err != nil {
		t.Fatalf("reconcileTunnelRoutes: %v", err)
	}

	config, err := client.CoreV1().ConfigMaps(cloudflaredNamespace).Get(context.Background(), "tunnel-config", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get tunnel config: %v", err)
	}
	rendered := config.Data["config.yaml"]
	for _, want := range []string{
		"tunnel: tunnel_123",
		"hostname: fresh-env.usetelos.ai",
		"service: " + envAPIService,
		"hostname: postgres.usetelos.ai",
		"service: " + envAPIService,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered tunnel config missing %q:\n%s", want, rendered)
		}
	}

	route, err := client.CoreV1().ConfigMaps("ns-postgres").Get(context.Background(), "dashboard-route", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get public route: %v", err)
	}
	if route.Data["product_handle"] != "postgres.usetelos.ai" {
		t.Fatalf("product_handle: got %q", route.Data["product_handle"])
	}
	if route.Data["service"] != "http://dashboard.ns-postgres.svc.cluster.local:8080" {
		t.Fatalf("service: got %q", route.Data["service"])
	}

	deployment, err := client.AppsV1().Deployments(cloudflaredNamespace).Get(context.Background(), "cloudflared", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get cloudflared deployment: %v", err)
	}
	if deployment.Spec.Template.Annotations["telos.ai/route-restarted-at"] == "" {
		t.Fatal("expected cloudflared restart annotation")
	}

	seen := map[string]bool{}
	for _, request := range cloudflareRequests {
		if request["method"] == http.MethodPost {
			seen[request["name"].(string)] = true
		}
	}
	if !seen["fresh-env.usetelos.ai"] || !seen["postgres.usetelos.ai"] {
		t.Fatalf("missing Cloudflare DNS requests: %+v", cloudflareRequests)
	}
}

func TestReconcileTunnelRoutesLabelsManagedNamespaces(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns-auth"}},
	)

	if err := reconcileTunnelRoutes(context.Background(), client, nil); err != nil {
		t.Fatalf("reconcileTunnelRoutes: %v", err)
	}

	defaultNamespace, err := client.CoreV1().Namespaces().Get(context.Background(), "default", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get default namespace: %v", err)
	}
	if len(defaultNamespace.Labels) != 0 {
		t.Fatalf("default namespace labels: got %#v", defaultNamespace.Labels)
	}

	authNamespace, err := client.CoreV1().Namespaces().Get(context.Background(), "ns-auth", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get auth namespace: %v", err)
	}
	for key, value := range workerNamespaceLabels {
		if authNamespace.Labels[key] != value {
			t.Fatalf("ns-auth label %s: got %q want %q", key, authNamespace.Labels[key], value)
		}
	}
}

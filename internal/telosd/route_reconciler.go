package telosd

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/telos-org/telos/internal/envutil"
)

const (
	cloudflaredNamespace = "ns-cloudflared"
	envAPIService        = "http://telos-api.ns-telos-env.svc.cluster.local:8000"
	routeRandAlphabet    = "abcdefghijkmnpqrstuvwxyz23456789"
)

var cloudflareAPIBaseURL = "https://api.cloudflare.com/client/v4"

type tunnelRoute struct {
	Hostname string
	Service  string
	Protocol string
}

type routeReconciler struct {
	client     kubernetes.Interface
	httpClient *http.Client
	retry      *reconcileRetryTracker
}

func startRouteReconciler(ctx context.Context, client kubernetes.Interface) {
	if os.Getenv("TELOS_ROUTE_RECONCILER_ENABLED") != "1" {
		return
	}
	intervalSec := envutil.EnvInt("TELOS_ROUTE_RECONCILER_INTERVAL", 10)
	if intervalSec <= 0 {
		intervalSec = 10
	}
	interval := time.Duration(intervalSec) * time.Second
	reconciler := routeReconciler{client: client, httpClient: http.DefaultClient, retry: newReconcileRetryTracker()}
	go func() {
		for {
			if err := reconciler.reconcile(ctx); err != nil {
				log.Printf("route reconcile failed: %v", err)
			}
			timer := time.NewTimer(interval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
}

func reconcileTunnelRoutes(ctx context.Context, client kubernetes.Interface, httpClient *http.Client) error {
	return (&routeReconciler{client: client, httpClient: httpClient, retry: newReconcileRetryTracker()}).reconcile(ctx)
}

func (r *routeReconciler) reconcile(ctx context.Context) error {
	if r.retry == nil {
		r.retry = newReconcileRetryTracker()
	}
	var reconcileErr error
	if err := r.reconcileManagedNamespacePolicy(ctx); err != nil {
		reconcileErr = errors.Join(reconcileErr, err)
	}
	zoneID := strings.TrimSpace(os.Getenv("TELOS_CF_ZONE_ID"))
	if zoneID == "" {
		return reconcileErr
	}
	token, err := cloudflareToken(ctx, r.client)
	if err != nil || token == "" {
		return errors.Join(reconcileErr, err)
	}
	tunnelID, envHandle, err := envTunnel(ctx, r.client)
	if err != nil || tunnelID == "" || envHandle == "" {
		return errors.Join(reconcileErr, err)
	}
	routes, err := routeRequests(ctx, r.client)
	if err != nil {
		reconcileErr = errors.Join(reconcileErr, err)
	}
	allRoutes := append([]tunnelRoute{{Hostname: envHandle, Service: envAPIService, Protocol: "http"}}, routes...)
	for _, route := range allRoutes {
		key := "cloudflare-dns:" + route.Hostname
		if !r.retry.shouldRun(key) {
			continue
		}
		if err := ensureCloudflareDNS(ctx, r.httpClient, zoneID, token, route.Hostname, tunnelID); err != nil {
			reconcileErr = errors.Join(reconcileErr, fmt.Errorf("ensure dns %s: %w", route.Hostname, r.retry.recordError(key, classifyCloudflareError(err))))
			continue
		}
		r.retry.recordSuccess(key)
	}
	changed, err := applyTunnelConfig(ctx, r.client, renderTunnelConfig(tunnelID, envHandle, routes))
	if err != nil {
		return errors.Join(reconcileErr, err)
	}
	if changed {
		if err := restartCloudflared(ctx, r.client); err != nil {
			return errors.Join(reconcileErr, err)
		}
	}
	return reconcileErr
}

func reconcileManagedNamespacePolicy(ctx context.Context, client kubernetes.Interface) error {
	return (&routeReconciler{client: client, retry: newReconcileRetryTracker()}).reconcileManagedNamespacePolicy(ctx)
}

func (r *routeReconciler) reconcileManagedNamespacePolicy(ctx context.Context) error {
	namespaces, err := r.client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	var reconcileErr error
	for _, namespace := range namespaces.Items {
		if !strings.HasPrefix(namespace.Name, "ns-") {
			continue
		}
		key := "namespace-policy:" + namespace.Name
		if !r.retry.shouldRun(key) {
			continue
		}
		next := namespace.DeepCopy()
		if next.Labels == nil {
			next.Labels = map[string]string{}
		}
		changed := false
		for key, value := range workerNamespaceLabels {
			if next.Labels[key] != value {
				next.Labels[key] = value
				changed = true
			}
		}
		if !changed {
			r.retry.recordSuccess(key)
			continue
		}
		if _, err := r.client.CoreV1().Namespaces().Update(ctx, next, metav1.UpdateOptions{}); err != nil {
			reconcileErr = errors.Join(reconcileErr, fmt.Errorf("label namespace %s: %w", namespace.Name, r.retry.recordError(key, err)))
			continue
		}
		r.retry.recordSuccess(key)
	}
	return reconcileErr
}

func cloudflareToken(ctx context.Context, client kubernetes.Interface) (string, error) {
	secret, err := client.CoreV1().Secrets(cloudflaredNamespace).Get(ctx, "cloudflare-api", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(secret.Data["token"])), nil
}

func envTunnel(ctx context.Context, client kubernetes.Interface) (string, string, error) {
	cm, err := client.CoreV1().ConfigMaps(cloudflaredNamespace).Get(ctx, "env-tunnel", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	return strings.TrimSpace(cm.Data["tunnel_id"]), strings.TrimSpace(cm.Data["env_handle"]), nil
}

func routeRequests(ctx context.Context, client kubernetes.Interface) ([]tunnelRoute, error) {
	public, err := publicRouteRequests(ctx, client)
	if err != nil {
		return nil, err
	}
	console, err := consoleRouteRequests(ctx, client)
	if err != nil {
		return nil, err
	}
	return append(public, console...), nil
}

func publicRouteRequests(ctx context.Context, client kubernetes.Interface) ([]tunnelRoute, error) {
	list, err := client.CoreV1().ConfigMaps("").List(ctx, metav1.ListOptions{
		LabelSelector: "telos.ai/public-route=primary",
	})
	if err != nil {
		return nil, err
	}
	routes := make([]tunnelRoute, 0, len(list.Items))
	var reconcileErr error
	for _, item := range list.Items {
		route, patch, ok := publicRouteFromConfigMap(item)
		if !ok {
			continue
		}
		if len(patch) > 0 {
			next := item.DeepCopy()
			if next.Data == nil {
				next.Data = map[string]string{}
			}
			for key, value := range patch {
				next.Data[key] = value
			}
			if _, err := client.CoreV1().ConfigMaps(item.Namespace).Update(ctx, next, metav1.UpdateOptions{}); err != nil {
				reconcileErr = errors.Join(reconcileErr, fmt.Errorf("patch public route %s/%s: %w", item.Namespace, item.Name, err))
				continue
			}
		}
		routes = append(routes, route)
	}
	return routes, reconcileErr
}

func publicRouteFromConfigMap(cm corev1.ConfigMap) (tunnelRoute, map[string]string, bool) {
	data := cm.Data
	service := routeService(data)
	if cm.Namespace == "" || cm.Name == "" || service == "" {
		return tunnelRoute{}, nil, false
	}
	protocol := routeProtocol(data, service)
	routeType := routeType(data, cm.Name, protocol, service)
	prefix := strings.TrimSpace(data["prefix"])
	if prefix == "" {
		prefix = strings.TrimPrefix(cm.Namespace, "ns-")
	}
	if prefix == "" {
		prefix = "product"
	}
	routeRand := strings.TrimSpace(data["rand"])
	hostname := routeHandle(data)
	if hostname == "" {
		if routeRand == "" {
			routeRand = randomRouteSuffix()
		}
		hostname = fmt.Sprintf("%s-%s.%s", prefix, routeRand, routeDomain())
	}

	desired := map[string]string{
		"prefix":         prefix,
		"hostname":       hostname,
		"product_handle": hostname,
		"allocated_at":   valueOr(data["allocated_at"], routeTimestamp()),
		"protocol":       protocol,
		"service":        service,
		"type":           routeType,
	}
	if routeRand != "" {
		desired["rand"] = routeRand
	}
	patch := map[string]string{}
	for key, value := range desired {
		if strings.TrimSpace(data[key]) != value {
			patch[key] = value
		}
	}
	return tunnelRoute{Hostname: hostname, Service: service, Protocol: protocol}, patch, true
}

func consoleRouteRequests(ctx context.Context, client kubernetes.Interface) ([]tunnelRoute, error) {
	list, err := client.CoreV1().ConfigMaps("").List(ctx, metav1.ListOptions{
		LabelSelector: "telos.ai/route=console",
	})
	if err != nil {
		return nil, err
	}
	routes := make([]tunnelRoute, 0, len(list.Items))
	for _, item := range list.Items {
		hostname := strings.TrimSpace(item.Data["hostname"])
		service := strings.TrimSpace(item.Data["service"])
		if hostname == "" || service == "" {
			continue
		}
		routes = append(routes, tunnelRoute{
			Hostname: hostname,
			Service:  service,
			Protocol: routeProtocol(item.Data, service),
		})
	}
	return routes, nil
}

func routeService(data map[string]string) string {
	if service := strings.TrimSpace(data["service"]); service != "" {
		return service
	}
	targetService := strings.TrimSpace(data["target_service"])
	targetPort := strings.TrimSpace(data["target_port"])
	if targetService == "" || targetPort == "" {
		return ""
	}
	if strings.Contains(targetService, "://") {
		return targetService
	}
	protocol := strings.ToLower(strings.TrimSpace(firstNonEmpty(data["protocol"], data["type"])))
	scheme := "http"
	if protocol == "tcp" {
		scheme = "tcp"
	}
	return fmt.Sprintf("%s://%s:%s", scheme, targetService, targetPort)
}

func routeProtocol(data map[string]string, service string) string {
	if protocol := strings.ToLower(strings.TrimSpace(data["protocol"])); protocol != "" {
		return protocol
	}
	switch {
	case strings.HasPrefix(service, "tcp://"):
		return "tcp"
	case strings.HasPrefix(service, "https://"):
		return "https"
	default:
		return "http"
	}
}

func routeType(data map[string]string, name string, protocol string, service string) string {
	if value := strings.ToLower(strings.TrimSpace(data["type"])); value != "" {
		return value
	}
	if (protocol == "http" || protocol == "https") && (strings.Contains(name, "dashboard") || strings.Contains(service, ".dashboard.") || strings.Contains(service, "dashboard.")) {
		return "dashboard"
	}
	if protocol == "tcp" {
		return "service"
	}
	return "app"
}

func ensureCloudflareDNS(ctx context.Context, httpClient *http.Client, zoneID string, token string, hostname string, tunnelID string) error {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	target := tunnelID + ".cfargotunnel.com"
	query := url.Values{"type": []string{"CNAME"}, "name": []string{hostname}}
	listed, err := cloudflareJSON(ctx, httpClient, http.MethodGet, fmt.Sprintf("/zones/%s/dns_records?%s", zoneID, query.Encode()), token, nil)
	if err != nil {
		return err
	}
	records, _ := listed["result"].([]any)
	for _, raw := range records {
		record, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if stringValue(record["content"]) == target && boolValue(record["proxied"]) {
			return nil
		}
		id := stringValue(record["id"])
		if id == "" {
			continue
		}
		_, err := cloudflareJSON(ctx, httpClient, http.MethodPatch, fmt.Sprintf("/zones/%s/dns_records/%s", zoneID, id), token, cloudflareDNSRecord(hostname, target))
		return err
	}
	_, err = cloudflareJSON(ctx, httpClient, http.MethodPost, fmt.Sprintf("/zones/%s/dns_records", zoneID), token, cloudflareDNSRecord(hostname, target))
	return err
}

func cloudflareDNSRecord(hostname string, target string) map[string]any {
	return map[string]any{
		"type":    "CNAME",
		"name":    hostname,
		"content": target,
		"ttl":     1,
		"proxied": true,
	}
}

func cloudflareJSON(ctx context.Context, httpClient *http.Client, method string, path string, token string, body map[string]any) (map[string]any, error) {
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, cloudflareAPIBaseURL+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := fmt.Errorf("cloudflare %s %s: HTTP %d", method, path, resp.StatusCode)
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			err = permanentReconcileError("%v", err)
		}
		return nil, err
	}
	return payload, nil
}

func classifyCloudflareError(err error) error {
	if isPermanentReconcileError(err) {
		return err
	}
	return err
}

func renderTunnelConfig(tunnelID string, envHandle string, routes []tunnelRoute) string {
	lines := []string{
		"tunnel: " + tunnelID,
		"credentials-file: /etc/cloudflared/creds/credentials.json",
		"ingress:",
		"  - hostname: " + envHandle,
		"    path: /internal/*",
		"    service: http_status:404",
		"  - hostname: " + envHandle,
		"    service: " + envAPIService,
	}
	for _, route := range routes {
		lines = append(lines,
			"  - hostname: "+route.Hostname,
			"    service: "+route.Service,
		)
	}
	lines = append(lines, "  - service: http_status:404")
	return strings.Join(lines, "\n") + "\n"
}

func applyTunnelConfig(ctx context.Context, client kubernetes.Interface, config string) (bool, error) {
	current, err := client.CoreV1().ConfigMaps(cloudflaredNamespace).Get(ctx, "tunnel-config", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err := client.CoreV1().ConfigMaps(cloudflaredNamespace).Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "tunnel-config", Namespace: cloudflaredNamespace},
			Data:       map[string]string{"config.yaml": config},
		}, metav1.CreateOptions{})
		return err == nil, err
	}
	if err != nil {
		return false, err
	}
	if current.Data["config.yaml"] == config {
		return false, nil
	}
	next := current.DeepCopy()
	if next.Data == nil {
		next.Data = map[string]string{}
	}
	next.Data["config.yaml"] = config
	_, err = client.CoreV1().ConfigMaps(cloudflaredNamespace).Update(ctx, next, metav1.UpdateOptions{})
	return err == nil, err
}

func restartCloudflared(ctx context.Context, client kubernetes.Interface) error {
	deployment, err := client.AppsV1().Deployments(cloudflaredNamespace).Get(ctx, "cloudflared", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	next := deployment.DeepCopy()
	if next.Spec.Template.Annotations == nil {
		next.Spec.Template.Annotations = map[string]string{}
	}
	next.Spec.Template.Annotations["telos.ai/route-restarted-at"] = routeTimestamp()
	_, err = client.AppsV1().Deployments(cloudflaredNamespace).Update(ctx, next, metav1.UpdateOptions{})
	return err
}

func randomRouteSuffix() string {
	var raw [5]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	out := make([]byte, len(raw))
	for i, value := range raw {
		out[i] = routeRandAlphabet[int(value)%len(routeRandAlphabet)]
	}
	return string(out)
}

func routeDomain() string {
	if value := strings.TrimSpace(os.Getenv("TELOS_DOMAIN")); value != "" {
		return value
	}
	return "usetelos.ai"
}

func routeTimestamp() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func stringValue(value any) string {
	out, _ := value.(string)
	return out
}

func boolValue(value any) bool {
	out, _ := value.(bool)
	return out
}

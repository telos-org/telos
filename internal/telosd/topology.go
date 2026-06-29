package telosd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/telos-org/telos/internal/sessionapi"
)

var kubectlOutput = runKubectlOutput

type clustersResponse struct {
	Clusters []clusterState `json:"clusters"`
}

type clusterState struct {
	Name       string           `json:"name"`
	Context    string           `json:"context,omitempty"`
	Namespaces []namespaceState `json:"namespaces"`
	Reachable  bool             `json:"reachable"`
}

type namespaceState struct {
	Namespace    string         `json:"namespace"`
	Name         string         `json:"name"`
	Pods         []podState     `json:"pods"`
	Services     []serviceState `json:"services"`
	DashboardURL *string        `json:"dashboard_url,omitempty"`
	Health       healthState    `json:"health"`
}

type podState struct {
	Name     string            `json:"name"`
	Labels   map[string]string `json:"labels,omitempty"`
	Phase    string            `json:"phase"`
	Ready    bool              `json:"ready"`
	Restarts int               `json:"restarts,omitempty"`
	Image    *string           `json:"image,omitempty"`
}

type serviceState struct {
	Name      string      `json:"name"`
	Type      string      `json:"type,omitempty"`
	ClusterIP string      `json:"clusterIP,omitempty"`
	Ports     []portState `json:"ports"`
}

type portState struct {
	Name       string `json:"name,omitempty"`
	Port       int    `json:"port"`
	TargetPort any    `json:"targetPort,omitempty"`
	NodePort   *int   `json:"nodePort,omitempty"`
	Protocol   string `json:"protocol,omitempty"`
}

type healthState struct {
	Total  int    `json:"total"`
	Ready  int    `json:"ready"`
	Status string `json:"status"`
}

type kubeList[T any] struct {
	Items []T `json:"items"`
}

type kubeMetadata struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	Labels    map[string]string `json:"labels"`
}

type kubeNamespace struct {
	Metadata kubeMetadata `json:"metadata"`
}

type kubePod struct {
	Metadata kubeMetadata  `json:"metadata"`
	Status   kubePodStatus `json:"status"`
}

type kubePodStatus struct {
	Phase             string                `json:"phase"`
	Conditions        []kubeCondition       `json:"conditions"`
	ContainerStatuses []kubeContainerStatus `json:"containerStatuses"`
}

type kubeCondition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}

type kubeContainerStatus struct {
	RestartCount int    `json:"restartCount"`
	Image        string `json:"image"`
}

type kubeService struct {
	Metadata kubeMetadata    `json:"metadata"`
	Spec     kubeServiceSpec `json:"spec"`
}

type kubeServiceSpec struct {
	Type      string     `json:"type"`
	ClusterIP string     `json:"clusterIP"`
	Ports     []kubePort `json:"ports"`
}

type kubePort struct {
	Name       string `json:"name"`
	Port       int    `json:"port"`
	TargetPort any    `json:"targetPort"`
	NodePort   int    `json:"nodePort"`
	Protocol   string `json:"protocol"`
}

func registerTopologyRoutes(mux *http.ServeMux, authorizer sessionapi.Authorizer) {
	mux.HandleFunc("GET /api/clusters", func(w http.ResponseWriter, r *http.Request) {
		if _, err := authorizer.Caller(r, sessionapi.AccessRequest{Action: sessionapi.ActionReadCluster}); err != nil {
			writeAuthError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, clustersResponse{
			Clusters: []clusterState{scanCluster(r.Context())},
		})
	})
}

func scanCluster(ctx context.Context) clusterState {
	name, contextName := clusterIdentity(ctx)
	cluster := clusterState{Name: name, Context: contextName, Reachable: false}

	namespaces, err := kubectlList[kubeNamespace](ctx, 10*time.Second, "get", "namespaces", "-o", "json")
	if err != nil {
		cluster.Namespaces = []namespaceState{}
		return cluster
	}
	cluster.Reachable = true

	nsNames := telosNamespaces(namespaces.Items)
	cluster.Namespaces = make([]namespaceState, 0, len(nsNames))
	if len(nsNames) == 0 {
		return cluster
	}

	pods, _ := kubectlList[kubePod](ctx, 10*time.Second, "get", "pods", "-A", "-o", "json")
	services, _ := kubectlList[kubeService](ctx, 10*time.Second, "get", "svc", "-A", "-o", "json")
	dashboards := dashboardHandles(ctx)
	podsByNamespace := podsByNamespace(pods.Items)
	servicesByNamespace := servicesByNamespace(services.Items)

	for _, namespace := range nsNames {
		pods := podsByNamespace[namespace]
		cluster.Namespaces = append(cluster.Namespaces, namespaceState{
			Namespace:    namespace,
			Name:         strings.TrimPrefix(namespace, "ns-"),
			Pods:         pods,
			Services:     servicesByNamespace[namespace],
			DashboardURL: dashboards[namespace],
			Health:       summarizeHealth(pods),
		})
	}
	return cluster
}

func clusterIdentity(ctx context.Context) (string, string) {
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		if name := strings.TrimSpace(os.Getenv("TELOS_CLUSTER_NAME")); name != "" {
			return name, ""
		}
		return "local", ""
	}
	out, err := kubectlOutput(ctx, 5*time.Second, "config", "current-context")
	if err != nil {
		return "local", ""
	}
	contextName := strings.TrimSpace(string(out))
	if contextName == "" {
		return "local", ""
	}
	return contextName, contextName
}

func telosNamespaces(items []kubeNamespace) []string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		name := item.Metadata.Name
		if strings.HasPrefix(name, "ns-") {
			names = append(names, name)
		}
	}
	return names
}

func podsByNamespace(items []kubePod) map[string][]podState {
	out := map[string][]podState{}
	for _, item := range items {
		namespace := item.Metadata.Namespace
		if namespace == "" {
			continue
		}
		out[namespace] = append(out[namespace], summarizePod(item))
	}
	return out
}

func servicesByNamespace(items []kubeService) map[string][]serviceState {
	out := map[string][]serviceState{}
	for _, item := range items {
		namespace := item.Metadata.Namespace
		name := item.Metadata.Name
		if namespace == "" || name == "" || name == "kubernetes" {
			continue
		}
		out[namespace] = append(out[namespace], summarizeService(item))
	}
	return out
}

func dashboardHandles(ctx context.Context) map[string]*string {
	routes, err := readPublicRoutes(ctx)
	if err != nil {
		return nil
	}
	out := map[string]*string{}
	for _, route := range routes {
		if !isDashboardRoute(route) || isTCPRoute(route.Data) {
			continue
		}
		handle := routeHandle(route.Data)
		if handle == "" {
			continue
		}
		targetNamespace := route.Namespace
		namespaces := routeNamespaces(route.Data)
		if len(namespaces) > 0 {
			targetNamespace = namespaces[0]
		}
		if targetNamespace == "" {
			continue
		}
		url := "https://" + handle
		out[targetNamespace] = &url
	}
	return out
}

func summarizePod(item kubePod) podState {
	return podState{
		Name:     item.Metadata.Name,
		Labels:   item.Metadata.Labels,
		Phase:    valueOr(item.Status.Phase, "Unknown"),
		Ready:    podReady(item.Status.Conditions),
		Restarts: restartCount(item.Status.ContainerStatuses),
		Image:    firstContainerImage(item.Status.ContainerStatuses),
	}
}

func summarizeService(item kubeService) serviceState {
	return serviceState{
		Name:      item.Metadata.Name,
		Type:      item.Spec.Type,
		ClusterIP: item.Spec.ClusterIP,
		Ports:     summarizePorts(item.Spec.Ports),
	}
}

func summarizePorts(items []kubePort) []portState {
	ports := make([]portState, 0, len(items))
	for _, item := range items {
		var nodePort *int
		if item.NodePort > 0 {
			value := item.NodePort
			nodePort = &value
		}
		ports = append(ports, portState{
			Name:       item.Name,
			Port:       item.Port,
			TargetPort: item.TargetPort,
			NodePort:   nodePort,
			Protocol:   item.Protocol,
		})
	}
	return ports
}

func summarizeHealth(pods []podState) healthState {
	total := 0
	ready := 0
	for _, pod := range pods {
		if pod.Phase == "Succeeded" || pod.Phase == "Failed" {
			continue
		}
		total++
		if pod.Ready {
			ready++
		}
	}
	status := "empty"
	switch {
	case total > 0 && ready == total:
		status = "healthy"
	case ready > 0:
		status = "degraded"
	case total > 0:
		status = "down"
	}
	return healthState{Total: total, Ready: ready, Status: status}
}

func podReady(conditions []kubeCondition) bool {
	for _, condition := range conditions {
		if condition.Type == "Ready" && condition.Status == "True" {
			return true
		}
	}
	return false
}

func restartCount(containers []kubeContainerStatus) int {
	total := 0
	for _, container := range containers {
		total += container.RestartCount
	}
	return total
}

func firstContainerImage(containers []kubeContainerStatus) *string {
	if len(containers) == 0 || containers[0].Image == "" {
		return nil
	}
	return &containers[0].Image
}

func kubectlList[T any](ctx context.Context, timeout time.Duration, args ...string) (kubeList[T], error) {
	out, err := kubectlOutput(ctx, timeout, args...)
	if err != nil {
		return kubeList[T]{}, err
	}
	var list kubeList[T]
	if err := json.Unmarshal(out, &list); err != nil {
		return kubeList[T]{}, err
	}
	if list.Items == nil {
		list.Items = []T{}
	}
	return list, nil
}

func runKubectlOutput(ctx context.Context, timeout time.Duration, args ...string) ([]byte, error) {
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(callCtx, "kubectl", args...)
	out, err := cmd.Output()
	if errors.Is(callCtx.Err(), context.DeadlineExceeded) {
		return nil, callCtx.Err()
	}
	return out, err
}

func valueOr(value string, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(value)
}

func writeAuthError(w http.ResponseWriter, err error) {
	if status, detail, ok := sessionapi.AuthHTTPError(err); ok {
		writeJSON(w, status, map[string]string{"detail": detail})
		return
	}
	writeJSON(w, http.StatusForbidden, map[string]string{"detail": err.Error()})
}

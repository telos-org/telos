package telosd

import (
	"context"
	"encoding/json"
	"os/exec"
	"regexp"
	"strings"

	"github.com/telos-org/telos-go/internal/sessionapi"
)

var routeNamespaceRE = regexp.MustCompile(`\.(ns-[a-z0-9-]+)\.svc(?:\.|:|/|$)`)

type publicRoute struct {
	Namespace   string
	Name        string
	Labels      map[string]string
	Annotations map[string]string
	Data        map[string]string
}

type routeHandleResolver struct {
	read func(context.Context) ([]publicRoute, error)
}

func newRouteHandleResolver() routeHandleResolver {
	return routeHandleResolver{read: readPublicRoutes}
}

func (r routeHandleResolver) Routes(ctx context.Context) ([]publicRoute, error) {
	if r.read == nil {
		return nil, nil
	}
	return r.read(ctx)
}

func productHandleFor(routes []publicRoute, session sessionapi.Session) string {
	if handle := singleProductHandle(routes, func(route publicRoute) bool {
		return routeMatchesSessionID(route, session.SessionID)
	}); handle != "" {
		return handle
	}

	namespace := sessionNamespace(session)
	if namespace == "" {
		return ""
	}
	return singleProductHandle(routes, func(route publicRoute) bool {
		return routeMatchesNamespace(route, namespace)
	})
}

func readPublicRoutes(ctx context.Context) ([]publicRoute, error) {
	cmd := exec.CommandContext(
		ctx,
		"kubectl",
		"--request-timeout=2s",
		"get",
		"cm",
		"-A",
		"-l",
		"telos.ai/public-route=primary",
		"-o",
		"json",
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return parsePublicRoutes(out)
}

func parsePublicRoutes(data []byte) ([]publicRoute, error) {
	var body struct {
		Items []struct {
			Metadata struct {
				Namespace   string            `json:"namespace"`
				Name        string            `json:"name"`
				Labels      map[string]string `json:"labels"`
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
			Data map[string]string `json:"data"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, err
	}
	routes := make([]publicRoute, 0, len(body.Items))
	for _, item := range body.Items {
		routes = append(routes, publicRoute{
			Namespace:   item.Metadata.Namespace,
			Name:        item.Metadata.Name,
			Labels:      item.Metadata.Labels,
			Annotations: item.Metadata.Annotations,
			Data:        item.Data,
		})
	}
	return routes, nil
}

func sessionNamespace(session sessionapi.Session) string {
	if len(session.Specs) > 0 && session.Specs[0].Name != nil {
		return namespaceFromSpec(*session.Specs[0].Name)
	}
	if session.SpecName != nil {
		return namespaceFromSpec(*session.SpecName)
	}
	return ""
}

func namespaceFromSpec(specName string) string {
	name := strings.TrimSpace(specName)
	if name == "" {
		return ""
	}
	return "ns-" + name
}

func routeMatchesSessionID(route publicRoute, sessionID string) bool {
	if sessionID == "" {
		return false
	}
	if route.Data["session_id"] == sessionID || route.Data["controller_id"] == sessionID {
		return true
	}
	if route.Labels["telos.ai/session"] == sessionID || route.Labels["telos.ai/controller"] == sessionID {
		return true
	}
	if route.Annotations["telos.ai/session"] == sessionID || route.Annotations["telos.ai/controller"] == sessionID {
		return true
	}
	return false
}

func routeMatchesNamespace(route publicRoute, namespace string) bool {
	if route.Namespace == namespace {
		return true
	}
	for _, routeNamespace := range routeNamespaces(route.Data) {
		if routeNamespace == namespace {
			return true
		}
	}
	return false
}

func singleProductHandle(routes []publicRoute, match func(publicRoute) bool) string {
	handles := map[string]struct{}{}
	for _, route := range routes {
		if !match(route) || isTCPRoute(route.Data) {
			continue
		}
		handle := routeHandle(route.Data)
		if handle == "" {
			continue
		}
		handles[handle] = struct{}{}
	}
	if len(handles) != 1 {
		return ""
	}
	for handle := range handles {
		return handle
	}
	return ""
}

func routeNamespaces(data map[string]string) []string {
	var namespaces []string
	for _, key := range []string{"namespace", "target_namespace"} {
		value := strings.TrimSpace(data[key])
		if strings.HasPrefix(value, "ns-") {
			namespaces = append(namespaces, value)
		}
	}
	for _, key := range []string{"service", "target_service"} {
		matches := routeNamespaceRE.FindStringSubmatch(strings.TrimSpace(data[key]))
		if len(matches) == 2 {
			namespaces = append(namespaces, matches[1])
		}
	}
	return namespaces
}

func isTCPRoute(data map[string]string) bool {
	if strings.EqualFold(strings.TrimSpace(data["protocol"]), "tcp") {
		return true
	}
	service := strings.TrimSpace(data["service"])
	target := strings.TrimSpace(data["target_service"])
	return strings.HasPrefix(service, "tcp://") || strings.HasPrefix(target, "tcp://")
}

func routeHandle(data map[string]string) string {
	for _, key := range []string{"product_handle", "hostname", "handle"} {
		if value := strings.TrimSpace(data[key]); value != "" {
			return stripScheme(value)
		}
	}
	return ""
}

func stripScheme(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "https://")
	value = strings.TrimPrefix(value, "http://")
	return strings.TrimSuffix(value, "/")
}

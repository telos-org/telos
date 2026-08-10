package game

import "strings"

// AgentFailureBlocker classifies provider authentication or configuration
// failures that will not heal through retrying the same agent request.
func AgentFailureBlocker(errorText string) (string, bool) {
	value := strings.ToLower(strings.TrimSpace(errorText))
	switch {
	case value == "":
		return "", false
	case strings.Contains(value, "inactive virtual key"),
		strings.Contains(value, "inactive api key"),
		strings.Contains(value, "invalid api key"),
		strings.Contains(value, "invalid_api_key"),
		strings.Contains(value, "authentication failed"),
		strings.Contains(value, "unauthorized"),
		strings.Contains(value, "http 401"),
		strings.HasPrefix(value, "401"):
		return "agent_authentication_invalid", true
	case strings.Contains(value, "forbidden"),
		strings.Contains(value, "http 403"),
		strings.HasPrefix(value, "403"):
		return "agent_access_denied", true
	case strings.Contains(value, "api key not configured"),
		strings.Contains(value, "no api key"),
		strings.Contains(value, "credentials not configured"),
		strings.Contains(value, "unknown model"),
		strings.Contains(value, "invalid model"),
		strings.Contains(value, "model not found"):
		return "agent_configuration_invalid", true
	default:
		return "", false
	}
}

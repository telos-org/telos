package game

import "testing"

func TestAgentFailureBlocker(t *testing.T) {
	tests := []struct {
		errorText string
		code      string
		blocked   bool
	}{
		{"403: inactive virtual key", "agent_authentication_invalid", true},
		{"request failed (HTTP 401)", "agent_authentication_invalid", true},
		{"provider returned forbidden", "agent_access_denied", true},
		{"unknown model openai/nope", "agent_configuration_invalid", true},
		{"API key not configured", "agent_configuration_invalid", true},
		{"429: provider at capacity", "", false},
		{"502: no healthy upstream", "", false},
		{"socket connection was closed", "", false},
	}
	for _, test := range tests {
		t.Run(test.errorText, func(t *testing.T) {
			code, blocked := AgentFailureBlocker(test.errorText)
			if code != test.code || blocked != test.blocked {
				t.Fatalf("AgentFailureBlocker(%q) = %q, %t", test.errorText, code, blocked)
			}
		})
	}
}

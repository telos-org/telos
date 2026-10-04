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
		{"400: Your credit balance is too low to access the Anthropic API. Please go to Plans & Billing to upgrade or purchase credits.", "agent_quota_exhausted", true},
		{"429: insufficient_quota", "agent_quota_exhausted", true},
		{"You exceeded your current quota, please check your plan and billing details", "agent_quota_exhausted", true},
		{"billing_hard_limit_reached", "agent_quota_exhausted", true},
		{"insufficient credits", "agent_quota_exhausted", true},
		{"out of budget", "agent_quota_exhausted", true},
		{"Monthly usage limit reached", "agent_quota_exhausted", true},
		{"request failed (HTTP 402)", "agent_quota_exhausted", true},
		{"402: payment required", "agent_quota_exhausted", true},
		{"429: rate limit exceeded, retry after 10 seconds", "", false},
		{"503: billing service unavailable", "", false},
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

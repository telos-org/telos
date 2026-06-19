package executor

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type nativeProviderConfig struct {
	Provider   string
	Model      string
	BaseURL    string
	APIKey     string
	Capability modelCapabilityProfile
}

type modelCapabilityProfile struct {
	StateMode               string `json:"state_mode,omitempty"`
	MaxOutputTokens         int    `json:"max_output_tokens,omitempty"`
	SupportsReasoning       *bool  `json:"supports_reasoning,omitempty"`
	SupportsFunctionCalling *bool  `json:"supports_function_calling,omitempty"`
	StrictProtocol          bool   `json:"strict_protocol,omitempty"`
}

func resolveNativeProvider(model string) (nativeProviderConfig, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return nativeProviderConfig{}, fmt.Errorf("model is required")
	}
	base := firstEnv("TELOS_LITELLM_BASE_URL", "TELOS_API_BASE_URL", "TELOS_BASE_URL")
	if base == "" {
		return nativeProviderConfig{}, fmt.Errorf("TELOS_LITELLM_BASE_URL is required (TELOS_API_BASE_URL and TELOS_BASE_URL are accepted aliases)")
	}
	key := firstEnv("TELOS_LITELLM_API_KEY", "TELOS_API_KEY")
	if key == "" {
		return nativeProviderConfig{}, fmt.Errorf("TELOS_LITELLM_API_KEY is required (TELOS_API_KEY is accepted as an alias)")
	}
	return nativeProviderConfig{
		Provider:   "litellm",
		Model:      model,
		BaseURL:    strings.TrimRight(base, "/"),
		APIKey:     key,
		Capability: modelCapabilityProfileFromEnv(),
	}, nil
}

func firstEnv(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

func modelCapabilityProfileFromEnv() modelCapabilityProfile {
	profile := modelCapabilityProfile{StateMode: "server_chain"}
	if raw := strings.TrimSpace(os.Getenv("TELOS_MODEL_CAPABILITY_PROFILE")); raw != "" {
		_ = json.Unmarshal([]byte(raw), &profile)
	}
	if mode := strings.TrimSpace(os.Getenv("TELOS_MODEL_STATE_MODE")); mode != "" {
		profile.StateMode = mode
	}
	if raw := strings.TrimSpace(os.Getenv("TELOS_MODEL_MAX_OUTPUT_TOKENS")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			profile.MaxOutputTokens = n
		}
	}
	if raw := strings.TrimSpace(os.Getenv("TELOS_MODEL_SUPPORTS_REASONING")); raw != "" {
		value := parseEnvBool(raw)
		profile.SupportsReasoning = &value
	}
	if raw := strings.TrimSpace(os.Getenv("TELOS_MODEL_SUPPORTS_FUNCTION_CALLING")); raw != "" {
		value := parseEnvBool(raw)
		profile.SupportsFunctionCalling = &value
	}
	if raw := strings.TrimSpace(os.Getenv("TELOS_MODEL_STRICT_PROTOCOL")); raw != "" {
		profile.StrictProtocol = parseEnvBool(raw)
	}
	switch profile.StateMode {
	case "server_chain", "stateless_history":
	default:
		profile.StateMode = "server_chain"
	}
	return profile
}

func parseEnvBool(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

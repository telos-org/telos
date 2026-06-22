package executor

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// nativeConfig is the process-level executor configuration, resolved once from
// environment in NewNativeExecutor. It carries the base URL, API key, and the
// per-model pricing and capability tables so that providerFor is a pure map
// lookup with no env parsing per turn or per model response.
type nativeConfig struct {
	baseURL           string
	apiKey            string
	pricing           map[string]modelPricing
	capability        map[string]modelCapabilityProfile
	defaultCapability modelCapabilityProfile
}

func resolveNativeConfig() (nativeConfig, error) {
	base := firstEnv("TELOS_LITELLM_BASE_URL", "TELOS_API_BASE_URL", "TELOS_BASE_URL")
	if base == "" {
		return nativeConfig{}, fmt.Errorf("TELOS_LITELLM_BASE_URL is required (TELOS_API_BASE_URL and TELOS_BASE_URL are accepted aliases)")
	}
	key := firstEnv("TELOS_LITELLM_API_KEY", "TELOS_API_KEY")
	if key == "" {
		return nativeConfig{}, fmt.Errorf("TELOS_LITELLM_API_KEY is required (TELOS_API_KEY is accepted as an alias)")
	}
	return nativeConfig{
		baseURL:           strings.TrimRight(base, "/"),
		apiKey:            key,
		pricing:           parseModelPricingTable(),
		capability:        parseModelCapabilityTable(),
		defaultCapability: modelCapabilityProfileFromEnv(),
	}, nil
}

// providerFor returns the per-model provider config via map lookups — no env
// parsing. The model's capability is resolved as: capability-table[model] →
// the process default capability profile. Pricing is resolved similarly.
func (c nativeConfig) providerFor(model string) (nativeProviderConfig, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return nativeProviderConfig{}, fmt.Errorf("model is required")
	}
	cap := c.defaultCapability
	if specific, ok := c.capability[model]; ok {
		cap = specific
	}
	pricing, pricingKnown := modelPricing{}, false
	if p, ok := c.pricing[model]; ok && validPricing(p) {
		pricing, pricingKnown = p, true
	}
	return nativeProviderConfig{
		Provider:         "litellm",
		Model:            model,
		BaseURL:          c.baseURL,
		APIKey:           c.apiKey,
		Capability:       cap,
		Pricing:          pricing,
		PricingConfigured: pricingKnown,
	}, nil
}

type nativeProviderConfig struct {
	Provider          string
	Model             string
	BaseURL           string
	APIKey            string
	Capability        modelCapabilityProfile
	Pricing           modelPricing
	PricingConfigured bool
}

type modelCapabilityProfile struct {
	StateMode               string `json:"state_mode,omitempty"`
	MaxOutputTokens         int    `json:"max_output_tokens,omitempty"`
	SupportsReasoning       *bool  `json:"supports_reasoning,omitempty"`
	SupportsFunctionCalling *bool  `json:"supports_function_calling,omitempty"`
	StrictProtocol          bool   `json:"strict_protocol,omitempty"`
}

func firstEnv(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

// parseModelCapabilityTable reads TELOS_MODEL_CAPABILITY_TABLE, a JSON map from
// model name to capability profile. Models not in the table fall back to the
// process default profile (from TELOS_MODEL_CAPABILITY_PROFILE / scalar env).
func parseModelCapabilityTable() map[string]modelCapabilityProfile {
	raw := strings.TrimSpace(os.Getenv("TELOS_MODEL_CAPABILITY_TABLE"))
	if raw == "" {
		return nil
	}
	var table map[string]modelCapabilityProfile
	if err := json.Unmarshal([]byte(raw), &table); err != nil {
		return nil
	}
	return table
}

func modelCapabilityProfileFromEnv() modelCapabilityProfile {
	profile := modelCapabilityProfile{StateMode: "stateless_history"}
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
		profile.StateMode = "stateless_history"
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

// parseModelPricingTable reads TELOS_MODEL_PRICING_TABLE, a JSON map from model
// name to per-token pricing. Parsed once at construction; providerFor does a
// map lookup instead of re-reading env per model response.
func parseModelPricingTable() map[string]modelPricing {
	raw := strings.TrimSpace(os.Getenv("TELOS_MODEL_PRICING_TABLE"))
	if raw == "" {
		return nil
	}
	var table map[string]modelPricing
	if err := json.Unmarshal([]byte(raw), &table); err != nil {
		return nil
	}
	return table
}

func validPricing(p modelPricing) bool {
	if p.InputUSDPer1MTokens < 0 || p.OutputUSDPer1MTokens < 0 {
		return false
	}
	if p.InputUSDPer1MTokens == 0 && p.OutputUSDPer1MTokens == 0 {
		return false
	}
	return true
}

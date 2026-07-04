// Package gatewaycred owns the OpenAI-compatible Responses gateway credential.
package gatewaycred

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

const (
	BaseURLEnv         = "TELOS_GATEWAY_BASE_URL"
	APIKeyEnv          = "TELOS_GATEWAY_API_KEY"
	ProviderEnv        = "TELOS_GATEWAY_PROVIDER"
	TransportEnv       = "TELOS_GATEWAY_TRANSPORT"
	KindEnv            = "TELOS_GATEWAY_KIND"
	HeadersEnv         = "TELOS_GATEWAY_HEADERS"
	ModelProfileEnv    = "TELOS_MODEL_PROFILE"
	CostHardLimitEnv   = "TELOS_COST_HARD_LIMIT"
	AnthropicAPIKeyEnv = "ANTHROPIC_API_KEY"
	GeminiAPIKeyEnv    = "GEMINI_API_KEY"
	BillingEnvIDEnv    = "TELOS_ENV_ID"
	BillingTokenEnv    = "TELOS_BILLING_ENV_TOKEN"
	BillingFileEnv     = "TELOS_BILLING_ENV_TOKEN_FILE"
)

type Transport string

type Kind string

type Provider string

// ModelProfile selects the managed model tier a session runs on.
type ModelProfile string

const (
	ModelProfileStandard ModelProfile = "standard"
	ModelProfilePremium  ModelProfile = "premium"
)

// NormalizeModelProfile validates a model profile, defaulting empty to standard.
func NormalizeModelProfile(value string) (ModelProfile, error) {
	profile := ModelProfile(strings.ToLower(strings.TrimSpace(value)))
	if profile == "" {
		return ModelProfileStandard, nil
	}
	switch profile {
	case ModelProfileStandard, ModelProfilePremium:
		return profile, nil
	default:
		return "", fmt.Errorf("invalid model_profile %q", value)
	}
}

func (p *ModelProfile) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	profile, err := NormalizeModelProfile(value)
	if err != nil {
		return err
	}
	*p = profile
	return nil
}

const (
	TransportOpenAISync   Transport = "openai_sync"
	TransportBifrostAsync Transport = "bifrost_async"

	KindOpenAI  Kind = "openai"
	KindBifrost Kind = "bifrost"

	ProviderOpenAI    Provider = "openai"
	ProviderAnthropic Provider = "anthropic"
	ProviderGemini    Provider = "gemini"
	ProviderCodex     Provider = "codex"
)

// Credential is the Responses API endpoint and key a run should use.
type Credential struct {
	BaseURL       string            `json:"base_url,omitempty"`
	APIKey        string            `json:"api_key,omitempty"`
	Provider      Provider          `json:"provider,omitempty"`
	Transport     Transport         `json:"transport,omitempty"`
	Kind          Kind              `json:"kind,omitempty"`
	Headers       map[string]string `json:"headers,omitempty"`
	ModelProfile  ModelProfile      `json:"model_profile,omitempty"`
	CostHardLimit bool              `json:"cost_hard_limit,omitempty"`
}

// FromEnv reads the TELOS_GATEWAY_* credential from the process environment.
// The bool return reports whether the environment carries a usable credential:
// an explicit TELOS_GATEWAY_API_KEY, or a provider whose native key variable is
// set. Stale non-key variables (a leftover base URL, transport, or headers)
// must not opt direct-provider setups into gateway routing; use EnvPresent to
// detect them.
func FromEnv() (Credential, bool, error) {
	rawHeaders := os.Getenv(HeadersEnv)
	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv(BaseURLEnv)), "/")
	apiKey := strings.TrimSpace(os.Getenv(APIKeyEnv))
	providerRaw := strings.TrimSpace(os.Getenv(ProviderEnv))
	transportRaw := strings.TrimSpace(os.Getenv(TransportEnv))
	kindRaw := strings.TrimSpace(os.Getenv(KindEnv))
	if apiKey == "" {
		if provider, err := NormalizeProvider(providerRaw); err == nil {
			apiKey = providerAPIKeyFromEnv(provider)
		}
	}
	if apiKey == "" {
		return Credential{}, false, nil
	}
	headers, err := ParseHeaders(rawHeaders)
	if err != nil {
		return Credential{}, true, err
	}
	provider, err := NormalizeProvider(providerRaw)
	if err != nil {
		return Credential{}, true, err
	}
	transport, kind, err := NormalizeTransportAndKind(transportRaw, kindRaw)
	if err != nil {
		return Credential{}, true, err
	}
	profile, err := NormalizeModelProfile(os.Getenv(ModelProfileEnv))
	if err != nil {
		return Credential{}, true, err
	}
	return Credential{
		BaseURL:       defaultBaseURL(provider, baseURL),
		APIKey:        apiKey,
		Provider:      provider,
		Transport:     transport,
		Kind:          kind,
		Headers:       headers,
		ModelProfile:  profile,
		CostHardLimit: CostHardLimitFromEnv(),
	}, true, nil
}

// EnvPresent reports whether any TELOS_GATEWAY_* variable is set, usable or
// not. Callers use it to avoid re-applying stale gateway env overrides on top
// of the saved config file when FromEnv found no usable credential.
func EnvPresent() bool {
	for _, name := range []string{BaseURLEnv, APIKeyEnv, ProviderEnv, TransportEnv, KindEnv, HeadersEnv} {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return true
		}
	}
	return false
}

// Normalize returns a trimmed, cloned credential with default transport/kind.
func Normalize(cred Credential) (Credential, error) {
	provider, err := NormalizeProvider(string(cred.Provider))
	if err != nil {
		return Credential{}, err
	}
	transport, kind, err := NormalizeTransportAndKind(string(cred.Transport), string(cred.Kind))
	if err != nil {
		return Credential{}, err
	}
	profile, err := NormalizeModelProfile(string(cred.ModelProfile))
	if err != nil {
		return Credential{}, err
	}
	return Credential{
		BaseURL:       defaultBaseURL(provider, strings.TrimRight(strings.TrimSpace(cred.BaseURL), "/")),
		APIKey:        defaultAPIKey(provider, strings.TrimSpace(cred.APIKey)),
		Provider:      provider,
		Transport:     transport,
		Kind:          kind,
		Headers:       CloneHeaders(cred.Headers),
		ModelProfile:  profile,
		CostHardLimit: cred.CostHardLimit,
	}, nil
}

func defaultBaseURL(provider Provider, baseURL string) string {
	if baseURL != "" {
		return baseURL
	}
	switch provider {
	case ProviderAnthropic:
		return "https://api.anthropic.com"
	case ProviderGemini:
		return "https://generativelanguage.googleapis.com"
	case ProviderCodex:
		return "https://chatgpt.com"
	default:
		return ""
	}
}

func defaultAPIKey(provider Provider, apiKey string) string {
	if apiKey != "" {
		return apiKey
	}
	return providerAPIKeyFromEnv(provider)
}

func providerAPIKeyFromEnv(provider Provider) string {
	switch provider {
	case ProviderAnthropic:
		return strings.TrimSpace(os.Getenv(AnthropicAPIKeyEnv))
	case ProviderGemini:
		return strings.TrimSpace(os.Getenv(GeminiAPIKeyEnv))
	default:
		return ""
	}
}

func NormalizeProvider(raw string) (Provider, error) {
	provider := Provider(strings.ToLower(strings.TrimSpace(raw)))
	switch provider {
	case "":
		return ProviderOpenAI, nil
	case ProviderOpenAI, ProviderAnthropic, ProviderGemini, ProviderCodex:
		return provider, nil
	default:
		return "", fmt.Errorf("unknown %s %q (accepted: openai, anthropic, gemini, codex)", ProviderEnv, raw)
	}
}

// NormalizeWithEnvPolicy normalizes a credential and applies the process-wide
// cost-hard-limit policy. Explicit true on the credential wins, but env can
// opt any credential source into fail-closed unknown-cost behavior.
func NormalizeWithEnvPolicy(cred Credential) (Credential, error) {
	cred, err := Normalize(cred)
	if err != nil {
		return Credential{}, err
	}
	cred.CostHardLimit = cred.CostHardLimit || CostHardLimitFromEnv()
	return cred, nil
}

func RequireComplete(cred Credential, baseURLError, apiKeyError string) (Credential, error) {
	cred, err := Normalize(cred)
	if err != nil {
		return Credential{}, err
	}
	if cred.BaseURL == "" {
		return Credential{}, fmt.Errorf("%s", baseURLError)
	}
	if cred.APIKey == "" {
		return Credential{}, fmt.Errorf("%s", apiKeyError)
	}
	if cred.Provider == ProviderOpenAI && cred.Transport == TransportBifrostAsync && !strings.HasSuffix(cred.BaseURL, "/openai") {
		return Credential{}, fmt.Errorf("bifrost_async via the OpenAI SDK requires %s to end in /openai", BaseURLEnv)
	}
	return cred, nil
}

func NormalizeTransportAndKind(rawTransport, rawKind string) (Transport, Kind, error) {
	kind := Kind(strings.ToLower(strings.TrimSpace(rawKind)))
	switch kind {
	case "":
	case KindOpenAI, KindBifrost:
	default:
		return "", "", fmt.Errorf("unknown %s %q (accepted: openai, bifrost)", KindEnv, rawKind)
	}
	transport := Transport(strings.ToLower(strings.TrimSpace(rawTransport)))
	switch transport {
	case "":
		// Neutral default. Managed sessions prefer bifrost_async; the managed
		// resolution paths pre-fill it before normalizing, so kind alone never
		// switches a BYO or env credential to the async transport.
		transport = TransportOpenAISync
	case TransportOpenAISync, TransportBifrostAsync:
	default:
		return "", "", fmt.Errorf("unknown %s %q (accepted: openai_sync, bifrost_async)", TransportEnv, rawTransport)
	}
	if kind == "" {
		if transport == TransportBifrostAsync {
			kind = KindBifrost
		} else {
			kind = KindOpenAI
		}
	}
	return transport, kind, nil
}

func ParseHeaders(raw string) (map[string]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var headers map[string]string
	if err := json.Unmarshal([]byte(raw), &headers); err != nil {
		return nil, fmt.Errorf("%s must be a JSON object of string values: %w", HeadersEnv, err)
	}
	return CloneHeaders(headers), nil
}

func CloneHeaders(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		out[k] = strings.TrimSpace(v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func CostHardLimitFromEnv() bool {
	raw := strings.TrimSpace(os.Getenv(CostHardLimitEnv))
	if raw != "" {
		return costHardLimitBool(raw)
	}
	return strings.TrimSpace(os.Getenv(BillingEnvIDEnv)) != "" &&
		(strings.TrimSpace(os.Getenv(BillingTokenEnv)) != "" ||
			strings.TrimSpace(os.Getenv(BillingFileEnv)) != "")
}

func costHardLimitBool(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

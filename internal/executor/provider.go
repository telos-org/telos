package executor

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// DefaultNativeRequestTimeoutSec bounds a single provider HTTP request (the full
// model response). The native executor otherwise drives requests with
// http.DefaultClient, which has no timeout, so a wedged provider request — the
// connection is accepted but the response never completes — hangs until the turn
// budget, which is itself unbounded when --agent-timeout-sec is 0. 30 minutes
// fails a hung request fast while leaving headroom for long reasoning
// generations. Override the process default with TELOS_NATIVE_REQUEST_TIMEOUT_SEC
// (or TELOS_MODEL_REQUEST_TIMEOUT_SEC) and per model via the capability profile's
// request_timeout_sec; a resolved value <= 0 disables the bound.
const DefaultNativeRequestTimeoutSec = 1800

// nativeConfig is the process-level executor configuration, resolved once from
// environment in NewNativeExecutor. It carries the base URL, API key, and the
// per-model capability table so that providerFor is a pure map lookup with no
// env parsing per turn or per model response.
type nativeConfig struct {
	baseURL           string
	apiKey            string
	transport         responseTransport
	kind              gatewayKind
	headers           map[string]string
	asyncPoll         asyncPollConfig
	capability        map[string]modelCapabilityProfile
	defaultCapability modelCapabilityProfile
}

// GatewayConfig is an explicit gateway credential supplied by the CLI or telosd.
// When empty, the executor falls back to TELOS_GATEWAY_* environment variables.
type GatewayConfig struct {
	BaseURL       string
	APIKey        string
	Transport     string
	Kind          string
	Headers       map[string]string
	CostHardLimit bool
}

type gatewayKind string

type responseTransport string

const (
	gatewayKindOpenAI  gatewayKind = "openai"
	gatewayKindBifrost gatewayKind = "bifrost"

	transportOpenAISync   responseTransport = "openai_sync"
	transportBifrostAsync responseTransport = "bifrost_async"
)

type asyncPollConfig struct {
	Initial          time.Duration
	Max              time.Duration
	ResultTTLSeconds int
}

type nativeGatewayInput struct {
	baseURL    string
	apiKey     string
	transport  string
	kind       string
	headers    map[string]string
	headersRaw string
}

func resolveNativeConfig() (nativeConfig, error) {
	return nativeConfigFromGatewayInput(nativeGatewayInput{
		baseURL:    os.Getenv("TELOS_GATEWAY_BASE_URL"),
		apiKey:     os.Getenv("TELOS_GATEWAY_API_KEY"),
		transport:  os.Getenv("TELOS_GATEWAY_TRANSPORT"),
		kind:       os.Getenv("TELOS_GATEWAY_KIND"),
		headersRaw: os.Getenv("TELOS_GATEWAY_HEADERS"),
	})
}

func costHardLimitFromEnv() bool {
	raw := strings.TrimSpace(os.Getenv("TELOS_COST_HARD_LIMIT"))
	if raw != "" {
		value, err := strconv.ParseBool(raw)
		return err == nil && value
	}
	return strings.TrimSpace(os.Getenv("TELOS_ENV_ID")) != "" &&
		(strings.TrimSpace(os.Getenv("TELOS_BILLING_ENV_TOKEN")) != "" ||
			strings.TrimSpace(os.Getenv("TELOS_BILLING_ENV_TOKEN_FILE")) != "")
}

func resolveNativeConfigWithGateway(gateway GatewayConfig) (nativeConfig, error) {
	if strings.TrimSpace(gateway.BaseURL) == "" && strings.TrimSpace(gateway.APIKey) == "" {
		return resolveNativeConfig()
	}
	return nativeConfigFromGatewayInput(nativeGatewayInput{
		baseURL:   gateway.BaseURL,
		apiKey:    gateway.APIKey,
		transport: gateway.Transport,
		kind:      gateway.Kind,
		headers:   gateway.Headers,
	})
}

func nativeConfigFromGatewayInput(input nativeGatewayInput) (nativeConfig, error) {
	base := strings.TrimRight(strings.TrimSpace(input.baseURL), "/")
	if base == "" {
		return nativeConfig{}, fmt.Errorf("TELOS_GATEWAY_BASE_URL is required")
	}
	key := strings.TrimSpace(input.apiKey)
	if key == "" {
		return nativeConfig{}, fmt.Errorf("TELOS_GATEWAY_API_KEY is required")
	}
	headers, err := resolveGatewayHeaders(input.headers, input.headersRaw)
	if err != nil {
		return nativeConfig{}, err
	}
	kind, err := resolveGatewayKind(input.kind)
	if err != nil {
		return nativeConfig{}, err
	}
	transport, err := resolveResponseTransport(input.transport, kind)
	if err != nil {
		return nativeConfig{}, err
	}
	if kind == "" {
		kind = kindForTransport(transport)
	}
	if transport == transportBifrostAsync && !strings.HasSuffix(base, "/openai") {
		return nativeConfig{}, fmt.Errorf("bifrost_async via the OpenAI SDK requires TELOS_GATEWAY_BASE_URL to end in /openai")
	}
	return nativeConfig{
		baseURL:           base,
		apiKey:            key,
		transport:         transport,
		kind:              kind,
		headers:           headers,
		asyncPoll:         asyncPollConfigFromEnv(),
		capability:        parseModelCapabilityTable(),
		defaultCapability: modelCapabilityProfileFromEnv(),
	}, nil
}

// providerFor returns the per-model provider config via map lookups — no env
// parsing. The model's capability is resolved as: capability-table[model] →
// the process default capability profile.
func (c nativeConfig) providerFor(model string) (nativeProviderConfig, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return nativeProviderConfig{}, fmt.Errorf("model is required")
	}
	cap := c.defaultCapability
	if specific, ok := c.capability[model]; ok {
		cap = specific
	}
	return nativeProviderConfig{
		Provider:   string(c.kind),
		Model:      model,
		BaseURL:    c.baseURL,
		APIKey:     c.apiKey,
		Transport:  c.transport,
		Kind:       c.kind,
		Headers:    cloneStringMap(c.headers),
		AsyncPoll:  c.asyncPoll,
		Capability: cap,
	}, nil
}

// requestTimeout resolves the per-request HTTP timeout for a model. Precedence:
// the model's capability-table request_timeout_sec → the process default
// capability profile (TELOS_MODEL_REQUEST_TIMEOUT_SEC) → TELOS_NATIVE_REQUEST_TIMEOUT_SEC
// → DefaultNativeRequestTimeoutSec. A non-zero capability value wins (including a
// negative value, which disables the bound for that model); a resolved value
// <= 0 returns 0, meaning "no timeout".
func (c nativeConfig) requestTimeout(model string) time.Duration {
	sec := 0
	if specific, ok := c.capability[strings.TrimSpace(model)]; ok && specific.RequestTimeoutSec != 0 {
		sec = specific.RequestTimeoutSec
	} else if c.defaultCapability.RequestTimeoutSec != 0 {
		sec = c.defaultCapability.RequestTimeoutSec
	} else {
		sec = nativeRequestTimeoutDefaultSec()
	}
	if sec <= 0 {
		return 0
	}
	return time.Duration(sec) * time.Second
}

// nativeRequestTimeoutDefaultSec is the process-wide request-timeout default,
// read from TELOS_NATIVE_REQUEST_TIMEOUT_SEC and falling back to
// DefaultNativeRequestTimeoutSec when unset or unparseable.
func nativeRequestTimeoutDefaultSec() int {
	if raw := strings.TrimSpace(os.Getenv("TELOS_NATIVE_REQUEST_TIMEOUT_SEC")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			return n
		}
	}
	return DefaultNativeRequestTimeoutSec
}

func resolveGatewayKind(raw string) (gatewayKind, error) {
	value := gatewayKind(strings.ToLower(strings.TrimSpace(raw)))
	switch value {
	case "":
		return "", nil
	case gatewayKindOpenAI, gatewayKindBifrost:
		return value, nil
	default:
		return "", fmt.Errorf("unknown TELOS_GATEWAY_KIND %q (accepted: openai, bifrost)", raw)
	}
}

func resolveResponseTransport(raw string, kind gatewayKind) (responseTransport, error) {
	value := responseTransport(strings.ToLower(strings.TrimSpace(raw)))
	switch value {
	case "":
		if kind == gatewayKindBifrost {
			return transportBifrostAsync, nil
		}
		return transportOpenAISync, nil
	case transportOpenAISync, transportBifrostAsync:
		return value, nil
	default:
		return "", fmt.Errorf("unknown TELOS_GATEWAY_TRANSPORT %q (accepted: openai_sync, bifrost_async)", raw)
	}
}

func kindForTransport(transport responseTransport) gatewayKind {
	if transport == transportBifrostAsync {
		return gatewayKindBifrost
	}
	return gatewayKindOpenAI
}

func resolveGatewayHeaders(headers map[string]string, raw string) (map[string]string, error) {
	if strings.TrimSpace(raw) != "" {
		var parsed map[string]string
		if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
			return nil, fmt.Errorf("TELOS_GATEWAY_HEADERS must be a JSON object of string values: %w", err)
		}
		return cloneStringMap(parsed), nil
	}
	return cloneStringMap(headers), nil
}

func cloneStringMap(in map[string]string) map[string]string {
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

func asyncPollConfigFromEnv() asyncPollConfig {
	return asyncPollConfig{
		Initial:          envDurationMS("TELOS_GATEWAY_ASYNC_POLL_INITIAL_MS", 500*time.Millisecond),
		Max:              envDurationMS("TELOS_GATEWAY_ASYNC_POLL_MAX_MS", 5*time.Second),
		ResultTTLSeconds: envIntDefault("TELOS_GATEWAY_ASYNC_JOB_RESULT_TTL_SEC", 3600),
	}
}

func envDurationMS(name string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return fallback
	}
	return time.Duration(n) * time.Millisecond
}

func envIntDefault(name string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return n
}

type nativeProviderConfig struct {
	Provider   string
	Model      string
	BaseURL    string
	APIKey     string
	Transport  responseTransport
	Kind       gatewayKind
	Headers    map[string]string
	AsyncPoll  asyncPollConfig
	Capability modelCapabilityProfile
}

type modelCapabilityProfile struct {
	StateMode               string `json:"state_mode,omitempty"`
	MaxOutputTokens         int    `json:"max_output_tokens,omitempty"`
	ContextWindow           int    `json:"context_window,omitempty"`
	RequestTimeoutSec       int    `json:"request_timeout_sec,omitempty"`
	SupportsReasoning       *bool  `json:"supports_reasoning,omitempty"`
	SupportsFunctionCalling *bool  `json:"supports_function_calling,omitempty"`
	StrictProtocol          bool   `json:"strict_protocol,omitempty"`
}

// effectiveContextWindow resolves the model's total context window in tokens.
// Precedence: an explicit capability value (table/env) wins; otherwise a
// built-in default keyed by the model family is used; 0 means "unknown" and
// leaves the compaction trigger at the configured global default.
func (p modelCapabilityProfile) effectiveContextWindow(model string) int {
	if p.ContextWindow > 0 {
		return p.ContextWindow
	}
	return builtinModelContextWindow(model)
}

// builtinModelContextWindow returns a conservative, well-established total
// context window for the model families Telos routes today, used only when the
// capability table/profile does not specify one (env always wins). Matching is
// substring-based on the gateway model alias/name. Values are the
// published total context windows as of early 2026; when a model's real window
// is unknown here, callers fall back to the global compaction default.
//
// Because the effective compaction window is floor(global default, this value),
// families at or above the 128k default are unaffected in practice — the table
// matters most for models whose window is *below* the default.
func builtinModelContextWindow(model string) int {
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case m == "":
		return 0
	// Anthropic Claude 4.x (opus/sonnet/haiku): 200k context.
	case strings.Contains(m, "claude"), strings.Contains(m, "opus"), strings.Contains(m, "sonnet"), strings.Contains(m, "haiku"):
		return 200000
	// OpenAI GPT-5.x and GPT-4.1 / o-series reasoning models.
	case strings.Contains(m, "gpt-4.1"):
		return 1000000
	case strings.Contains(m, "gpt-5"):
		return 400000
	case strings.Contains(m, "o3"), strings.Contains(m, "o4"):
		return 200000
	// Older OpenAI 128k-class models.
	case strings.Contains(m, "gpt-4o"), strings.Contains(m, "gpt-4-turbo"):
		return 128000
	case strings.Contains(m, "gpt-4-32k"):
		return 32768
	case strings.Contains(m, "gpt-3.5"):
		return 16385
	// Google Gemini 1.5/2.x long-context models.
	case strings.Contains(m, "gemini"):
		return 1000000
	default:
		return 0
	}
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
	if raw := strings.TrimSpace(os.Getenv("TELOS_MODEL_CONTEXT_WINDOW")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			profile.ContextWindow = n
		}
	}
	if raw := strings.TrimSpace(os.Getenv("TELOS_MODEL_REQUEST_TIMEOUT_SEC")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			profile.RequestTimeoutSec = n
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

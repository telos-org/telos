// Package config handles ~/.telos/config.yaml and environments.yaml.
package config

import (
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

const (
	ConfigPathEnv       = "TELOS_CONFIG"
	EnvironmentsPathEnv = "TELOS_ENVIRONMENTS_CONFIG"
	APIEndpointEnv      = "TELOS_API_ENDPOINT"
	BillingEndpointEnv  = "TELOS_BILLING_ENDPOINT"
	AuthTokenEnv        = "TELOS_AUTH_TOKEN"
	GatewayModeEnv      = "TELOS_GATEWAY_MODE"
	GatewayBaseURLEnv   = "TELOS_GATEWAY_BASE_URL"
	GatewayAPIKeyEnv    = "TELOS_GATEWAY_API_KEY"
	GatewayTransportEnv = "TELOS_GATEWAY_TRANSPORT"
	GatewayKindEnv      = "TELOS_GATEWAY_KIND"
	GatewayHeadersEnv   = "TELOS_GATEWAY_HEADERS"
)

// Config holds user-facing cloud CLI configuration.
type Config struct {
	APIEndpoint     string        `yaml:"api_endpoint,omitempty"`
	BillingEndpoint string        `yaml:"billing_endpoint,omitempty"`
	AuthToken       string        `yaml:"auth_token,omitempty"`
	Gateway         GatewayConfig `yaml:"gateway,omitempty"`
}

// GatewayConfig holds local model gateway selection.
type GatewayConfig struct {
	Mode      string            `yaml:"mode,omitempty"`
	BaseURL   string            `yaml:"base_url,omitempty"`
	APIKey    string            `yaml:"api_key,omitempty"`
	Transport string            `yaml:"transport,omitempty"`
	Kind      string            `yaml:"kind,omitempty"`
	Headers   map[string]string `yaml:"headers,omitempty"`
}

// EnvironmentAccess holds a saved scoped token for one cloud environment.
type EnvironmentAccess struct {
	ID    string
	Token string
}

// ConfigPath returns the path to the active config file.
func ConfigPath() string {
	if override := os.Getenv(ConfigPathEnv); override != "" {
		return override
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".telos", "config.yaml")
}

// EnvironmentsPath returns the path to saved environment credentials.
func EnvironmentsPath() string {
	if override := os.Getenv(EnvironmentsPathEnv); override != "" {
		return override
	}
	dir := filepath.Dir(ConfigPath())
	return filepath.Join(dir, "environments.yaml")
}

// LoadConfig reads config from disk with env overrides.
func LoadConfig() *Config {
	raw := readYAMLFile(ConfigPath())
	cfg := &Config{}
	if ep, ok := raw["api_endpoint"].(string); ok {
		cfg.APIEndpoint = ep
	}
	if ep, ok := raw["billing_endpoint"].(string); ok {
		cfg.BillingEndpoint = ep
	}
	if at, ok := raw["auth_token"].(string); ok {
		cfg.AuthToken = at
	}
	if rawGateway, ok := raw["gateway"].(map[string]interface{}); ok {
		if mode, ok := rawGateway["mode"].(string); ok {
			cfg.Gateway.Mode = mode
		}
		if baseURL, ok := rawGateway["base_url"].(string); ok {
			cfg.Gateway.BaseURL = baseURL
		}
		if apiKey, ok := rawGateway["api_key"].(string); ok {
			cfg.Gateway.APIKey = apiKey
		}
		if transport, ok := rawGateway["transport"].(string); ok {
			cfg.Gateway.Transport = transport
		}
		if kind, ok := rawGateway["kind"].(string); ok {
			cfg.Gateway.Kind = kind
		}
		if headers, ok := stringMap(rawGateway["headers"]); ok {
			cfg.Gateway.Headers = headers
		}
	}
	// Env overrides
	if v := os.Getenv(APIEndpointEnv); v != "" {
		cfg.APIEndpoint = v
	}
	if v := os.Getenv(BillingEndpointEnv); v != "" {
		cfg.BillingEndpoint = v
	}
	if v := os.Getenv(AuthTokenEnv); v != "" {
		cfg.AuthToken = v
	}
	if v := os.Getenv(GatewayModeEnv); v != "" {
		cfg.Gateway.Mode = v
	}
	if v := os.Getenv(GatewayBaseURLEnv); v != "" {
		cfg.Gateway.BaseURL = v
	}
	if v := os.Getenv(GatewayAPIKeyEnv); v != "" {
		cfg.Gateway.APIKey = v
	}
	if v := os.Getenv(GatewayTransportEnv); v != "" {
		cfg.Gateway.Transport = v
	}
	if v := os.Getenv(GatewayKindEnv); v != "" {
		cfg.Gateway.Kind = v
	}
	if v := os.Getenv(GatewayHeadersEnv); v != "" {
		var headers map[string]string
		if err := yaml.Unmarshal([]byte(v), &headers); err == nil {
			cfg.Gateway.Headers = headers
		}
	}
	return cfg
}

// SaveConfig writes config to disk.
func SaveConfig(cfg *Config) error {
	path := ConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	m := map[string]any{}
	if cfg.APIEndpoint != "" {
		m["api_endpoint"] = cfg.APIEndpoint
	}
	if cfg.BillingEndpoint != "" {
		m["billing_endpoint"] = cfg.BillingEndpoint
	}
	if cfg.AuthToken != "" {
		m["auth_token"] = cfg.AuthToken
	}
	if cfg.Gateway.Mode != "" || cfg.Gateway.BaseURL != "" || cfg.Gateway.APIKey != "" || cfg.Gateway.Transport != "" || cfg.Gateway.Kind != "" || len(cfg.Gateway.Headers) > 0 {
		m["gateway"] = map[string]any{}
		gateway := m["gateway"].(map[string]any)
		if cfg.Gateway.Mode != "" {
			gateway["mode"] = cfg.Gateway.Mode
		}
		if cfg.Gateway.BaseURL != "" {
			gateway["base_url"] = cfg.Gateway.BaseURL
		}
		if cfg.Gateway.APIKey != "" {
			gateway["api_key"] = cfg.Gateway.APIKey
		}
		if cfg.Gateway.Transport != "" {
			gateway["transport"] = cfg.Gateway.Transport
		}
		if cfg.Gateway.Kind != "" {
			gateway["kind"] = cfg.Gateway.Kind
		}
		if len(cfg.Gateway.Headers) > 0 {
			gateway["headers"] = cfg.Gateway.Headers
		}
	}
	data, err := yaml.Marshal(m)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	return nil
}

// LoadEnvironmentAccess reads saved cloud environment access tokens.
func LoadEnvironmentAccess() []EnvironmentAccess {
	raw := readYAMLFile(EnvironmentsPath())
	entries, ok := raw["environments"]
	if !ok || entries == nil {
		return nil
	}
	switch v := entries.(type) {
	case map[string]interface{}:
		var result []EnvironmentAccess
		for id, token := range v {
			if s, ok := token.(string); ok && id != "" && s != "" {
				result = append(result, EnvironmentAccess{ID: id, Token: s})
			}
		}
		sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
		return result
	case []interface{}:
		var result []EnvironmentAccess
		for _, entry := range v {
			m, ok := entry.(map[string]interface{})
			if !ok {
				continue
			}
			id, _ := m["id"].(string)
			token, _ := m["access_token"].(string)
			if token == "" {
				token, _ = m["env_api_key"].(string)
			}
			if id != "" && token != "" {
				result = append(result, EnvironmentAccess{ID: id, Token: token})
			}
		}
		sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
		return result
	}
	return nil
}

// EnvironmentAccessByID returns the saved scoped token for an environment, if any.
func EnvironmentAccessByID(envID string) (EnvironmentAccess, bool) {
	for _, env := range LoadEnvironmentAccess() {
		if env.ID == envID {
			return env, true
		}
	}
	return EnvironmentAccess{}, false
}

// SaveEnvironmentAccess writes saved environment access tokens.
func SaveEnvironmentAccess(envs []EnvironmentAccess) error {
	path := EnvironmentsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	m := map[string]string{}
	for _, e := range envs {
		m[e.ID] = e.Token
	}
	data, err := yaml.Marshal(map[string]interface{}{"environments": m})
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// SaveEnvironmentAccessEntry upserts one environment access token.
func SaveEnvironmentAccessEntry(entry EnvironmentAccess) error {
	if entry.ID == "" || entry.Token == "" {
		return nil
	}
	byID := map[string]EnvironmentAccess{}
	for _, env := range LoadEnvironmentAccess() {
		byID[env.ID] = env
	}
	byID[entry.ID] = entry
	keys := make([]string, 0, len(byID))
	for id := range byID {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	envs := make([]EnvironmentAccess, 0, len(keys))
	for _, id := range keys {
		envs = append(envs, byID[id])
	}
	return SaveEnvironmentAccess(envs)
}

// IsConfigured returns true if the user has configured cloud access.
func IsConfigured() bool {
	return LoadConfig().AuthToken != ""
}

func readYAMLFile(path string) map[string]interface{} {
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]interface{}{}
	}
	var raw map[string]interface{}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return map[string]interface{}{}
	}
	if raw == nil {
		return map[string]interface{}{}
	}
	return raw
}

func stringMap(value any) (map[string]string, bool) {
	raw, ok := value.(map[string]interface{})
	if !ok {
		return nil, false
	}
	out := make(map[string]string, len(raw))
	for key, value := range raw {
		s, ok := value.(string)
		if !ok || key == "" {
			return nil, false
		}
		out[key] = s
	}
	return out, true
}

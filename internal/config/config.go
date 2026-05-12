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
	AuthTokenEnv        = "TELOS_AUTH_TOKEN"
)

// Config holds user-facing hosted CLI configuration.
type Config struct {
	APIEndpoint string `yaml:"api_endpoint,omitempty"`
	AuthToken   string `yaml:"auth_token,omitempty"`
}

// EnvironmentAccess holds saved credentials for one hosted environment.
type EnvironmentAccess struct {
	ID        string
	EnvAPIKey string
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
	if at, ok := raw["auth_token"].(string); ok {
		cfg.AuthToken = at
	}
	// Env overrides
	if v := os.Getenv(APIEndpointEnv); v != "" {
		cfg.APIEndpoint = v
	}
	if v := os.Getenv(AuthTokenEnv); v != "" {
		cfg.AuthToken = v
	}
	return cfg
}

// SaveConfig writes config to disk.
func SaveConfig(cfg *Config) error {
	path := ConfigPath()
	os.MkdirAll(filepath.Dir(path), 0o755)
	m := map[string]string{}
	if cfg.APIEndpoint != "" {
		m["api_endpoint"] = cfg.APIEndpoint
	}
	if cfg.AuthToken != "" {
		m["auth_token"] = cfg.AuthToken
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

// LoadEnvironmentAccess reads saved hosted environment credentials.
func LoadEnvironmentAccess() []EnvironmentAccess {
	raw := readYAMLFile(EnvironmentsPath())
	entries, ok := raw["environments"]
	if !ok || entries == nil {
		return nil
	}
	switch v := entries.(type) {
	case map[string]interface{}:
		var result []EnvironmentAccess
		for id, key := range v {
			if s, ok := key.(string); ok && id != "" && s != "" {
				result = append(result, EnvironmentAccess{ID: id, EnvAPIKey: s})
			}
		}
		sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
		return result
	}
	return nil
}

// EnvironmentAccessByID returns the saved API key for an environment, if any.
func EnvironmentAccessByID(envID string) (EnvironmentAccess, bool) {
	for _, env := range LoadEnvironmentAccess() {
		if env.ID == envID {
			return env, true
		}
	}
	return EnvironmentAccess{}, false
}

// SaveEnvironmentAccess writes saved environment credentials.
func SaveEnvironmentAccess(envs []EnvironmentAccess) error {
	path := EnvironmentsPath()
	os.MkdirAll(filepath.Dir(path), 0o755)
	m := map[string]string{}
	for _, e := range envs {
		m[e.ID] = e.EnvAPIKey
	}
	data, err := yaml.Marshal(map[string]interface{}{"environments": m})
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// SaveEnvironmentAccessEntry upserts one environment credential.
func SaveEnvironmentAccessEntry(entry EnvironmentAccess) error {
	if entry.ID == "" || entry.EnvAPIKey == "" {
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

// IsConfigured returns true if the user has configured hosted access.
func IsConfigured() bool {
	if os.Getenv(APIEndpointEnv) != "" || os.Getenv(AuthTokenEnv) != "" {
		return true
	}
	if _, err := os.Stat(ConfigPath()); err == nil {
		return true
	}
	return false
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

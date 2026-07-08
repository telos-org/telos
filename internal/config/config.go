// Package config handles ~/.telos/config.yaml.
package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const (
	ConfigPathEnv  = "TELOS_CONFIG"
	APIEndpointEnv = "TELOS_API_ENDPOINT"
	AuthTokenEnv   = "TELOS_AUTH_TOKEN"
	OrgIDEnv       = "TELOS_ORG_ID"
)

// Config holds user-facing cloud CLI configuration.
type Config struct {
	APIEndpoint string `yaml:"api_endpoint,omitempty"`
	AuthToken   string `yaml:"auth_token,omitempty"`
	OrgID       string `yaml:"org_id,omitempty"`
}

// ConfigPath returns the path to the active config file.
func ConfigPath() string {
	if override := os.Getenv(ConfigPathEnv); override != "" {
		return override
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".telos", "config.yaml")
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
	if orgID, ok := raw["org_id"].(string); ok {
		cfg.OrgID = orgID
	}
	// Env overrides
	if v := os.Getenv(APIEndpointEnv); v != "" {
		cfg.APIEndpoint = v
	}
	if v := os.Getenv(AuthTokenEnv); v != "" {
		cfg.AuthToken = v
	}
	if v := os.Getenv(OrgIDEnv); v != "" {
		cfg.OrgID = v
	}
	return cfg
}

// SaveConfig writes config to disk.
func SaveConfig(cfg *Config) error {
	path := ConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	m := map[string]string{}
	if cfg.APIEndpoint != "" {
		m["api_endpoint"] = cfg.APIEndpoint
	}
	if cfg.AuthToken != "" {
		m["auth_token"] = cfg.AuthToken
	}
	if cfg.OrgID != "" {
		m["org_id"] = cfg.OrgID
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

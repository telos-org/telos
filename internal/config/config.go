// Package config handles ~/.telos/config.yaml.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	ConfigPathEnv  = "TELOS_CONFIG"
	APIEndpointEnv = "TELOS_API_ENDPOINT"
	AuthTokenEnv   = "TELOS_AUTH_TOKEN"
	ContextEnv     = "TELOS_CONTEXT"
)

// Config holds user-facing cloud CLI configuration.
type Config struct {
	APIEndpoint  string `yaml:"api_endpoint,omitempty"`
	AuthToken    string `yaml:"auth_token,omitempty"`
	Context      string `yaml:"context,omitempty"`
	DefaultModel string `yaml:"default_model,omitempty"`
}

// ConfigPath returns the path to the active config file.
func ConfigPath() (string, error) {
	if override := strings.TrimSpace(os.Getenv(ConfigPathEnv)); override != "" {
		return override, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory for Telos config: %w", err)
	}
	if strings.TrimSpace(home) == "" {
		return "", errors.New("resolve home directory for Telos config: empty path")
	}
	return filepath.Join(home, ".telos", "config.yaml"), nil
}

// LoadStoredConfig reads config from disk without environment overrides.
func LoadStoredConfig() (*Config, error) {
	path, err := ConfigPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Config{}, nil
		}
		return nil, fmt.Errorf("read Telos config %s: %w", path, err)
	}

	cfg := &Config{}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return cfg, nil
		}
		return nil, fmt.Errorf("parse Telos config %s: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return nil, fmt.Errorf("parse Telos config %s: multiple YAML documents are not supported", path)
	} else if !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse Telos config %s: %w", path, err)
	}
	return cfg, nil
}

// LoadConfig reads stored config with environment overrides.
func LoadConfig() (*Config, error) {
	cfg, err := LoadStoredConfig()
	if err != nil {
		return nil, err
	}
	if value := os.Getenv(APIEndpointEnv); value != "" {
		cfg.APIEndpoint = value
	}
	if value := os.Getenv(AuthTokenEnv); value != "" {
		cfg.AuthToken = value
	}
	if value := os.Getenv(ContextEnv); value != "" {
		cfg.Context = value
	}
	return cfg, nil
}

// SaveConfig atomically writes config to disk with credential-safe permissions.
func SaveConfig(cfg *Config) error {
	if cfg == nil {
		return errors.New("save Telos config: config is nil")
	}
	path, err := ConfigPath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create Telos config directory %s: %w", dir, err)
	}
	if strings.TrimSpace(os.Getenv(ConfigPathEnv)) == "" {
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("secure Telos config directory %s: %w", dir, err)
		}
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encode Telos config: %w", err)
	}
	return writeConfigAtomic(path, data)
}

// IsConfigured reports whether cloud credentials are available.
func IsConfigured() (bool, error) {
	cfg, err := LoadConfig()
	if err != nil {
		return false, err
	}
	return cfg.AuthToken != "", nil
}

func writeConfigAtomic(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".config.yaml.*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary Telos config: %w", err)
	}
	temporaryPath := file.Name()
	defer os.Remove(temporaryPath)

	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("secure temporary Telos config: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write temporary Telos config: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("flush temporary Telos config: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close temporary Telos config: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace Telos config %s: %w", path, err)
	}
	return nil
}

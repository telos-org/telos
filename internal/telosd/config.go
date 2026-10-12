package telosd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const ConfigKind = "telosd.config.v1"

// Config configures the telosd server inside a Telos Cloud environment. Local
// runs start telosd only as a session worker, which needs no config.
type Config struct {
	Kind      string        `yaml:"kind"`
	Root      string        `yaml:"root"`
	Token     string        `yaml:"token"`
	TokenFile string        `yaml:"token_file"`
	Server    ServerConfig  `yaml:"server"`
	Auth      AuthConfig    `yaml:"auth"`
	Runtime   RuntimeConfig `yaml:"runtime"`
}

type ServerConfig struct {
	Listen string `yaml:"listen"`
}

type AuthConfig struct {
	Token     string `yaml:"token"`
	TokenFile string `yaml:"token_file"`
}

type RuntimeConfig struct {
	ArtifactBaseURL string `yaml:"artifact_base_url"`
	ArtifactVersion string `yaml:"artifact_version"`
	MountPath       string `yaml:"mount_path"`
}

func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	return NormalizeConfig(cfg)
}

func NormalizeConfig(cfg Config) (Config, error) {
	if cfg.Kind == "" {
		cfg.Kind = ConfigKind
	}
	if cfg.Kind != ConfigKind {
		return Config{}, fmt.Errorf("unsupported config kind %q", cfg.Kind)
	}
	if cfg.Auth.Token == "" {
		cfg.Auth.Token = cfg.Token
	}
	if cfg.Auth.TokenFile == "" {
		cfg.Auth.TokenFile = cfg.TokenFile
	}
	if cfg.Root == "" {
		cfg.Root = "/telos-state"
	}
	if cfg.Server.Listen == "" {
		cfg.Server.Listen = "0.0.0.0:8000"
	}
	if cfg.Runtime.ArtifactBaseURL == "" {
		cfg.Runtime.ArtifactBaseURL = "https://storage.googleapis.com/telos-runtime-artifacts/releases"
	}
	if cfg.Runtime.ArtifactVersion == "" {
		cfg.Runtime.ArtifactVersion = "latest"
	}
	if cfg.Runtime.MountPath == "" {
		cfg.Runtime.MountPath = "/telos-runtime"
	}
	if cfg.Auth.Token == "" {
		token, err := authTokenFromFile(cfg.Auth.TokenFile)
		if err != nil {
			return Config{}, err
		}
		cfg.Auth.Token = token
	}
	if cfg.Auth.Token == "" {
		cfg.Auth.Token = os.Getenv("TELOS_API_TOKEN")
	}
	if cfg.Auth.Token == "" {
		return Config{}, fmt.Errorf("auth.token is required for bearer auth")
	}
	return cfg, nil
}

func SessionsRoot(root string) string {
	return filepath.Join(root, "sessions")
}

func authTokenFromFile(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read auth.token_file: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

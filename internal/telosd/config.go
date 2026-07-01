package telosd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const ConfigKind = "telosd.config.v1"

type Mode string

const (
	ModeLocal Mode = "local"
	ModeCloud Mode = "cloud"
)

type AuthType string

const (
	AuthLocal  AuthType = "local"
	AuthBearer AuthType = "bearer"
)

type Config struct {
	Kind      string        `yaml:"kind"`
	Mode      Mode          `yaml:"mode"`
	Root      string        `yaml:"root"`
	Token     string        `yaml:"token"`
	TokenFile string        `yaml:"token_file"`
	Server    ServerConfig  `yaml:"server"`
	Auth      AuthConfig    `yaml:"auth"`
	Runtime   RuntimeConfig `yaml:"runtime"`
}

type ServerConfig struct {
	Transport   string `yaml:"transport"`
	Listen      string `yaml:"listen"`
	Socket      string `yaml:"socket"`
	IdleSeconds int    `yaml:"idle_seconds"`
}

type AuthConfig struct {
	Type      AuthType `yaml:"type"`
	Token     string   `yaml:"token"`
	TokenFile string   `yaml:"token_file"`
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

func DefaultConfig(mode Mode) Config {
	cfg := Config{
		Kind: ConfigKind,
		Mode: mode,
	}
	normalized, _ := NormalizeConfig(cfg)
	return normalized
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
	if cfg.Mode == "" {
		cfg.Mode = ModeLocal
	}
	switch cfg.Mode {
	case ModeLocal:
		if cfg.Root == "" {
			cfg.Root = ".telos"
		}
		if cfg.Auth.Type == "" {
			cfg.Auth.Type = AuthLocal
		}
		if cfg.Server.Transport == "" {
			cfg.Server.Transport = "unix"
		}
		if cfg.Server.Socket == "" {
			cfg.Server.Socket = filepath.Join(cfg.Root, "run", "telosd.sock")
		}
		if cfg.Server.IdleSeconds == 0 {
			cfg.Server.IdleSeconds = 300
		}
	case ModeCloud:
		if cfg.Root == "" {
			cfg.Root = "/telos-state"
		}
		if cfg.Auth.Type == "" {
			cfg.Auth.Type = AuthBearer
		}
		if cfg.Server.Transport == "" {
			cfg.Server.Transport = "http"
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
	default:
		return Config{}, fmt.Errorf("invalid mode %q", cfg.Mode)
	}
	if cfg.Mode == ModeLocal && cfg.Auth.Type != AuthLocal {
		return Config{}, fmt.Errorf("local mode requires auth.type %q", AuthLocal)
	}
	if cfg.Mode == ModeCloud && cfg.Auth.Type != AuthBearer {
		return Config{}, fmt.Errorf("cloud mode requires auth.type %q", AuthBearer)
	}
	if cfg.Mode == ModeLocal && cfg.Server.Transport != "unix" {
		return Config{}, fmt.Errorf("local mode requires server.transport %q", "unix")
	}
	if cfg.Mode == ModeCloud && cfg.Server.Transport != "http" {
		return Config{}, fmt.Errorf("cloud mode requires server.transport %q", "http")
	}
	if cfg.Auth.Type != AuthLocal && cfg.Auth.Type != AuthBearer {
		return Config{}, fmt.Errorf("invalid auth.type %q", cfg.Auth.Type)
	}
	if cfg.Auth.Type == AuthBearer {
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
	}
	switch cfg.Server.Transport {
	case "unix":
		if cfg.Server.Socket == "" {
			return Config{}, fmt.Errorf("server.socket is required for unix transport")
		}
	case "http":
		if cfg.Server.Listen == "" {
			return Config{}, fmt.Errorf("server.listen is required for http transport")
		}
	default:
		return Config{}, fmt.Errorf("invalid server.transport %q", cfg.Server.Transport)
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

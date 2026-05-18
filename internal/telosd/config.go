package telosd

import (
	"fmt"
	"os"
	"path/filepath"

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
	Kind   string       `yaml:"kind"`
	Mode   Mode         `yaml:"mode"`
	Root   string       `yaml:"root"`
	Server ServerConfig `yaml:"server"`
	Auth   AuthConfig   `yaml:"auth"`
}

type ServerConfig struct {
	Transport   string `yaml:"transport"`
	Listen      string `yaml:"listen"`
	Socket      string `yaml:"socket"`
	IdleSeconds int    `yaml:"idle_seconds"`
}

type AuthConfig struct {
	Type  AuthType `yaml:"type"`
	Token string   `yaml:"token"`
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

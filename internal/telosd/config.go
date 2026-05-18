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

type Config struct {
	Kind   string       `yaml:"kind"`
	Mode   Mode         `yaml:"mode"`
	Root   string       `yaml:"root"`
	Server ServerConfig `yaml:"server"`
	Access string       `yaml:"access"`
}

type ServerConfig struct {
	Transport   string `yaml:"transport"`
	Listen      string `yaml:"listen"`
	Socket      string `yaml:"socket"`
	IdleSeconds int    `yaml:"idle_seconds"`
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
		if cfg.Access == "" {
			cfg.Access = "local"
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
		if cfg.Access == "" {
			cfg.Access = "bearer"
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
	if cfg.Access != "local" && cfg.Access != "bearer" {
		return Config{}, fmt.Errorf("invalid access %q", cfg.Access)
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

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
	Kind            string           `yaml:"kind"`
	Mode            Mode             `yaml:"mode"`
	Root            string           `yaml:"root"`
	Token           string           `yaml:"token"`
	TokenFile       string           `yaml:"token_file"`
	AgentImage      string           `yaml:"agent_image"`
	ImagePullSecret string           `yaml:"image_pull_secret"`
	Server          ServerConfig     `yaml:"server"`
	Auth            AuthConfig       `yaml:"auth"`
	Runtime         RuntimeConfig    `yaml:"runtime"`
	Kubernetes      KubernetesConfig `yaml:"kubernetes"`
	ControlPlane    ControlConfig    `yaml:"control_plane"`
	Billing         BillingConfig    `yaml:"billing"`
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

type ControlConfig struct {
	Endpoint  string `yaml:"endpoint"`
	EnvID     string `yaml:"env_id"`
	Token     string `yaml:"token"`
	TokenFile string `yaml:"token_file"`
}

type BillingConfig struct {
	Endpoint  string `yaml:"endpoint"`
	EnvID     string `yaml:"env_id"`
	Token     string `yaml:"token"`
	TokenFile string `yaml:"token_file"`
}

type KubernetesConfig struct {
	AgentImage      string   `yaml:"agent_image"`
	EnvNamespace    string   `yaml:"env_namespace"`
	StateMountRoot  string   `yaml:"state_mount_root"`
	StateHostRoot   string   `yaml:"state_host_root"`
	StateNodeRoot   string   `yaml:"state_node_root"`
	ImagePullSecret string   `yaml:"image_pull_secret"`
	AgentSecretName string   `yaml:"agent_secret_name"`
	AgentSecretKey  string   `yaml:"agent_secret_key"`
	CopySecrets     []string `yaml:"copy_secrets"`
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
	if cfg.Kubernetes.AgentImage == "" {
		cfg.Kubernetes.AgentImage = cfg.AgentImage
	}
	if cfg.Kubernetes.ImagePullSecret == "" {
		cfg.Kubernetes.ImagePullSecret = cfg.ImagePullSecret
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
			cfg.Runtime.ArtifactBaseURL = "https://usetelos.ai/releases"
		}
		if cfg.Runtime.ArtifactVersion == "" {
			cfg.Runtime.ArtifactVersion = "latest"
		}
		if cfg.Runtime.MountPath == "" {
			cfg.Runtime.MountPath = "/telos-runtime"
		}
		if cfg.ControlPlane.Endpoint == "" {
			cfg.ControlPlane.Endpoint = envFirst(
				[]string{"TELOS_CONTROL_ENDPOINT", "TELOS_CONTROL_API_URL"},
				"https://api.usetelos.ai",
			)
		}
		if cfg.ControlPlane.EnvID == "" {
			cfg.ControlPlane.EnvID = os.Getenv("TELOS_ENV_ID")
		}
		if cfg.ControlPlane.TokenFile == "" {
			cfg.ControlPlane.TokenFile = os.Getenv("TELOS_CONTROL_TOKEN_FILE")
		}
		if cfg.ControlPlane.Token == "" {
			if token, err := authTokenFromFile(cfg.ControlPlane.TokenFile); err != nil {
				return Config{}, fmt.Errorf("read control_plane.token_file: %w", err)
			} else if token != "" {
				cfg.ControlPlane.Token = token
			}
		}
		if cfg.ControlPlane.Token == "" {
			cfg.ControlPlane.Token = os.Getenv("TELOS_CONTROL_TOKEN")
		}
		if cfg.Billing.Endpoint == "" {
			cfg.Billing.Endpoint = envOr("TELOS_BILLING_ENDPOINT", "https://billing.usetelos.ai")
		}
		if cfg.Billing.EnvID == "" {
			cfg.Billing.EnvID = os.Getenv("TELOS_ENV_ID")
		}
		if cfg.Billing.TokenFile == "" {
			cfg.Billing.TokenFile = os.Getenv("TELOS_BILLING_ENV_TOKEN_FILE")
		}
		if cfg.Billing.Token == "" {
			if token, err := authTokenFromFile(cfg.Billing.TokenFile); err != nil {
				return Config{}, fmt.Errorf("read billing.token_file: %w", err)
			} else if token != "" {
				cfg.Billing.Token = token
			}
		}
		if cfg.Billing.Token == "" {
			cfg.Billing.Token = os.Getenv("TELOS_BILLING_ENV_TOKEN")
		}
		if cfg.Billing.Endpoint != "" && cfg.Billing.EnvID != "" && cfg.Billing.Token == "" {
			return Config{}, fmt.Errorf("billing.token is required when cloud billing is configured")
		}
		if cfg.Kubernetes.AgentImage == "" {
			cfg.Kubernetes.AgentImage = envOr("TELOS_AGENT_IMAGE", "telos-agent:latest")
		}
		if cfg.Kubernetes.EnvNamespace == "" {
			cfg.Kubernetes.EnvNamespace = envOr("TELOS_ENV_NAMESPACE", "ns-telos-env")
		}
		if cfg.Kubernetes.StateMountRoot == "" {
			cfg.Kubernetes.StateMountRoot = envOr("TELOS_STATE_MOUNT_ROOT", cfg.Root)
		}
		if cfg.Kubernetes.StateHostRoot == "" {
			cfg.Kubernetes.StateHostRoot = envOr("TELOS_STATE_HOST_ROOT", "/var/telos-state")
		}
		if cfg.Kubernetes.StateNodeRoot == "" {
			cfg.Kubernetes.StateNodeRoot = envOr("TELOS_STATE_NODE_ROOT", "/var/telos-state")
		}
		if cfg.Kubernetes.ImagePullSecret == "" {
			cfg.Kubernetes.ImagePullSecret = defaultImagePullSecret(cfg.Kubernetes.AgentImage)
		}
		if cfg.Kubernetes.AgentSecretName == "" {
			cfg.Kubernetes.AgentSecretName = "agent-api-keys"
		}
		if cfg.Kubernetes.AgentSecretKey == "" {
			cfg.Kubernetes.AgentSecretKey = "TELOS_GATEWAY_API_KEY"
		}
		if cfg.Kubernetes.CopySecrets == nil {
			cfg.Kubernetes.CopySecrets = []string{"telos-env-keys"}
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
		if cfg.Mode == ModeCloud && cfg.ControlPlane.Token == "" {
			cfg.ControlPlane.Token = cfg.Auth.Token
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

func defaultImagePullSecret(agentImage string) string {
	if value := strings.TrimSpace(os.Getenv("TELOS_IMAGE_PULL_SECRET")); value != "" {
		return value
	}
	if strings.Contains(agentImage, ".pkg.dev/") {
		return "gar-pull"
	}
	return ""
}

func envFirst(names []string, fallback string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return fallback
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

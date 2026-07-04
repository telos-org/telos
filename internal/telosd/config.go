package telosd

import (
	"errors"
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
	ArtifactBaseURL string                  `yaml:"artifact_base_url"`
	ArtifactVersion string                  `yaml:"artifact_version"`
	Artifacts       []RuntimeArtifactConfig `yaml:"artifacts"`
	MountPath       string                  `yaml:"mount_path"`
}

type RuntimeArtifactConfig struct {
	Name   string `yaml:"name"`
	OS     string `yaml:"os"`
	Arch   string `yaml:"arch"`
	SHA256 string `yaml:"sha256"`
}

type ServiceCredentialConfig struct {
	Endpoint  string `yaml:"endpoint"`
	EnvID     string `yaml:"env_id"`
	Token     string `yaml:"token"`
	TokenFile string `yaml:"token_file"`
}

type ControlConfig = ServiceCredentialConfig

type BillingConfig = ServiceCredentialConfig

type KubernetesConfig struct {
	AgentImage         string   `yaml:"agent_image"`
	EnvNamespace       string   `yaml:"env_namespace"`
	StateMountRoot     string   `yaml:"state_mount_root"`
	StateHostRoot      string   `yaml:"state_host_root"`
	StateNodeRoot      string   `yaml:"state_node_root"`
	StateStorageClass  string   `yaml:"state_storage_class"`
	StateStorageSize   string   `yaml:"state_storage_size"`
	AllowHostPathState bool     `yaml:"allow_host_path_state"`
	WorkerEgressCIDRs  []string `yaml:"worker_egress_cidrs"`
	ImagePullSecret    string   `yaml:"image_pull_secret"`
	AgentSecretName    string   `yaml:"agent_secret_name"`
	AgentSecretKey     string   `yaml:"agent_secret_key"`
	CopySecrets        []string `yaml:"copy_secrets"`
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
			cfg.Runtime.ArtifactVersion = envOr("TELOS_RUNTIME_VERSION", "latest")
		}
		if len(cfg.Runtime.Artifacts) == 0 {
			cfg.Runtime.Artifacts = runtimeArtifactsFromEnv()
		}
		if cfg.Runtime.MountPath == "" {
			cfg.Runtime.MountPath = "/telos-runtime"
		}
		var err error
		cfg.ControlPlane, err = resolveServiceCredential(cfg.ControlPlane, serviceCredentialEnv{
			Name:          "control_plane",
			EndpointVars:  []string{"TELOS_CONTROL_ENDPOINT", "TELOS_CONTROL_API_URL"},
			EndpointValue: "https://api.usetelos.ai",
			TokenFileVar:  "TELOS_CONTROL_TOKEN_FILE",
			TokenVar:      "TELOS_CONTROL_TOKEN",
		})
		if err != nil {
			return Config{}, err
		}
		cfg.Billing, err = resolveServiceCredential(cfg.Billing, serviceCredentialEnv{
			Name:              "billing",
			EndpointVars:      []string{"TELOS_BILLING_ENDPOINT"},
			EndpointValue:     "https://billing.usetelos.ai",
			TokenFileVar:      "TELOS_BILLING_ENV_TOKEN_FILE",
			TokenVar:          "TELOS_BILLING_ENV_TOKEN",
			OptionalTokenFile: !managedGatewayModeEnabled(),
		})
		if err != nil {
			return Config{}, err
		}
		if managedGatewayModeEnabled() && cfg.Billing.Endpoint != "" && cfg.Billing.EnvID != "" && cfg.Billing.Token == "" {
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
		if cfg.Kubernetes.StateStorageClass == "" {
			cfg.Kubernetes.StateStorageClass = strings.TrimSpace(os.Getenv("TELOS_STATE_STORAGE_CLASS"))
		}
		if cfg.Kubernetes.StateStorageSize == "" {
			cfg.Kubernetes.StateStorageSize = envOr("TELOS_STATE_STORAGE_SIZE", "10Gi")
		}
		if !cfg.Kubernetes.AllowHostPathState {
			cfg.Kubernetes.AllowHostPathState = boolEnv("TELOS_ALLOW_HOST_PATH_STATE", false)
		}
		if cfg.Kubernetes.WorkerEgressCIDRs == nil {
			cfg.Kubernetes.WorkerEgressCIDRs = splitList(os.Getenv("TELOS_WORKER_EGRESS_CIDRS"))
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

type serviceCredentialEnv struct {
	Name              string
	EndpointVars      []string
	EndpointValue     string
	TokenFileVar      string
	TokenVar          string
	OptionalTokenFile bool
}

func resolveServiceCredential(cfg ServiceCredentialConfig, env serviceCredentialEnv) (ServiceCredentialConfig, error) {
	if cfg.Endpoint == "" {
		cfg.Endpoint = envFirst(env.EndpointVars, env.EndpointValue)
	}
	if cfg.EnvID == "" {
		cfg.EnvID = os.Getenv("TELOS_ENV_ID")
	}
	if cfg.TokenFile == "" {
		cfg.TokenFile = os.Getenv(env.TokenFileVar)
	}
	if cfg.Token == "" {
		if token, err := authTokenFromFile(cfg.TokenFile); err != nil {
			if !(env.OptionalTokenFile && errors.Is(err, os.ErrNotExist)) {
				return ServiceCredentialConfig{}, fmt.Errorf("read %s.token_file: %w", env.Name, err)
			}
		} else if token != "" {
			cfg.Token = token
		}
	}
	if cfg.Token == "" {
		cfg.Token = os.Getenv(env.TokenVar)
	}
	return cfg, nil
}

func envFirst(names []string, fallback string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return fallback
}

func managedGatewayModeEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("TELOS_GATEWAY_MODE")), "managed")
}

func runtimeArtifactsFromEnv() []RuntimeArtifactConfig {
	var out []RuntimeArtifactConfig
	for _, item := range []struct {
		name string
		arch string
		env  string
	}{
		{name: "telos", arch: "amd64", env: "TELOS_RUNTIME_TELOS_LINUX_AMD64_SHA256"},
		{name: "telosd", arch: "amd64", env: "TELOS_RUNTIME_TELOSD_LINUX_AMD64_SHA256"},
		{name: "telos", arch: "arm64", env: "TELOS_RUNTIME_TELOS_LINUX_ARM64_SHA256"},
		{name: "telosd", arch: "arm64", env: "TELOS_RUNTIME_TELOSD_LINUX_ARM64_SHA256"},
	} {
		if value := strings.TrimSpace(os.Getenv(item.env)); value != "" {
			out = append(out, RuntimeArtifactConfig{Name: item.name, OS: "linux", Arch: item.arch, SHA256: value})
		}
	}
	return out
}

func boolEnv(name string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	switch value {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func splitList(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '\n' || r == ' ' || r == '\t'
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if trimmed := strings.TrimSpace(field); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
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

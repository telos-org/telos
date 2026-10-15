package telosd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeConfigDefaults(t *testing.T) {
	cfg, err := NormalizeConfig(Config{
		Auth: AuthConfig{Token: "operator-token"},
	})
	if err != nil {
		t.Fatalf("NormalizeConfig: %v", err)
	}
	if cfg.Root != "/telos-state" {
		t.Fatalf("root: got %q", cfg.Root)
	}
	if cfg.Server.Listen != "0.0.0.0:8000" {
		t.Fatalf("listen: got %q", cfg.Server.Listen)
	}
}

func TestNormalizeConfigAcceptsCompactShape(t *testing.T) {
	cfg, err := NormalizeConfig(Config{Token: "operator-token"})
	if err != nil {
		t.Fatalf("NormalizeConfig: %v", err)
	}
	if cfg.Auth.Token != "operator-token" {
		t.Fatalf("auth.token: got %q", cfg.Auth.Token)
	}
}

func TestNormalizeConfigReadsTokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("operator-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := NormalizeConfig(Config{TokenFile: path})
	if err != nil {
		t.Fatalf("NormalizeConfig: %v", err)
	}
	if cfg.Auth.Token != "operator-token" {
		t.Fatalf("auth.token: got %q", cfg.Auth.Token)
	}
}

func TestNormalizeConfigRequiresBearerToken(t *testing.T) {
	t.Setenv("TELOS_API_TOKEN", "")
	_, err := NormalizeConfig(Config{})
	if err == nil {
		t.Fatal("expected missing bearer token error")
	}
	if err.Error() != "auth.token is required for bearer auth" {
		t.Fatalf("error: got %q", err)
	}
}

// The fixture keeps the retired mode, transport, and auth.type keys: configs
// written before telosd became Cloud-only must still load.
func TestLoadConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telosd.yaml")
	if err := os.WriteFile(path, []byte(`kind: telosd.config.v1
mode: cloud
root: /state
server:
  transport: http
  listen: 127.0.0.1:9000
auth:
  type: bearer
  token: test-token
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Root != "/state" || cfg.Server.Listen != "127.0.0.1:9000" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if cfg.Auth.Token != "test-token" {
		t.Fatalf("auth token: got %q", cfg.Auth.Token)
	}
}

func TestLoadConfigIgnoresLegacyWorkerShape(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte("test-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "telosd.yaml")
	if err := os.WriteFile(path, []byte(`kind: telosd.config.v1
mode: cloud
token_file: `+tokenPath+`
worker:
  substrate: legacy
legacy_worker:
  image: registry/telos-agent@sha256:abc123
  pull_secret: registry-pull
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Auth.Token != "test-token" {
		t.Fatalf("auth token: got %q", cfg.Auth.Token)
	}
}

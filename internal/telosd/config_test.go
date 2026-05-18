package telosd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeLocalConfigDefaults(t *testing.T) {
	cfg, err := NormalizeConfig(Config{Mode: ModeLocal})
	if err != nil {
		t.Fatalf("NormalizeConfig: %v", err)
	}
	if cfg.Root != ".telos" {
		t.Fatalf("root: got %q", cfg.Root)
	}
	if cfg.Server.Transport != "unix" {
		t.Fatalf("transport: got %q", cfg.Server.Transport)
	}
	if cfg.Server.Socket != filepath.Join(".telos", "run", "telosd.sock") {
		t.Fatalf("socket: got %q", cfg.Server.Socket)
	}
	if cfg.Auth.Type != AuthLocal {
		t.Fatalf("auth.type: got %q", cfg.Auth.Type)
	}
}

func TestNormalizeCloudConfigDefaults(t *testing.T) {
	cfg, err := NormalizeConfig(Config{
		Mode: ModeCloud,
		Auth: AuthConfig{Token: "operator-token"},
	})
	if err != nil {
		t.Fatalf("NormalizeConfig: %v", err)
	}
	if cfg.Root != "/telos-state" {
		t.Fatalf("root: got %q", cfg.Root)
	}
	if cfg.Server.Transport != "http" {
		t.Fatalf("transport: got %q", cfg.Server.Transport)
	}
	if cfg.Server.Listen != "0.0.0.0:8000" {
		t.Fatalf("listen: got %q", cfg.Server.Listen)
	}
	if cfg.Auth.Type != AuthBearer {
		t.Fatalf("auth.type: got %q", cfg.Auth.Type)
	}
}

func TestNormalizeCloudConfigRequiresBearerToken(t *testing.T) {
	t.Setenv("TELOS_API_TOKEN", "")
	_, err := NormalizeConfig(Config{Mode: ModeCloud})
	if err == nil {
		t.Fatal("expected missing bearer token error")
	}
	if err.Error() != "auth.token is required for bearer auth" {
		t.Fatalf("error: got %q", err)
	}
}

func TestNormalizeConfigRejectsCrossModeAuth(t *testing.T) {
	_, err := NormalizeConfig(Config{
		Mode: ModeCloud,
		Auth: AuthConfig{Type: AuthLocal},
	})
	if err == nil {
		t.Fatal("expected cloud/local auth mismatch")
	}
	if err.Error() != `cloud mode requires auth.type "bearer"` {
		t.Fatalf("cloud mismatch error: got %q", err)
	}

	_, err = NormalizeConfig(Config{
		Mode: ModeLocal,
		Auth: AuthConfig{Type: AuthBearer, Token: "operator-token"},
	})
	if err == nil {
		t.Fatal("expected local/bearer auth mismatch")
	}
	if err.Error() != `local mode requires auth.type "local"` {
		t.Fatalf("local mismatch error: got %q", err)
	}
}

func TestNormalizeConfigRejectsCrossModeTransport(t *testing.T) {
	_, err := NormalizeConfig(Config{
		Mode: ModeLocal,
		Server: ServerConfig{
			Transport: "http",
			Listen:    "127.0.0.1:8000",
		},
	})
	if err == nil {
		t.Fatal("expected local/http mismatch")
	}
	if err.Error() != `local mode requires server.transport "unix"` {
		t.Fatalf("local mismatch error: got %q", err)
	}

	_, err = NormalizeConfig(Config{
		Mode: ModeCloud,
		Auth: AuthConfig{Token: "operator-token"},
		Server: ServerConfig{
			Transport: "unix",
			Socket:    "/tmp/telosd.sock",
		},
	})
	if err == nil {
		t.Fatal("expected cloud/unix mismatch")
	}
	if err.Error() != `cloud mode requires server.transport "http"` {
		t.Fatalf("cloud mismatch error: got %q", err)
	}
}

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

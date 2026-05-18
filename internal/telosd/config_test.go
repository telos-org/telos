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
	if cfg.Access != "local" {
		t.Fatalf("access: got %q", cfg.Access)
	}
}

func TestNormalizeCloudConfigDefaults(t *testing.T) {
	cfg, err := NormalizeConfig(Config{Mode: ModeCloud})
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
	if cfg.Access != "bearer" {
		t.Fatalf("access: got %q", cfg.Access)
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
access: bearer
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
}

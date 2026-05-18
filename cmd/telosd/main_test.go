package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/telos-org/telos-go/internal/telosd"
)

func TestConfigFromFlagsDefaultsToLocal(t *testing.T) {
	cfg, err := configFromFlags("", "")
	if err != nil {
		t.Fatalf("configFromFlags: %v", err)
	}
	if cfg.Mode != telosd.ModeLocal {
		t.Fatalf("mode: got %q", cfg.Mode)
	}
	if cfg.Server.Transport != "unix" {
		t.Fatalf("transport: got %q", cfg.Server.Transport)
	}
}

func TestConfigFromFlagsUsesModeFromConfig(t *testing.T) {
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

	cfg, err := configFromFlags(path, "")
	if err != nil {
		t.Fatalf("configFromFlags: %v", err)
	}
	if cfg.Mode != telosd.ModeCloud {
		t.Fatalf("mode: got %q", cfg.Mode)
	}
	if cfg.Root != "/state" {
		t.Fatalf("root: got %q", cfg.Root)
	}
}

func TestConfigFromFlagsRootOverride(t *testing.T) {
	cfg, err := configFromFlags("", "/tmp/telos-state")
	if err != nil {
		t.Fatalf("configFromFlags: %v", err)
	}
	if cfg.Root != "/tmp/telos-state" {
		t.Fatalf("root: got %q", cfg.Root)
	}
	if cfg.Server.Socket != filepath.Join("/tmp/telos-state", "run", "telosd.sock") {
		t.Fatalf("socket: got %q", cfg.Server.Socket)
	}
}

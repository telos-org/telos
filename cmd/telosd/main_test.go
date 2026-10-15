package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigFromFlagsRequiresConfig(t *testing.T) {
	_, err := configFromFlags("", "")
	if err == nil || !strings.Contains(err.Error(), "--config is required") {
		t.Fatalf("error = %v, want --config is required", err)
	}
}

func writeConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "telosd.yaml")
	if err := os.WriteFile(path, []byte(`kind: telosd.config.v1
mode: cloud
root: /state
token: test-token
`), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestConfigFromFlagsLoadsConfig(t *testing.T) {
	cfg, err := configFromFlags(writeConfig(t), "")
	if err != nil {
		t.Fatalf("configFromFlags: %v", err)
	}
	if cfg.Root != "/state" {
		t.Fatalf("root: got %q", cfg.Root)
	}
}

func TestConfigFromFlagsRootOverride(t *testing.T) {
	cfg, err := configFromFlags(writeConfig(t), "/tmp/telos-state")
	if err != nil {
		t.Fatalf("configFromFlags: %v", err)
	}
	if cfg.Root != "/tmp/telos-state" {
		t.Fatalf("root: got %q", cfg.Root)
	}
}

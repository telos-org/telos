package platform

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPiConnectionCapabilityProbe(t *testing.T) {
	for _, outcome := range []string{"supported", "unsupported", "unknown", "failed", "missing", "timeout"} {
		t.Run(outcome, func(t *testing.T) {
			home := t.TempDir()
			bin := filepath.Join(home, ".local", "bin")
			if err := os.MkdirAll(bin, 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", home)
			t.Setenv("PATH", "/usr/bin:/bin")
			t.Setenv("NVM_DIR", "")
			t.Setenv("PI_CODING_AGENT_DIR", "/must-not-load-user-config")
			script := "#!/bin/sh\n" +
				"test -f ./capabilities.js || exit 1\n" +
				"test -f \"$PI_CODING_AGENT_DIR/capabilities.js\" || exit 1\n"
			switch outcome {
			case "supported":
				script += "printf 'TELOS_PI_CONNECTION_SWITCHING\\n'\n"
			case "unsupported":
				script += "exit 78\n"
			case "unknown":
				script += "printf '1.0.4\\n'\n"
			case "failed":
				script += "printf 'TELOS_PI_CONNECTION_SWITCHING\\n'\nexit 1\n"
			case "timeout":
				script += "exec sleep 30\n"
			}
			if outcome != "missing" {
				if err := os.WriteFile(filepath.Join(bin, "pi"), []byte(script), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			timeout := 3 * time.Second
			if outcome == "timeout" {
				timeout = 100 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			if got := piSupportsInferenceConnections(ctx); got != (outcome == "supported") {
				t.Fatalf("connection support = %v", got)
			}
		})
	}
}

func TestPiConnectionCapabilityVersionAndRuntime(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to check the probe's version/runtime guard")
	}
	for _, tt := range []struct {
		version string
		bun     bool
		want    bool
	}{
		{"0.84.2", true, false},
		{"1.0.3", true, false},
		{"1.0.4", true, true},
		{"1.0.5", true, true},
		{"1.1.0", true, true},
		{"2.0.0", true, false},
		{"unknown", true, false},
		{"1.0.4", false, false},
	} {
		t.Run(fmt.Sprintf("%s/bun=%t", tt.version, tt.bun), func(t *testing.T) {
			script := strings.Replace(string(piCapabilitiesExtension), `import { VERSION } from "@earendil-works/pi-coding-agent";`, fmt.Sprintf("const VERSION = %q;", tt.version), 1)
			script = strings.Replace(script, "export default function ()", "function probe()", 1)
			if tt.bun {
				script = "globalThis.Bun = {};\n" + script
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			output, err := exec.CommandContext(ctx, node, "--input-type=module", "-e", script+"\nprobe();").CombinedOutput()
			got := err == nil && strings.TrimSpace(string(output)) == "TELOS_PI_CONNECTION_SWITCHING"
			if got != tt.want {
				t.Fatalf("support = %v, want %v: %s %v", got, tt.want, output, err)
			}
		})
	}
}

func TestPiConnectionCapabilityRealRuntime(t *testing.T) {
	for _, tt := range []struct {
		env  string
		want bool
	}{
		{"TELOS_TEST_PI_BINARY", true},
		{"TELOS_TEST_LEGACY_PI_BINARY", false},
	} {
		t.Run(tt.env, func(t *testing.T) {
			binary := os.Getenv(tt.env)
			if binary == "" {
				if os.Getenv("CI") != "" {
					t.Fatal("CI must provide " + tt.env + " for real Pi capability tests")
				}
				t.Skip("set " + tt.env + " to run real Pi")
			}
			home := t.TempDir()
			bin := filepath.Join(home, ".local", "bin")
			if err := os.MkdirAll(bin, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(binary, filepath.Join(bin, "pi")); err != nil {
				t.Fatal(err)
			}
			// A broken user config must not affect the installed capability.
			if err := os.WriteFile(filepath.Join(home, "models.json"), []byte("invalid JSON"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", home)
			t.Setenv("PI_CODING_AGENT_DIR", home)
			if got := PiSupportsInferenceConnections(); got != tt.want {
				t.Fatalf("connection support = %v, want %v", got, tt.want)
			}
		})
	}
}

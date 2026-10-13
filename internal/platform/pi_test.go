package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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
			case "supported", "unsupported":
				script += fmt.Sprintf("printf '{\"connection_switching\":%t,\"executable\":\"%%s\"}\\n' \"$0\"\n", outcome == "supported")
			case "unknown":
				script += "printf '1.0.4\\n'\n"
			case "failed":
				script += "exit 78\n"
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
			got, err := piSupportsInferenceConnections(ctx)
			wantError := outcome != "supported" && outcome != "unsupported"
			if got != (outcome == "supported") || (err != nil) != wantError {
				t.Fatalf("connection support = %v, error = %v", got, err)
			}
		})
	}
}

func TestPiConnectionCapabilityCacheInvalidation(t *testing.T) {
	for _, change := range []string{"replacement", "in_place", "symlink", "wrapped_executable", "earlier_path"} {
		t.Run(change, func(t *testing.T) {
			home := t.TempDir()
			bin := filepath.Join(home, ".local", "bin")
			if err := os.MkdirAll(bin, 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", home)
			t.Setenv("PATH", home+":/usr/bin:/bin")
			count := filepath.Join(home, "checks")
			write := func(path string, supported bool) {
				t.Helper()
				body := fmt.Sprintf("#!/bin/sh\necho checked >> %q\nprintf '{\"connection_switching\":%t,\"executable\":\"%%s\"}\\n' \"$0\"\n", count, supported)
				if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			command := filepath.Join(bin, "pi")
			target := command
			switch change {
			case "symlink", "wrapped_executable":
				target = filepath.Join(home, "actual-pi")
				if change == "symlink" {
					if err := os.Symlink(target, command); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(command, []byte(fmt.Sprintf("#!/bin/sh\nexec %q \"$@\"\n", target)), 0o755); err != nil {
					t.Fatal(err)
				}
			case "earlier_path":
				target = filepath.Join(home, "pi")
			}
			write(target, true)
			// Concurrent readers share the one successful check.
			var readers sync.WaitGroup
			for i := 0; i < 8; i++ {
				readers.Go(func() {
					if supported, err := PiSupportsInferenceConnections(); err != nil || !supported {
						t.Errorf("cached supported check: %v %v", supported, err)
					}
				})
			}
			readers.Wait()
			if data, _ := os.ReadFile(count); string(data) != "checked\n" {
				t.Fatalf("successful check was not cached: %q", data)
			}
			switch change {
			case "in_place":
				write(target, false)
			case "earlier_path":
				write(command, false)
			case "symlink":
				replacement := filepath.Join(home, "new-pi")
				write(replacement, false)
				if err := os.Remove(command); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(replacement, command); err != nil {
					t.Fatal(err)
				}
			default:
				write(target+".new", false)
				if err := os.Rename(target+".new", target); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < 2; i++ {
				if supported, err := PiSupportsInferenceConnections(); err != nil || supported {
					t.Fatalf("replacement retained old capability: %v %v", supported, err)
				}
			}
			if data, _ := os.ReadFile(count); string(data) != "checked\nchecked\n" {
				t.Fatalf("explicit unsupported check was not cached: %q", data)
			}
		})
	}
}

func TestPiConnectionCapabilityDoesNotCacheFailures(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", home+":/usr/bin:/bin")
	ready := filepath.Join(home, "ready")
	script := fmt.Sprintf("#!/bin/sh\ntest -f %q || exit 1\nprintf '{\"connection_switching\":true,\"executable\":\"%%s\"}\\n' \"$0\"\n", ready)
	if err := os.WriteFile(filepath.Join(home, "pi"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := PiSupportsInferenceConnections(); err == nil {
		t.Fatal("probe failure was classified as unsupported")
	}
	if err := os.WriteFile(ready, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if supported, err := PiSupportsInferenceConnections(); err != nil || !supported {
		t.Fatalf("failed check was cached: %v %v", supported, err)
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
			var result struct {
				Supported bool `json:"connection_switching"`
			}
			if err != nil || json.Unmarshal(output, &result) != nil || result.Supported != tt.want {
				t.Fatalf("support = %v, want %v: %s %v", result.Supported, tt.want, output, err)
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
			if got, err := PiSupportsInferenceConnections(); err != nil || got != tt.want {
				t.Fatalf("connection support = %v, want %v, error = %v", got, tt.want, err)
			}
		})
	}
}

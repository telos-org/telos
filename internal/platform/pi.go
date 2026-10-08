package platform

import (
	"context"
	_ "embed"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// PiShellSetup resolves the same user-installed Pi for probes and agent turns.
const PiShellSetup = `export PATH="$HOME/.local/bin:$HOME/.npm-global/bin:$PATH"; ` +
	`if ! command -v pi >/dev/null 2>&1; then ` +
	`for nvm_script in "${NVM_DIR:-}/nvm.sh" "$HOME/.nvm/nvm.sh" "/usr/local/nvm/nvm.sh"; do ` +
	`[ -s "$nvm_script" ] || continue; ` +
	`. "$nvm_script"; ` +
	`break; ` +
	`done; ` +
	`fi; `

//go:embed pi_capabilities.js
var piCapabilitiesExtension []byte

// PiSupportsInferenceConnections checks the installed runtime without loading
// user extensions or dispatching a model request. Probe each API operation so
// replacing Pi cannot leave a stale cached capability behind.
func PiSupportsInferenceConnections() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return piSupportsInferenceConnections(ctx)
}

func piSupportsInferenceConnections(ctx context.Context) bool {
	dir, err := os.MkdirTemp("", "telos-pi-capabilities-")
	if err != nil {
		return false
	}
	defer os.RemoveAll(dir)
	extension := filepath.Join(dir, "capabilities.js")
	if err := os.WriteFile(extension, piCapabilitiesExtension, 0o600); err != nil {
		return false
	}
	script := PiShellSetup + `exec pi --offline --no-extensions --no-skills --no-prompt-templates --no-themes --no-context-files --no-tools --no-session -e "$1" --mode text --print`
	cmd := exec.CommandContext(ctx, "sh", "-c", script, "pi", extension)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PI_CODING_AGENT_DIR="+dir, "PI_TELEMETRY=0")
	cmd.WaitDelay = 100 * time.Millisecond
	output, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(output)) == "TELOS_PI_CONNECTION_SWITCHING"
}

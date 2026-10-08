package platform

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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

type piFileIdentity struct {
	path string
	info os.FileInfo
}

func (f piFileIdentity) unchanged() bool {
	info, err := os.Stat(f.path)
	return err == nil && os.SameFile(f.info, info) && f.info.Size() == info.Size() &&
		f.info.Mode() == info.Mode() && f.info.ModTime() == info.ModTime()
}

// Keep one result, including an explicit unsupported result, for the installed
// command and the executable/script it launched. Failed checks are never cached.
var piCapabilityCache struct {
	sync.Mutex
	command, environment string
	files                []piFileIdentity
	supported            bool
}

// PiSupportsInferenceConnections checks the installed runtime without loading
// user extensions or dispatching a model request. Ordinary reads only resolve
// and stat files; replacing Pi invalidates the cached probe.
func PiSupportsInferenceConnections() (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return piSupportsInferenceConnections(ctx)
}

func piSupportsInferenceConnections(ctx context.Context) (bool, error) {
	command, err := resolvePi(ctx)
	if err != nil {
		return false, err
	}
	environment := strings.Join([]string{os.Getenv("HOME"), os.Getenv("PATH"), os.Getenv("NVM_DIR")}, "\x00")
	piCapabilityCache.Lock()
	defer piCapabilityCache.Unlock()
	if piCapabilityCache.command == command && piCapabilityCache.environment == environment {
		unchanged := true
		for _, file := range piCapabilityCache.files {
			unchanged = unchanged && file.unchanged()
		}
		if unchanged {
			return piCapabilityCache.supported, nil
		}
	}
	piCapabilityCache.command = ""
	info, err := os.Stat(command)
	if err != nil {
		return false, fmt.Errorf("stat Pi: %w", err)
	}
	commandFile := piFileIdentity{command, info}
	dir, err := os.MkdirTemp("", "telos-pi-capabilities-")
	if err != nil {
		return false, fmt.Errorf("prepare Pi check: %w", err)
	}
	defer os.RemoveAll(dir)
	extension := filepath.Join(dir, "capabilities.js")
	if err := os.WriteFile(extension, piCapabilitiesExtension, 0o600); err != nil {
		return false, fmt.Errorf("prepare Pi check: %w", err)
	}
	script := PiShellSetup + `exec "$1" --offline --no-extensions --no-skills --no-prompt-templates --no-themes --no-context-files --no-tools --no-session -e "$2" --mode text --print`
	cmd := exec.CommandContext(ctx, "sh", "-c", script, "pi", command, extension)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PI_CODING_AGENT_DIR="+dir, "PI_TELEMETRY=0")
	cmd.WaitDelay = 100 * time.Millisecond
	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("run Pi capability check: %w", err)
	}
	var result struct {
		Supported  *bool  `json:"connection_switching"`
		Executable string `json:"executable"`
		Script     string `json:"script"`
	}
	if json.Unmarshal(output, &result) != nil || result.Supported == nil || !filepath.IsAbs(result.Executable) {
		return false, fmt.Errorf("Pi capability check returned an invalid result")
	}
	files := []piFileIdentity{commandFile}
	for _, path := range []string{result.Executable, result.Script} {
		if path == "" || path == command {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			return false, fmt.Errorf("stat Pi runtime: %w", err)
		}
		files = append(files, piFileIdentity{path, info})
	}
	if !commandFile.unchanged() {
		return false, fmt.Errorf("Pi changed during the capability check; retry")
	}
	piCapabilityCache.command, piCapabilityCache.environment = command, environment
	piCapabilityCache.files, piCapabilityCache.supported = files, *result.Supported
	return *result.Supported, nil
}

// Match PiShellSetup's lookup without launching a shell on the normal PATH.
// NVM-only installations still need its shell initialization to select Node/Pi.
func resolvePi(ctx context.Context) (string, error) {
	paths := append([]string{filepath.Join(os.Getenv("HOME"), ".local", "bin"), filepath.Join(os.Getenv("HOME"), ".npm-global", "bin")}, filepath.SplitList(os.Getenv("PATH"))...)
	for _, dir := range paths {
		path := filepath.Join(dir, "pi")
		if info, err := os.Stat(path); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return filepath.Abs(path)
		}
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", PiShellSetup+`command -v pi`)
	cmd.WaitDelay = 100 * time.Millisecond
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("locate Pi: %w", err)
	}
	return filepath.Abs(strings.TrimSpace(string(output)))
}

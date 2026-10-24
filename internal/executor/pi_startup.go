package executor

import (
	_ "embed"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/telos-org/telos/internal/sessionapi"
)

//go:embed pi_startup.js
var piStartupExtension []byte

type piStartupConfig struct {
	Model      string                          `json:"model"`
	Definition json.RawMessage                 `json:"definition,omitempty"`
	Connection *sessionapi.InferenceConnection `json:"connection,omitempty"`
}

// writePiStartup prepares the extension that registers this turn's model
// definition and connection, and returns the environment that loads it.
func writePiStartup(dir string, config piStartupConfig) (map[string]string, error) {
	data, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	extension, configPath := filepath.Join(dir, "pi-startup.js"), filepath.Join(dir, "pi-startup.json")
	if err := os.WriteFile(extension, piStartupExtension, 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		return nil, err
	}
	return map[string]string{"TELOS_PI_STARTUP_EXTENSION": extension, "TELOS_PI_STARTUP_CONFIG": configPath}, nil
}

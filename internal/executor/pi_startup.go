package executor

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed pi_startup.js
var piStartupExtension []byte

type PiStartupConfig struct {
	RequestID               string          `json:"request_id"`
	AttemptID               string          `json:"attempt_id"`
	Model                   string          `json:"model"`
	Thinking                string          `json:"thinking"`
	AllowThinkingAdjustment bool            `json:"allow_thinking_adjustment,omitempty"`
	Definition              json.RawMessage `json:"definition,omitempty"`
	ReceiptPath             string          `json:"receipt_path"`
}

type PiStartupReceipt struct {
	RequestID string `json:"request_id"`
	AttemptID string `json:"attempt_id"`
	Model     string `json:"model"`
	Thinking  string `json:"thinking"`
	Error     string `json:"error,omitempty"`
}

func ReadPiStartupReceipt(path string) (PiStartupReceipt, error) {
	var receipt PiStartupReceipt
	data, err := os.ReadFile(path)
	if err == nil {
		err = json.Unmarshal(data, &receipt)
	}
	return receipt, err
}

func (pe *PiExecutor) prepareStartup() (map[string]string, error) {
	if pe.Startup == nil {
		return nil, nil
	}
	dir := filepath.Dir(pe.Startup.ReceiptPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	file := filepath.Join(dir, "pi-startup.js")
	if err := os.WriteFile(file, piStartupExtension, 0o600); err != nil {
		return nil, err
	}
	data, err := json.Marshal(pe.Startup)
	if err != nil {
		return nil, err
	}
	config := filepath.Join(dir, "pi-startup-"+pe.Startup.AttemptID+".json")
	if err := os.WriteFile(config, data, 0o600); err != nil {
		return nil, err
	}
	if pe.Startup.Model != pe.Model || pe.Startup.Thinking != pe.Thinking {
		return nil, fmt.Errorf("startup settings differ from launch arguments")
	}
	return map[string]string{"TELOS_PI_STARTUP_EXTENSION": file, "TELOS_PI_STARTUP_CONFIG": config}, nil
}

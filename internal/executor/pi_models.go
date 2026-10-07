package executor

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
)

//go:embed pi_models.js
var piModelsExtension []byte

func WritePiModelDefinition(path, provider string, definition json.RawMessage) error {
	data, err := json.Marshal(struct {
		Provider   string          `json:"provider"`
		Definition json.RawMessage `json:"definition"`
	}{provider, definition})
	if err != nil {
		return err
	}
	if err := os.WriteFile(path+".tmp", data, 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

func (pe *PiExecutor) SetModelDefinition(ctx context.Context, provider, model string, definition json.RawMessage) (PiSettings, error) {
	return pe.SetSettings(ctx, PiSettingsUpdate{Provider: provider, Model: model, ModelDefinition: definition})
}

func (pe *PiExecutor) registerModelDefinition(ctx context.Context, rpc *piRPC, provider string, definition json.RawMessage) error {
	if pe.ModelConfigPath == "" {
		return fmt.Errorf("%w: model definition updates are not enabled", ErrPiSettingsRejected)
	}
	data, err := rpc.call(ctx, "get_commands", nil)
	if err != nil {
		return err
	}
	var commands struct{ Commands []struct{ Name string } }
	if err := json.Unmarshal(data, &commands); err != nil {
		return err
	}
	found := false
	for _, command := range commands.Commands {
		if command.Name == "telos-internal-refresh-model" {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("%w: model definition extension is unavailable", ErrPiSettingsRejected)
	}
	if err := WritePiModelDefinition(pe.ModelConfigPath, provider, definition); err != nil {
		return err
	}
	_, err = rpc.call(ctx, "prompt", map[string]interface{}{"message": "/telos-internal-refresh-model"})
	return err
}

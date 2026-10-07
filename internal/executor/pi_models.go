package executor

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
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

func (pe *PiExecutor) registerModelDefinition(ctx context.Context, rpc *piRPC, provider, model string, definition json.RawMessage) error {
	if pe.ModelConfigPath == "" {
		return fmt.Errorf("%w: model definition updates are not enabled", ErrPiSettingsRejected)
	}
	const command = "telos-internal-refresh-model"
	if err := rpc.requireCommand(ctx, command); err != nil {
		return err
	}
	data, err := rpc.callExtension(ctx, command, map[string]interface{}{"provider": provider, "definition": definition})
	if err != nil {
		return err
	}
	var registered struct{ Provider, Model, Error string }
	if err := json.Unmarshal(data, &registered); err != nil {
		return fmt.Errorf("Pi model registration: %w", err)
	}
	if registered.Error != "" {
		return fmt.Errorf("%w: %s", ErrPiSettingsRejected, registered.Error)
	}
	if registered.Provider != provider || registered.Model != model {
		return errors.New("Pi returned a different model during registration")
	}
	return nil
}

func (rpc *piRPC) requireCommand(ctx context.Context, name string) error {
	data, err := rpc.call(ctx, "get_commands", nil)
	if err != nil {
		return err
	}
	var commands struct{ Commands []struct{ Name string } }
	if err := json.Unmarshal(data, &commands); err != nil {
		return err
	}
	for _, command := range commands.Commands {
		if command.Name == name {
			return nil
		}
	}
	return fmt.Errorf("%w: Pi extension command %s is unavailable", ErrPiSettingsRejected, name)
}

func (pe *PiExecutor) setSettingsPair(ctx context.Context, rpc *piRPC, update PiSettingsUpdate) error {
	if len(update.ModelDefinition) > 0 && pe.ModelConfigPath == "" {
		return fmt.Errorf("%w: model definition updates are not enabled", ErrPiSettingsRejected)
	}
	const command = "telos-internal-prepare-settings"
	if err := rpc.requireCommand(ctx, command); err != nil {
		return err
	}
	data, err := rpc.callExtension(ctx, command, map[string]interface{}{
		"provider": update.Provider, "model": update.Model, "thinking": update.Thinking,
		"definition": update.ModelDefinition,
	})
	if err != nil {
		return err
	}
	var prepared struct{ Provider, Model, Thinking, ModelID, Error string }
	if err := json.Unmarshal(data, &prepared); err != nil {
		return fmt.Errorf("Pi settings preparation: %w", err)
	}
	if prepared.Error != "" {
		return fmt.Errorf("%w: %s", ErrPiSettingsRejected, prepared.Error)
	}
	if prepared.Provider != update.Provider || prepared.Model != update.Model || prepared.Thinking != update.Thinking ||
		!strings.HasPrefix(prepared.ModelID, "telos-internal-settings-") {
		return errors.New("Pi returned a different configuration during settings preparation")
	}
	key := prepared.Provider + "/" + prepared.ModelID
	if _, exists := rpc.settingsModels[key]; exists {
		return errors.New("Pi reused an immutable settings route")
	}
	rpc.settingsModels[key] = PiSettings{prepared.Provider, prepared.Model, prepared.Thinking}
	_, err = rpc.call(ctx, "set_model", map[string]interface{}{"provider": prepared.Provider, "modelId": prepared.ModelID})
	return err
}

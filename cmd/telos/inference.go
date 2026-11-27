package main

import (
	"flag"
	"fmt"
	"regexp"
	"strings"

	"github.com/telos-org/telos/internal/cloud"
)

const inferenceSettingsURL = "https://usetelos.ai/workspace?tab=inference"

func resolveCloudInference(client *cloud.Client, model string) (*cloud.InferenceSelection, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, nil
	}
	if model == "telos/default" || model == "telos/max" {
		return &cloud.InferenceSelection{Source: "managed", Tier: strings.TrimPrefix(model, "telos/")}, nil
	}
	if !strings.Contains(model, "/") {
		return nil, fmt.Errorf("--model must be telos/default, telos/max, or <name>/<model-id> for a saved API key or subscription")
	}
	connections, err := client.ListInferenceConnections()
	if err != nil {
		return nil, fmt.Errorf("cannot resolve inference connections: %w", err)
	}
	connection, modelID, err := selectInferenceConnection(connections, model)
	if err != nil {
		return nil, err
	}
	if connection.Source == "subscription" && connection.Status != "connected" {
		return nil, fmt.Errorf("subscription %q is %s; reconnect it at %s", connection.Name, connection.Status, inferenceSettingsURL)
	}
	return &cloud.InferenceSelection{Source: connection.Source, ConnectionID: connection.ID, Model: modelID}, nil
}

func selectInferenceConnection(connections []cloud.InferenceConnection, model string) (cloud.InferenceConnection, string, error) {
	var matches []cloud.InferenceConnection
	for _, connection := range connections {
		if strings.HasPrefix(model, connection.Name+"/") {
			matches = append(matches, connection)
		}
	}
	if len(matches) == 0 {
		return cloud.InferenceConnection{}, "", fmt.Errorf("no saved API key or subscription matches --model %q; run `telos config` to list them", model)
	}
	if len(matches) > 1 {
		return cloud.InferenceConnection{}, "", fmt.Errorf("--model %q matches more than one saved API key or subscription; rename them at %s so each name is unique", model, inferenceSettingsURL)
	}
	connection := matches[0]
	modelID := strings.TrimPrefix(model, connection.Name+"/")
	if strings.TrimSpace(modelID) == "" {
		return cloud.InferenceConnection{}, "", fmt.Errorf("a model ID is required; choose one at %s", inferenceSettingsURL)
	}
	return connection, modelID, nil
}

func inferenceSourceLabel(source string) string {
	switch source {
	case "byok":
		return "API key"
	case "subscription":
		return "Subscription"
	case "managed":
		return "Managed"
	default:
		return source
	}
}

var thinkingSyntax = regexp.MustCompile(`^[a-z]{1,32}$`)

func validateApplyInferenceFlags(fs *flag.FlagSet, model, thinking string) error {
	if flagNameSet(fs, "model") && strings.TrimSpace(model) == "" {
		return fmt.Errorf("--model requires a non-empty model")
	}
	if flagNameSet(fs, "thinking") && !thinkingSyntax.MatchString(strings.TrimSpace(thinking)) {
		return fmt.Errorf("--thinking requires a model-supported level using 1 to 32 lowercase letters")
	}
	return nil
}

func previewCreationInference(
	control *cloud.Client,
	preview *applyPreview,
	selection *cloud.InferenceSelection,
	runtime sessionRuntimeConfig,
) error {
	var preference *cloud.InferencePreference
	if selection == nil || runtime.Thinking == "" {
		var err error
		preference, err = control.InferencePreference()
		if err != nil {
			return fmt.Errorf("read workspace inference defaults: %w", err)
		}
	}
	state := &preview.Proposed
	state.Inference = selection
	state.Model = runtime.Model
	state.ModelSource = previewSourceOverride
	if selection == nil {
		state.Inference = &preference.Selection
		state.ModelSource = previewSourceWorkspace
		var connections []cloud.InferenceConnection
		if preference.Selection.Source != "managed" {
			var err error
			connections, err = control.ListInferenceConnections()
			if err != nil {
				return err
			}
		}
		state.Model = inferenceSelectionName(preference.Selection, connections)
	}
	state.Thinking = runtime.Thinking
	state.ThinkingSource = previewSourceOverride
	if runtime.Thinking == "" {
		state.ThinkingSource = previewSourceCloud
		if preference.AgentThinking != nil && *preference.AgentThinking != "" {
			state.Thinking = *preference.AgentThinking
			state.ThinkingSource = previewSourceWorkspace
		}
	}
	return nil
}

func prepareApplyInference(control *cloud.Client, prepared *preparedCloudApply, options cloudApplyOptions) error {
	current := applyState{
		Model:          cloudSessionModel(*prepared.goal),
		Thinking:       prepared.goal.AgentThinking,
		ModelSource:    previewSourceGoal,
		ThinkingSource: previewSourceGoal,
	}
	if options.model != nil || options.thinking != nil {
		state, err := control.GetDeploymentInference(prepared.goal.ID)
		if err != nil {
			return goalNotFound(err, prepared.goal.ID, control.ContextName())
		}
		current.Inference = state.Selection
		current.Model = cloudSessionModel(cloud.SessionRecord{AgentModel: state.AgentModel, Inference: &state.Inference})
		current.Thinking = state.AgentThinking
	}
	proposed := &prepared.proposed
	proposed.Inference, proposed.Model, proposed.Thinking = current.Inference, current.Model, current.Thinking
	proposed.ModelSource, proposed.ThinkingSource = previewSourceGoal, previewSourceGoal
	if prepared.current != nil {
		prepared.current.Inference, prepared.current.Model, prepared.current.Thinking = current.Inference, current.Model, current.Thinking
		prepared.current.ModelSource, prepared.current.ThinkingSource = previewSourceGoal, previewSourceGoal
	}
	if options.model != nil {
		selection, err := resolveCloudInference(control, *options.model)
		if err != nil {
			return err
		}
		prepared.inference = selection
		proposed.Inference, proposed.Model, proposed.ModelSource = selection, strings.TrimSpace(*options.model), previewSourceOverride
	}
	if options.thinking != nil {
		proposed.Thinking, proposed.ThinkingSource = strings.TrimSpace(*options.thinking), previewSourceOverride
	}
	return nil
}

func applyInferenceUnchanged(current, proposed *applyState) bool {
	modelSame := proposed.ModelSource == previewSourceGoal ||
		current.Inference != nil && proposed.Inference != nil && *current.Inference == *proposed.Inference
	thinkingSame := current.Thinking == proposed.Thinking
	return modelSame && thinkingSame
}

func goalUnchanged(current, accepted *cloud.SessionRecord) bool {
	if current == nil {
		return false
	}
	if current.CurrentRevisionID != "" && accepted.CurrentRevisionID != "" {
		return current.CurrentRevisionID == accepted.CurrentRevisionID
	}
	inferenceSame := current.Inference == nil && accepted.Inference == nil ||
		current.Inference != nil && accepted.Inference != nil && *current.Inference == *accepted.Inference
	return current.PackageDigest != "" && current.PackageDigest == accepted.PackageDigest &&
		inferenceSame && current.AgentModel == accepted.AgentModel && current.AgentThinking == accepted.AgentThinking
}

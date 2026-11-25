package main

import (
	"flag"
	"fmt"
	"io"
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

type inferenceChange struct {
	SessionID string `json:"goal_id"`
	Context   string `json:"context,omitempty"`
	*cloud.DeploymentInference
}

func validateApplyInferenceFlags(fs *flag.FlagSet, sessionID, model, thinking string) error {
	settingsSet := flagNamesSet(fs, "model", "thinking")
	if fs.NArg() > 0 {
		if sessionID != "" && settingsSet {
			return fmt.Errorf("apply spec and model/thinking changes separately: first apply SPEC.md --goal %s, then apply --goal %s --model MODEL and/or --thinking LEVEL", sessionID, sessionID)
		}
		return nil
	}
	if sessionID == "" || !settingsSet {
		return fmt.Errorf("provide a SPEC.md, or --goal GOAL with --model and/or --thinking")
	}
	if err := validateApplySession(sessionID); err != nil {
		return err
	}
	if flagNameSet(fs, "force") {
		return fmt.Errorf("--force cannot be used for a settings-only apply")
	}
	if flagNameSet(fs, "model") && strings.TrimSpace(model) == "" {
		return fmt.Errorf("--model requires a non-empty model")
	}
	if flagNameSet(fs, "thinking") && !thinkingSyntax.MatchString(strings.TrimSpace(thinking)) {
		return fmt.Errorf("--thinking requires a model-supported level using 1 to 32 lowercase letters")
	}
	return nil
}

func applySessionInference(sessionID, model, thinking, contextOverride string) (*inferenceChange, error) {
	control, request, err := prepareSessionInference(sessionID, model, thinking, contextOverride)
	if err != nil {
		return nil, err
	}
	state, err := control.UpdateDeploymentInference(sessionID, request)
	if err != nil {
		return nil, err
	}
	return &inferenceChange{SessionID: sessionID, Context: control.ContextName(), DeploymentInference: state}, nil
}

func prepareSessionInference(sessionID, model, thinking, contextOverride string) (*cloud.Client, cloud.DeploymentInferenceRequest, error) {
	var request cloud.DeploymentInferenceRequest
	if err := validateApplySession(sessionID); err != nil {
		return nil, request, err
	}
	if err := validateCloudSessionContext(sessionID, contextOverride); err != nil {
		return nil, request, err
	}
	control, err := cloud.ControlClientForContext(contextOverride)
	if err != nil {
		return nil, request, err
	}
	selection, err := resolveCloudInference(control, model)
	if err != nil {
		return nil, request, err
	}
	request.Inference = selection
	if thinking != "" {
		request.AgentThinking = &thinking
	}
	return control, request, nil
}

func printInferenceChange(out io.Writer, change *inferenceChange) {
	printSummaryField(out, "Goal", change.SessionID)
	if change.Context != "" {
		printSummaryField(out, "Context", change.Context)
	}
	printCloudInferenceSummary(out, cloud.SessionRecord{AgentModel: change.AgentModel, AgentThinking: change.AgentThinking, Inference: &change.Inference})
	if change.Applied {
		printSummaryField(out, "Applies", "next turn")
	} else {
		printSummaryField(out, "Applies", "next turn after the Goal's runtime receives it")
	}
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
		preview.Notes = append(preview.Notes,
			"The server resolves inherited thinking for the selected model when applying.",
		)
	}
	return nil
}

func previewSessionInference(goalID, model, thinking, contextOverride string) (*applyPreview, error) {
	control, request, err := prepareSessionInference(goalID, model, thinking, contextOverride)
	if err != nil {
		return nil, err
	}
	current, err := control.GetDeploymentInference(goalID)
	if err != nil {
		return nil, goalNotFound(err, goalID, control.ContextName())
	}
	preview := newApplyPreview(control, goalID)
	preview.Current = &applyState{
		Inference: current.Selection,
		Model: cloudSessionModel(cloud.SessionRecord{
			AgentModel: current.AgentModel,
			Inference:  &current.Inference,
		}),
		Thinking:       current.AgentThinking,
		ModelSource:    previewSourceGoal,
		ThinkingSource: previewSourceGoal,
	}
	preview.Proposed = *preview.Current
	modelUnchanged := request.Inference == nil ||
		current.Selection != nil && *request.Inference == *current.Selection
	thinkingUnchanged := request.AgentThinking == nil || *request.AgentThinking == current.AgentThinking
	if modelUnchanged && thinkingUnchanged {
		preview.Operation = "unchanged"
	}
	if request.Inference != nil {
		preview.Proposed.Inference = request.Inference
		preview.Proposed.Model = model
		preview.Proposed.ModelSource = previewSourceOverride
		if !modelUnchanged && request.AgentThinking == nil {
			preview.Proposed.Thinking = ""
			preview.Proposed.ThinkingSource = previewSourceInherited
			preview.Notes = append(preview.Notes,
				"The server resolves inherited thinking for the selected model when applying.",
			)
		}
	}
	if request.AgentThinking != nil {
		preview.Proposed.Thinking = thinking
		preview.Proposed.ThinkingSource = previewSourceOverride
	}
	return preview, nil
}

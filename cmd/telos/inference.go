package main

import (
	"fmt"
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
		return nil, fmt.Errorf("--model must be telos/default, telos/max, or <connection-name>/<model-id>")
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
		return cloud.InferenceConnection{}, "", fmt.Errorf("inference connection was not found; run `telos config` to list connections")
	}
	if len(matches) > 1 {
		return cloud.InferenceConnection{}, "", fmt.Errorf("inference selection is ambiguous; rename the connections at %s so the selection identifies one connection", inferenceSettingsURL)
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

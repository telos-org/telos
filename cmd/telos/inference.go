package main

import (
	"fmt"
	"strings"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/config"
)

func configuredCloudModel(model string) (string, error) {
	if model = strings.TrimSpace(model); model != "" {
		return model, nil
	}
	cfg, err := config.LoadConfig()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(cfg.DefaultModel), nil
}

func resolveCloudInference(
	client *cloud.Client,
	model string,
) (*cloud.InferenceSelection, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, nil
	}
	if model == "telos/default" || model == "telos/max" {
		return &cloud.InferenceSelection{
			Source: "managed",
			Tier:   strings.TrimPrefix(model, "telos/"),
		}, nil
	}

	connectionName, modelName, ok := strings.Cut(model, "/")
	if !ok || connectionName == "" || modelName == "" {
		return nil, fmt.Errorf(
			"--model must be telos/default, telos/max, or <connection-name>/<model-name>",
		)
	}
	connections, err := client.ListSubscriptionConnections()
	if err != nil {
		return nil, fmt.Errorf("list subscription connections: %w", err)
	}
	var selected *cloud.SubscriptionConnection
	for i := range connections {
		if connections[i].Name != connectionName {
			continue
		}
		if selected != nil {
			return nil, fmt.Errorf("subscription connection name %q is ambiguous", connectionName)
		}
		selected = &connections[i]
	}
	if selected == nil {
		return nil, fmt.Errorf(
			"subscription connection %q was not found; run `telos config` to list connections",
			connectionName,
		)
	}
	if selected.Status != "connected" {
		return nil, fmt.Errorf(
			"subscription connection %q is %s",
			connectionName,
			selected.Status,
		)
	}
	return &cloud.InferenceSelection{
		Source:       "subscription",
		ConnectionID: selected.ID,
		Model:        modelName,
	}, nil
}

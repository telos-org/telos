package main

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/telos-org/telos/internal/cloud"
)

const inferenceSettingsURL = "https://usetelos.ai/workspace?tab=inference"

type inferenceConnection struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Source   string `json:"source"`
	Provider string `json:"provider"`
	Status   string `json:"status"`
	Account  string `json:"account,omitempty"`
}

func loadInferenceConnections(client *cloud.Client) ([]inferenceConnection, error) {
	var subscriptions []cloud.SubscriptionConnection
	var keys []cloud.APIKeyConnection
	var subscriptionErr, keyErr error
	var wg sync.WaitGroup
	wg.Go(func() { subscriptions, subscriptionErr = client.ListSubscriptionConnections() })
	wg.Go(func() { keys, keyErr = client.ListAPIKeyConnections() })
	wg.Wait()
	connections := []inferenceConnection{}
	if subscriptionErr != nil {
		subscriptionErr = fmt.Errorf("subscriptions: %w", subscriptionErr)
	} else {
		for _, connection := range subscriptions {
			account := ""
			if connection.AccountLabel != nil {
				account = *connection.AccountLabel
			}
			connections = append(connections, inferenceConnection{
				ID: connection.ID, Name: connection.Name, Source: "subscription", Provider: connection.Provider,
				Status: connection.Status, Account: account,
			})
		}
	}
	if keyErr != nil {
		keyErr = fmt.Errorf("API keys: %w", keyErr)
	} else {
		for _, connection := range keys {
			connections = append(connections, inferenceConnection{
				ID: connection.ID, Name: connection.Name, Source: "byok", Provider: connection.Provider, Status: "saved",
			})
		}
	}
	return connections, errors.Join(subscriptionErr, keyErr)
}

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
	connections, err := loadInferenceConnections(client)
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

func selectInferenceConnection(connections []inferenceConnection, model string) (inferenceConnection, string, error) {
	var matches []inferenceConnection
	for _, connection := range connections {
		if strings.HasPrefix(model, connection.Name+"/") {
			matches = append(matches, connection)
		}
	}
	if len(matches) == 0 {
		return inferenceConnection{}, "", fmt.Errorf("inference connection was not found; run `telos config` to list connections")
	}
	if len(matches) > 1 {
		return inferenceConnection{}, "", fmt.Errorf("inference selection is ambiguous; rename the connections at %s so the selection identifies one connection", inferenceSettingsURL)
	}
	connection := matches[0]
	modelID := strings.TrimPrefix(model, connection.Name+"/")
	if strings.TrimSpace(modelID) == "" {
		return inferenceConnection{}, "", fmt.Errorf("a model ID is required; choose one at %s", inferenceSettingsURL)
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

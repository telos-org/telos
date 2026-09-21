package main

import (
	"fmt"
	"slices"
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

type inferenceIssue struct {
	Source  string `json:"source"`
	Message string `json:"message"`
}

type inferenceInventory struct {
	Connections []inferenceConnection
	Issues      []inferenceIssue
}

func loadInferenceConnections(client *cloud.Client) inferenceInventory {
	var subscriptions []cloud.SubscriptionConnection
	var keys []cloud.APIKeyConnection
	var subscriptionErr, keyErr error
	var wg sync.WaitGroup
	wg.Go(func() { subscriptions, subscriptionErr = client.ListSubscriptionConnections() })
	wg.Go(func() { keys, keyErr = client.ListAPIKeyConnections() })
	wg.Wait()
	result := inferenceInventory{Connections: []inferenceConnection{}}
	if subscriptionErr != nil {
		result.Issues = append(result.Issues, inferenceIssue{Source: "subscription", Message: subscriptionErr.Error()})
	} else {
		for _, connection := range subscriptions {
			account := ""
			if connection.AccountLabel != nil {
				account = *connection.AccountLabel
			}
			result.Connections = append(result.Connections, inferenceConnection{
				ID: connection.ID, Name: connection.Name, Source: "subscription", Provider: connection.Provider,
				Status: connection.Status, Account: account,
			})
		}
	}
	if keyErr != nil {
		result.Issues = append(result.Issues, inferenceIssue{Source: "byok", Message: keyErr.Error()})
	} else {
		for _, connection := range keys {
			result.Connections = append(result.Connections, inferenceConnection{
				ID: connection.ID, Name: connection.Name, Source: "byok", Provider: connection.Provider, Status: "saved",
			})
		}
	}
	return result
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
	inventory := loadInferenceConnections(client)
	if len(inventory.Issues) > 0 {
		return nil, fmt.Errorf("cannot resolve inference connections: %s", formatInferenceIssues(inventory.Issues))
	}
	connection, modelID, err := selectInferenceConnection(inventory.Connections, model)
	if err != nil {
		return nil, err
	}
	if connection.Source == "subscription" && connection.Status != "connected" {
		return nil, fmt.Errorf("subscription %q is %s; reconnect it at %s", connection.Name, connection.Status, inferenceSettingsURL)
	}
	if err := validateInferenceModel(client, connection, modelID); err != nil {
		return nil, err
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

func validateInferenceModel(client *cloud.Client, connection inferenceConnection, modelID string) error {
	if connection.Source == "subscription" {
		models, err := client.SubscriptionCatalog()
		if err != nil {
			return fmt.Errorf("could not verify models for %q: %w", connection.Name, err)
		}
		for _, model := range models {
			if model.ID == modelID && subscriptionModelAvailable(model, connection) {
				return nil
			}
		}
	} else {
		catalog, err := client.APIKeyCatalog()
		if err != nil {
			return fmt.Errorf("could not verify models for %q: %w", connection.Name, err)
		}
		if !catalog.Enabled {
			return fmt.Errorf("API-key inference is unavailable in this workspace")
		}
		for _, entry := range catalog.Connections {
			if entry.ConnectionID != connection.ID {
				continue
			}
			if entry.Error != nil && *entry.Error != "" {
				return fmt.Errorf("could not verify models for %q: %s; check the connection at %s", connection.Name, *entry.Error, inferenceSettingsURL)
			}
			for _, model := range entry.Models {
				if model.ID == modelID && model.Provider == connection.Provider {
					return nil
				}
			}
		}
	}
	return fmt.Errorf("model %q is unavailable for %q; check available models at %s", modelID, connection.Name, inferenceSettingsURL)
}

func subscriptionModelAvailable(model cloud.InferenceModel, connection inferenceConnection) bool {
	return model.Provider == connection.Provider && (model.ConnectionIDs == nil || slices.Contains(model.ConnectionIDs, connection.ID))
}

func formatInferenceIssues(issues []inferenceIssue) string {
	parts := make([]string, 0, len(issues))
	for _, issue := range issues {
		label := inferenceSourceLabel(issue.Source)
		parts = append(parts, label+": "+issue.Message)
	}
	return strings.Join(parts, "; ")
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

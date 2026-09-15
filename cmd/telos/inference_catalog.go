package main

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"text/tabwriter"

	"github.com/telos-org/telos/internal/cloud"
)

type modelCatalogRow struct {
	ID             string   `json:"id"`
	Label          string   `json:"label"`
	Source         string   `json:"source"`
	Provider       string   `json:"provider,omitempty"`
	ConnectionID   string   `json:"connection_id,omitempty"`
	ConnectionRef  string   `json:"connection_ref,omitempty"`
	ConnectionName string   `json:"connection_name,omitempty"`
	Status         string   `json:"status"`
	FetchedAt      *string  `json:"fetched_at,omitempty"`
	Arguments      []string `json:"arguments"`
}

type modelCatalogReport struct {
	Context string            `json:"context"`
	Models  []modelCatalogRow `json:"models"`
	Errors  []inferenceIssue  `json:"errors,omitempty"`
}

func loadModelCatalog(client *cloud.Client, refresh bool) modelCatalogReport {
	inventory := loadInferenceConnections(client, "")
	report := modelCatalogReport{Context: client.ContextName(), Errors: inventory.Issues}
	for _, tier := range []string{"default", "max"} {
		id := "telos/" + tier
		report.Models = append(report.Models, modelCatalogRow{
			ID: id, Label: id, Source: "managed", Status: "available", Arguments: []string{"--model", id},
		})
	}
	var subscriptions []inferenceConnection
	var keys []inferenceConnection
	for _, connection := range inventory.Connections {
		if connection.Source == "subscription" {
			subscriptions = append(subscriptions, connection)
		} else {
			keys = append(keys, connection)
		}
	}
	var subscriptionModels []cloud.InferenceModel
	var subscriptionErr error
	var keyCatalogs []cloud.ConnectionCatalog
	var keyIssues []inferenceIssue
	var wg sync.WaitGroup
	if len(subscriptions) > 0 {
		wg.Go(func() { subscriptionModels, subscriptionErr = client.SubscriptionCatalog() })
	}
	if len(keys) > 0 {
		wg.Go(func() { keyCatalogs, keyIssues = loadAPIKeyCatalogs(client, keys, refresh) })
	}
	wg.Wait()
	if subscriptionErr != nil {
		report.Errors = append(report.Errors, inferenceIssue{Source: "subscription", Message: subscriptionErr.Error()})
		subscriptionModels = nil
	}
	report.Errors = append(report.Errors, keyIssues...)
	for _, connection := range subscriptions {
		status := "available"
		if connection.Status != "connected" {
			status = connection.Status
			report.Errors = append(report.Errors, inferenceIssue{Source: connection.Source, ConnectionID: connection.ID, Message: "connection is " + connection.Status + "; reconnect at " + inferenceSettingsURL})
		}
		for _, model := range subscriptionModels {
			if subscriptionModelAvailable(model, connection) {
				report.Models = append(report.Models, catalogModelRow(model, connection, inventory, status, nil))
			}
		}
	}
	for _, connection := range keys {
		index := slices.IndexFunc(keyCatalogs, func(entry cloud.ConnectionCatalog) bool { return entry.ConnectionID == connection.ID })
		if index == -1 {
			continue
		}
		entry := keyCatalogs[index]
		status := "available"
		if entry.Error != nil && *entry.Error != "" {
			status = "stale"
			report.Errors = append(report.Errors, inferenceIssue{Source: connection.Source, ConnectionID: connection.ID, Message: *entry.Error})
		}
		for _, model := range entry.Models {
			if model.Provider != connection.Provider {
				report.Errors = append(report.Errors, inferenceIssue{Source: connection.Source, ConnectionID: connection.ID, Message: "catalog provider does not match the connection"})
				continue
			}
			report.Models = append(report.Models, catalogModelRow(model, connection, inventory, status, entry.FetchedAt))
		}
	}
	return report
}

func loadAPIKeyCatalogs(client *cloud.Client, keys []inferenceConnection, refresh bool) ([]cloud.ConnectionCatalog, []inferenceIssue) {
	if !refresh {
		catalog, err := client.APIKeyCatalog()
		if err != nil {
			return nil, []inferenceIssue{{Source: "byok", Message: err.Error()}}
		}
		if !catalog.Enabled {
			return nil, []inferenceIssue{{Source: "byok", Message: "API-key inference is unavailable in this workspace"}}
		}
		var issues []inferenceIssue
		for _, key := range keys {
			if !slices.ContainsFunc(catalog.Connections, func(entry cloud.ConnectionCatalog) bool { return entry.ConnectionID == key.ID }) {
				issues = append(issues, inferenceIssue{Source: "byok", ConnectionID: key.ID, Message: "model catalog is unavailable; try --models --refresh"})
			}
		}
		return catalog.Connections, issues
	}
	entries := make([]cloud.ConnectionCatalog, len(keys))
	errors := make([]error, len(keys))
	limit := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for index, key := range keys {
		wg.Go(func() {
			limit <- struct{}{}
			defer func() { <-limit }()
			entry, err := client.RefreshAPIKeyCatalog(key.ID)
			errors[index] = err
			if err == nil {
				if entry.ConnectionID != key.ID {
					errors[index] = fmt.Errorf("refresh returned a different connection")
				} else {
					entries[index] = *entry
				}
			}
		})
	}
	wg.Wait()
	var issues []inferenceIssue
	for index, err := range errors {
		if err != nil {
			issues = append(issues, inferenceIssue{Source: "byok", ConnectionID: keys[index].ID, Message: err.Error()})
		}
	}
	return entries, issues
}

func catalogModelRow(model cloud.InferenceModel, connection inferenceConnection, inventory inferenceInventory, status string, fetchedAt *string) modelCatalogRow {
	selector := connection.Name + "/" + model.ID
	arguments := []string{"--connection-id", connection.Ref, "--model", model.ID}
	selected, _, err := selectInferenceConnection(inventory.Connections, selector, "")
	if len(inventory.Issues) == 0 && err == nil && selected.Source == connection.Source && selected.ID == connection.ID && selector != "telos/default" && selector != "telos/max" {
		arguments = []string{"--model", selector}
	}
	label := model.Label
	if label == "" {
		label = model.ID
	}
	return modelCatalogRow{
		ID: model.ID, Label: label, Source: connection.Source, Provider: connection.Provider,
		ConnectionID: connection.ID, ConnectionRef: connection.Ref, ConnectionName: connection.Name,
		Status: status, FetchedAt: fetchedAt, Arguments: arguments,
	}
}

func printModelCatalog(report modelCatalogReport, jsonOut bool) {
	if jsonOut {
		printJSON(report)
		return
	}
	fmt.Printf("Context: %s\n\n", report.Context)
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "MODEL\tCONNECTION\tTYPE\tSTATUS\tDEPLOYMENT OPTIONS")
	for _, model := range report.Models {
		arguments := make([]string, len(model.Arguments))
		for i, value := range model.Arguments {
			arguments[i] = value
			if i%2 == 1 {
				arguments[i] = shellQuote(value)
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", model.Label, model.ConnectionName, inferenceSourceLabel(model.Source), model.Status, strings.Join(arguments, " "))
	}
	_ = w.Flush()
	for _, issue := range report.Errors {
		fmt.Printf("Error: %s\n", formatInferenceIssues([]inferenceIssue{issue}))
	}
}

package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"text/tabwriter"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/config"
)

func cmdConfig(args []string) {
	fs := newCommandFlagSet("config", "telos config [flags]")
	contextValue := fs.String("context", "", "Save the Cloud context as @handle, organization ID, or personal")
	jsonOut := fs.Bool("json", false, "JSON output")
	parseFlags(fs, args)
	requireArgCount(fs, 0, "no positional arguments")
	if flagNameSet(fs, "context") {
		if strings.TrimSpace(*contextValue) == "" {
			fmt.Fprintln(os.Stderr, "error: --context requires @handle, organization ID, or personal")
			os.Exit(2)
		}
		stored, err := config.LoadStoredConfig()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		for _, name := range []string{config.APIEndpointEnv, config.AuthTokenEnv} {
			if os.Getenv(name) != "" {
				fmt.Fprintf(os.Stderr, "warning: %s is ignored when updating stored context\n", name)
			}
		}
		contextName, err := setContext(stored, *contextValue)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if *jsonOut {
			printJSON(map[string]string{"context": contextName})
		} else {
			fmt.Printf("context set to %s\n", contextName)
		}
		return
	}
	cfg, err := config.LoadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	path, err := config.ConfigPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	report := loadConfigReport(cfg, path)
	if *jsonOut {
		printJSON(report)
	} else {
		printConfigReport(report)
	}
}

func setContext(stored *config.Config, value string) (string, error) {
	client, err := configClient(stored)
	if err != nil {
		return "", err
	}
	account, err := client.AccountBootstrap()
	if err != nil {
		return "", err
	}
	organization, err := account.ResolveContext(value)
	if err != nil {
		return "", err
	}
	contextName := account.CanonicalContextName(organization)
	stored.Context = contextName
	if organization.ID == account.PersonalOrgID {
		stored.Context = ""
	}
	if err := config.SaveConfig(stored); err != nil {
		return "", err
	}
	if os.Getenv(config.ContextEnv) != "" {
		fmt.Fprintf(os.Stderr, "warning: %s still overrides the stored context\n", config.ContextEnv)
	}
	return contextName, nil
}

type configReport struct {
	ConfigFile       string                    `json:"config_file"`
	Endpoint         string                    `json:"endpoint"`
	Authentication   string                    `json:"authentication"`
	Context          string                    `json:"context"`
	WorkspaceDefault *cloud.InferenceSelection `json:"workspace_default,omitempty"`
	Connections      []inferenceConnection     `json:"connections"`
	ModelOverride    string                    `json:"model_override,omitempty"`
	ThinkingOverride string                    `json:"thinking_override,omitempty"`
	Error            string                    `json:"error,omitempty"`
}

func loadConfigReport(cfg *config.Config, path string) configReport {
	report := configReport{
		ConfigFile: path, Endpoint: cfg.APIEndpoint, Context: strings.TrimSpace(cfg.Context),
		Authentication: "not configured", Connections: []inferenceConnection{},
		ModelOverride: strings.TrimSpace(os.Getenv("TELOS_MODEL")), ThinkingOverride: strings.TrimSpace(os.Getenv("TELOS_THINKING")),
	}
	if report.Endpoint == "" {
		report.Endpoint = cloud.DefaultAPIEndpoint
	}
	if report.Context == "" {
		report.Context = "personal"
	}
	if cfg.AuthToken == "" {
		return report
	}
	report.Authentication = "unavailable"
	client := cloud.NewClient(report.Endpoint, cfg.AuthToken)
	account, err := client.AccountBootstrap()
	if err != nil {
		if cloud.IsStatus(err, http.StatusUnauthorized) || cloud.IsStatus(err, http.StatusForbidden) {
			report.Authentication = "invalid"
		}
		report.Error = "authentication: " + err.Error()
		return report
	}
	report.Authentication = "valid"
	organization, err := account.ResolveContext(cfg.Context)
	if err != nil {
		report.Error = "context: " + err.Error()
		return report
	}
	report.Context = account.CanonicalContextName(organization)
	client.OrgID = organization.ID
	var connectionsErr, preferenceErr error
	var wg sync.WaitGroup
	wg.Go(func() { report.Connections, connectionsErr = loadInferenceConnections(client) })
	wg.Go(func() { report.WorkspaceDefault, preferenceErr = client.InferencePreference() })
	wg.Wait()
	if preferenceErr != nil {
		preferenceErr = fmt.Errorf("workspace default: %w", preferenceErr)
	}
	if err := errors.Join(connectionsErr, preferenceErr); err != nil {
		report.Error = err.Error()
	}
	return report
}

func printConfigReport(report configReport) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(w, "Config file\t%s\nEndpoint\t%s\nAuthentication\t%s\nContext\t%s\n", report.ConfigFile, report.Endpoint, report.Authentication, report.Context)
	if report.WorkspaceDefault != nil {
		fmt.Fprintf(w, "Workspace model\t%s\n", inferenceSelectionName(*report.WorkspaceDefault, report.Connections))
	}
	if report.ModelOverride != "" {
		fmt.Fprintf(w, "Model override\t%s (TELOS_MODEL)\n", report.ModelOverride)
	}
	if report.ThinkingOverride != "" {
		fmt.Fprintf(w, "Thinking override\t%s (TELOS_THINKING)\n", report.ThinkingOverride)
	}
	if report.Authentication == "valid" {
		fmt.Fprintln(w, "Connections")
		for _, connection := range report.Connections {
			fmt.Fprintf(w, "  %s\t%s\t%s\n", connection.Name, inferenceSourceLabel(connection.Source), connection.Status)
		}
	}
	if report.Error != "" {
		for _, line := range strings.Split(report.Error, "\n") {
			fmt.Fprintf(w, "Error\t%s\n", line)
		}
	}
	_ = w.Flush()
}

func inferenceSelectionName(selection cloud.InferenceSelection, connections []inferenceConnection) string {
	if selection.Source == "managed" {
		return "telos/" + selection.Tier
	}
	for _, connection := range connections {
		if connection.Source == selection.Source && connection.ID == selection.ConnectionID {
			return connection.Name + "/" + selection.Model
		}
	}
	return selection.Model + " (connection unavailable)"
}

func configClient(cfg *config.Config) (*cloud.Client, error) {
	if cfg.AuthToken == "" {
		return nil, fmt.Errorf("not logged in; run `telos login` first")
	}
	endpoint := cfg.APIEndpoint
	if endpoint == "" {
		endpoint = cloud.DefaultAPIEndpoint
	}
	return cloud.NewClient(endpoint, cfg.AuthToken), nil
}

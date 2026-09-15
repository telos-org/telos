package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"text/tabwriter"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/config"
)

type configOptions struct {
	Context, WorkspaceModel, ConnectionID          string
	ContextSet, WorkspaceModelSet, ConnectionIDSet bool
	Models, Refresh, JSON                          bool
}

func cmdConfig(args []string) {
	fs := newCommandFlagSet("config", "telos config [flags]")
	contextValue := fs.String("context", "", "Save the Cloud context as @handle, organization ID, or personal")
	models := fs.Bool("models", false, "List Cloud models and copyable deployment selectors")
	refresh := fs.Bool("refresh", false, "With --models, refresh API-key catalogs and reload subscription models")
	workspaceModel := fs.String("workspace-model", "", "Set the shared workspace default for future CLI and web deployments")
	connectionID := fs.String("connection-id", "", "With --workspace-model, select a connection ID and supply a raw model ID")
	jsonOut := fs.Bool("json", false, "JSON output")
	parseFlags(fs, args)
	requireArgCount(fs, 0, "no positional arguments")
	opts := configOptions{
		Context: *contextValue, WorkspaceModel: *workspaceModel, ConnectionID: *connectionID,
		ContextSet: flagNameSet(fs, "context"), WorkspaceModelSet: flagNameSet(fs, "workspace-model"), ConnectionIDSet: flagNameSet(fs, "connection-id"),
		Models: *models, Refresh: *refresh, JSON: *jsonOut,
	}
	if err := validateConfigOptions(opts); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	if err := runConfig(opts); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func validateConfigOptions(opts configOptions) error {
	if opts.ContextSet && (opts.Models || opts.WorkspaceModelSet || opts.Refresh || opts.ConnectionIDSet) {
		return fmt.Errorf("--context changes the saved CLI context and must be used separately; use TELOS_CONTEXT to scope another config operation")
	}
	if opts.ContextSet && strings.TrimSpace(opts.Context) == "" {
		return fmt.Errorf("--context requires @handle, organization ID, or personal")
	}
	if opts.Models && opts.WorkspaceModelSet {
		return fmt.Errorf("--models and --workspace-model cannot be combined")
	}
	if opts.Refresh && !opts.Models {
		return fmt.Errorf("--refresh requires --models")
	}
	if opts.WorkspaceModelSet && strings.TrimSpace(opts.WorkspaceModel) == "" {
		return fmt.Errorf("--workspace-model requires a model; use telos/default to choose the managed default")
	}
	if opts.ConnectionIDSet && (!opts.WorkspaceModelSet || strings.TrimSpace(opts.ConnectionID) == "") {
		return fmt.Errorf("--connection-id requires a non-empty ID and --workspace-model with a raw model ID")
	}
	return nil
}

func runConfig(opts configOptions) error {
	if opts.ContextSet {
		stored, err := config.LoadStoredConfig()
		if err != nil {
			return err
		}
		for _, name := range []string{config.APIEndpointEnv, config.AuthTokenEnv} {
			if os.Getenv(name) != "" {
				fmt.Fprintf(os.Stderr, "warning: %s is ignored when updating stored context\n", name)
			}
		}
		contextName, err := setContext(stored, opts.Context)
		if err != nil {
			return err
		}
		if opts.JSON {
			printJSON(map[string]string{"context": contextName, "scope": "local"})
		} else {
			fmt.Printf("context set to %s\n", contextName)
		}
		return nil
	}
	if opts.WorkspaceModelSet {
		client, err := cloud.ControlClient()
		if err != nil {
			return err
		}
		selection, err := resolveCloudInference(client, opts.WorkspaceModel, opts.ConnectionID)
		if err != nil {
			return err
		}
		preference, err := client.SetInferencePreference(*selection)
		if err != nil {
			return fmt.Errorf("could not change the default for %s: %w", client.ContextName(), err)
		}
		if opts.JSON {
			printJSON(struct {
				Context   string                   `json:"context"`
				Scope     string                   `json:"scope"`
				Selection cloud.InferenceSelection `json:"selection"`
			}{client.ContextName(), "workspace", preference.Selection})
		} else {
			fmt.Printf("Workspace default for %s set to %s.\nApplies to future CLI and web deployments. Existing deployments keep their settings.\n", client.ContextName(), strings.TrimSpace(opts.WorkspaceModel))
			if value := strings.TrimSpace(os.Getenv("TELOS_MODEL")); value != "" {
				fmt.Printf("TELOS_MODEL (%s) still overrides this default for CLI deployments.\n", value)
			}
		}
		return nil
	}
	if opts.Models {
		client, err := cloud.ControlClient()
		if err != nil {
			return err
		}
		catalog := loadModelCatalog(client, opts.Refresh)
		printModelCatalog(catalog, opts.JSON)
		if len(catalog.Errors) > 0 {
			return fmt.Errorf("some model information is unavailable; see the reported connection errors")
		}
		return nil
	}
	cfg, err := config.LoadConfig()
	if err != nil {
		return err
	}
	path, err := config.ConfigPath()
	if err != nil {
		return err
	}
	report := loadConfigReport(cfg, path)
	if opts.JSON {
		printJSON(report)
	} else {
		printConfigReport(report)
	}
	return nil
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
	Errors           []inferenceIssue          `json:"errors,omitempty"`
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
		report.Errors = append(report.Errors, inferenceIssue{Source: "Authentication", Message: err.Error()})
		return report
	}
	report.Authentication = "valid"
	organization, err := account.ResolveContext(cfg.Context)
	if err != nil {
		report.Errors = append(report.Errors, inferenceIssue{Source: "Context", Message: err.Error()})
		return report
	}
	report.Context = account.CanonicalContextName(organization)
	client.OrgID = organization.ID
	var inventory inferenceInventory
	var preference *cloud.InferencePreference
	var preferenceErr error
	var wg sync.WaitGroup
	wg.Go(func() { inventory = loadInferenceConnections(client, "") })
	wg.Go(func() { preference, preferenceErr = client.InferencePreference() })
	wg.Wait()
	report.Connections, report.Errors = inventory.Connections, inventory.Issues
	if preferenceErr != nil {
		report.Errors = append(report.Errors, inferenceIssue{Source: "Workspace default", Message: preferenceErr.Error()})
	} else {
		report.WorkspaceDefault = &preference.Selection
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
	for _, issue := range report.Errors {
		fmt.Fprintf(w, "Error\t%s\n", formatInferenceIssues([]inferenceIssue{issue}))
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
	return selection.ConnectionID + "/" + selection.Model
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

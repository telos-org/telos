package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/config"
)

func cmdConfig(args []string) {
	fs := newCommandFlagSet("config", "telos config [flags]")
	contextValue := fs.String(
		"context",
		"",
		"Cloud context as @handle, organization ID, or personal",
	)
	modelValue := fs.String(
		"model",
		"",
		"Default model for new Cloud deployments; empty clears it",
	)
	parseFlags(fs, args)
	requireArgCount(fs, 0, "no positional arguments")
	stored, err := config.LoadStoredConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if flagNameSet(fs, "context") {
		for _, name := range []string{config.APIEndpointEnv, config.AuthTokenEnv} {
			if os.Getenv(name) != "" {
				fmt.Fprintf(
					os.Stderr,
					"warning: %s is ignored when updating stored context\n",
					name,
				)
			}
		}
		setContext(stored, *contextValue)
		return
	}
	if flagNameSet(fs, "model") {
		setDefaultModel(stored, *modelValue)
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
	printConfig(cfg, path)
}

func setDefaultModel(stored *config.Config, value string) {
	stored.DefaultModel = strings.TrimSpace(value)
	if err := config.SaveConfig(stored); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if stored.DefaultModel == "" {
		fmt.Println("default model cleared")
		return
	}
	fmt.Printf("default model set to %s\n", stored.DefaultModel)
}

func setContext(stored *config.Config, value string) {
	if strings.TrimSpace(value) == "" {
		fmt.Fprintln(
			os.Stderr,
			"error: --context requires @handle, organization ID, or personal",
		)
		os.Exit(2)
	}
	client, err := configClient(stored)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	account, err := client.AccountBootstrap()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	organization, err := account.ResolveContext(value)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	contextName := account.CanonicalContextName(organization)
	if organization.ID == account.PersonalOrgID {
		stored.Context = ""
	} else {
		stored.Context = contextName
	}
	if err := config.SaveConfig(stored); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("context set to %s\n", contextName)
	if os.Getenv(config.ContextEnv) != "" {
		fmt.Fprintf(
			os.Stderr,
			"warning: %s still overrides the stored context\n",
			config.ContextEnv,
		)
	}
}

func printConfig(cfg *config.Config, path string) {
	endpoint := cfg.APIEndpoint
	if endpoint == "" {
		endpoint = cloud.DefaultAPIEndpoint
	}
	contextName := strings.TrimSpace(cfg.Context)
	if contextName == "" {
		contextName = "personal"
	}
	authentication := "not configured"
	var statusError error
	var connections []cloud.SubscriptionConnection
	if cfg.AuthToken != "" {
		authentication = "unavailable"
		client, err := configClient(cfg)
		if err != nil {
			statusError = err
		} else {
			account, err := client.AccountBootstrap()
			if err != nil {
				statusError = err
				if cloud.IsStatus(err, http.StatusUnauthorized) ||
					cloud.IsStatus(err, http.StatusForbidden) {
					authentication = "invalid"
				}
			} else {
				authentication = "valid"
				organization, err := account.ResolveContext(cfg.Context)
				if err != nil {
					statusError = err
				} else {
					contextName = account.CanonicalContextName(organization)
				}
				if statusError == nil {
					client.OrgID = organization.ID
					connections, err = client.ListSubscriptionConnections()
					if err != nil {
						statusError = err
					}
				}
			}
		}
	}
	defaultModel := strings.TrimSpace(cfg.DefaultModel)
	if defaultModel == "" {
		defaultModel = "workspace default"
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(w, "Config file\t%s\n", path)
	fmt.Fprintf(w, "Endpoint\t%s\n", endpoint)
	fmt.Fprintf(w, "Authentication\t%s\n", authentication)
	fmt.Fprintf(w, "Context\t%s\n", contextName)
	fmt.Fprintf(w, "Default model\t%s\n", defaultModel)
	if authentication == "valid" {
		fmt.Fprintln(w, "Subscriptions")
		for _, connection := range connections {
			account := ""
			if connection.AccountLabel != nil {
				account = *connection.AccountLabel
			}
			fmt.Fprintf(
				w,
				"  %s\t%s\t%s\t%s\n",
				connection.Name,
				connection.Provider,
				account,
				connection.Status,
			)
		}
	}
	if statusError != nil {
		fmt.Fprintf(w, "Error\t%v\n", statusError)
	}
	_ = w.Flush()
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

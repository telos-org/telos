package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/config"
)

func cmdConfig(args []string) {
	fs := flag.NewFlagSet("config", flag.ExitOnError)
	contextValue := fs.String(
		"context",
		"",
		"Cloud context as @handle, organization ID, or personal",
	)
	parseFlags(fs, args)
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: telos config [--context @handle]")
		os.Exit(2)
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
		setContext(config.LoadStoredConfig(), *contextValue)
		return
	}
	printConfig(config.LoadConfig())
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

	contextName := organization.ContextName()
	if organization.ID == account.PersonalOrgID {
		stored.Context = ""
		contextName = "personal"
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

func printConfig(cfg *config.Config) {
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
				} else if organization.ID == account.PersonalOrgID {
					contextName = "personal"
				} else {
					contextName = organization.ContextName()
				}
			}
		}
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(w, "Endpoint\t%s\n", endpoint)
	fmt.Fprintf(w, "Authentication\t%s\n", authentication)
	fmt.Fprintf(w, "Context\t%s\n", contextName)
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

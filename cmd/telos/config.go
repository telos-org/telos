package main

import (
	"flag"
	"fmt"
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

	effective := config.LoadConfig()
	if flagNameSet(fs, "context") {
		setContext(effective, *contextValue)
		return
	}
	printConfig(effective)
}

func setContext(effective *config.Config, value string) {
	if strings.TrimSpace(value) == "" {
		fmt.Fprintln(
			os.Stderr,
			"error: --context requires @handle, organization ID, or personal",
		)
		os.Exit(2)
	}
	client, err := configClient(effective)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	organization, err := client.ResolveContext(value)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	stored := config.LoadStoredConfig()
	if strings.TrimSpace(value) == "personal" {
		stored.Context = ""
	} else {
		stored.Context = organization.ID
	}
	if err := config.SaveConfig(stored); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("context set to %s\n", organization.ContextName())
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
	if cfg.AuthToken != "" {
		if client, err := configClient(cfg); err == nil {
			if organization, err := client.ResolveContext(cfg.Context); err == nil {
				contextName = organization.ContextName()
			}
		}
	}

	authenticated := "no"
	if cfg.AuthToken != "" {
		authenticated = "yes"
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(w, "Endpoint\t%s\n", endpoint)
	fmt.Fprintf(w, "Authenticated\t%s\n", authenticated)
	fmt.Fprintf(w, "Context\t%s\n", contextName)
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

package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/config"
)

// -- login --------------------------------------------------------------------

func cmdLogin(args []string) {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	endpoint := fs.String("endpoint", cloud.DefaultAPIEndpoint, "API endpoint")
	token := fs.String("token", "", "API token")
	noPrompt := fs.Bool("no-prompt", false, "No interactive prompt")
	parseFlags(fs, args)

	ep := cloud.NormalizeEndpoint(*endpoint)
	tok := *token
	if tok == "" {
		tok = os.Getenv("TELOS_AUTH_TOKEN")
	}
	if tok == "" && !*noPrompt {
		fmt.Print("Telos API token: ")
		fmt.Scanln(&tok)
	}
	if tok == "" {
		fmt.Fprintln(os.Stderr, "error: token required")
		os.Exit(1)
	}

	cfg := config.LoadConfig()
	cfg.APIEndpoint = ep
	cfg.AuthToken = tok
	client := cloud.NewClient(ep, tok)
	me, err := client.Me()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: login failed: %v\n", err)
		os.Exit(1)
	}
	cfg.OrgID = me.OrgID
	if err := config.SaveConfig(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if cfg.OrgID != "" {
		fmt.Printf("%s\n%s\n", ep, cfg.OrgID)
		return
	}
	fmt.Println(ep)
}

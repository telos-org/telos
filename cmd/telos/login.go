package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/config"
	"github.com/telos-org/telos/internal/gateway"
	"github.com/telos-org/telos/internal/oauthcred"
)

// -- login --------------------------------------------------------------------

func cmdLogin(args []string) {
	if len(args) > 0 && args[0] == "codex" {
		cmdLoginCodex(args[1:])
		return
	}
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
	client := cloud.NewControlClient(ep, tok)
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

func cmdLoginCodex(args []string) {
	fs := flag.NewFlagSet("login codex", flag.ExitOnError)
	noBrowser := fs.Bool("no-browser", false, "Print the authorization URL without opening a browser")
	parseFlags(fs, args)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	token, err := oauthcred.RunCodexLoopback(ctx, nil, oauthcred.StorePath(config.ConfigPath()), !*noBrowser)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: codex login failed: %v\n", err)
		os.Exit(1)
	}
	cfg := config.LoadConfig()
	headers := map[string]string{}
	if token.AccountID != "" {
		headers["chatgpt-account-id"] = token.AccountID
	}
	cfg.Gateway = config.GatewayConfig{
		Mode:      gateway.ModeBYO,
		Provider:  string(gateway.ProviderCodex),
		BaseURL:   "https://chatgpt.com",
		Transport: string(gateway.TransportOpenAISync),
		Kind:      string(gateway.KindOpenAI),
		Headers:   headers,
	}
	if err := config.SaveConfig(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("codex")
}

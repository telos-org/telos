package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/telos-org/telos/internal/config"
	"github.com/telos-org/telos/internal/gateway"
)

func cmdConfigure(args []string) {
	if len(args) == 0 || args[0] != "gateway" {
		fmt.Fprintln(os.Stderr, "usage: telos configure gateway --mode managed|byo [--base-url URL --api-key KEY] [--model MODEL] [--no-probe]")
		os.Exit(1)
	}
	fs := flag.NewFlagSet("configure gateway", flag.ExitOnError)
	mode := fs.String("mode", "", "Gateway mode: managed or byo")
	baseURL := fs.String("base-url", "", "BYO Responses API base URL")
	apiKey := fs.String("api-key", "", "BYO gateway API key")
	model := fs.String("model", "", "Model to use for the Responses probe")
	noProbe := fs.Bool("no-probe", false, "Skip BYO Responses API probe")
	parseFlags(fs, args[1:])

	cfg := config.LoadConfig()
	switch strings.ToLower(strings.TrimSpace(*mode)) {
	case gateway.ModeManaged:
		cfg.Gateway = config.GatewayConfig{Mode: gateway.ModeManaged}
	case gateway.ModeBYO:
		if strings.TrimSpace(*baseURL) == "" || strings.TrimSpace(*apiKey) == "" {
			fmt.Fprintln(os.Stderr, "error: BYO mode requires --base-url and --api-key")
			os.Exit(1)
		}
		if !*noProbe {
			if err := gateway.ProbeResponses(*baseURL, *apiKey, *model); err != nil {
				fmt.Fprintf(os.Stderr, "error: gateway probe failed: %v\n", err)
				os.Exit(1)
			}
		}
		cfg.Gateway = config.GatewayConfig{
			Mode:    gateway.ModeBYO,
			BaseURL: strings.TrimRight(strings.TrimSpace(*baseURL), "/"),
			APIKey:  strings.TrimSpace(*apiKey),
		}
	default:
		fmt.Fprintln(os.Stderr, "error: --mode must be managed or byo")
		os.Exit(1)
	}
	if err := config.SaveConfig(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(cfg.Gateway.Mode)
}

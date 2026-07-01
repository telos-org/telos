package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/telos-org/telos/internal/config"
	"github.com/telos-org/telos/internal/gateway"
)

func cmdConfigure(args []string) {
	if len(args) == 0 || args[0] != "gateway" {
		fmt.Fprintln(os.Stderr, "usage: telos configure gateway --mode managed|byo [--base-url URL --api-key KEY] [--transport openai_sync|bifrost_async] [--kind openai|bifrost] [--headers JSON] [--model MODEL] [--no-probe]")
		os.Exit(1)
	}
	fs := flag.NewFlagSet("configure gateway", flag.ExitOnError)
	mode := fs.String("mode", "", "Gateway mode: managed or byo")
	baseURL := fs.String("base-url", "", "BYO Responses API base URL")
	apiKey := fs.String("api-key", "", "BYO gateway API key")
	transport := fs.String("transport", "", "Gateway transport: openai_sync or bifrost_async")
	kind := fs.String("kind", "", "Gateway kind: openai or bifrost")
	headersRaw := fs.String("headers", "", "JSON object of extra gateway headers")
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
		headers, err := parseGatewayHeadersFlag(*headersRaw)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		resolvedTransport, resolvedKind, err := gateway.ValidateTransportAndKind(*transport, *kind)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if !*noProbe {
			if err := gateway.ProbeResponses(*baseURL, *apiKey, *model, gateway.ProbeConfig{
				Transport: resolvedTransport,
				Kind:      resolvedKind,
				Headers:   headers,
			}); err != nil {
				fmt.Fprintf(os.Stderr, "error: gateway probe failed: %v\n", err)
				os.Exit(1)
			}
		}
		cfg.Gateway = config.GatewayConfig{
			Mode:      gateway.ModeBYO,
			BaseURL:   strings.TrimRight(strings.TrimSpace(*baseURL), "/"),
			APIKey:    strings.TrimSpace(*apiKey),
			Transport: resolvedTransport,
			Kind:      resolvedKind,
			Headers:   headers,
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

func parseGatewayHeadersFlag(raw string) (map[string]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var headers map[string]string
	if err := json.Unmarshal([]byte(raw), &headers); err != nil {
		return nil, fmt.Errorf("--headers must be a JSON object of string values: %w", err)
	}
	return headers, nil
}

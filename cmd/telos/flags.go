package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/telos-org/telos/internal/cli"
	"github.com/telos-org/telos/internal/sessionapi"
)

type boolFlag interface {
	IsBoolFlag() bool
}

func parseFlags(fs *flag.FlagSet, args []string) {
	ordered := reorderInterspersedFlags(fs, args)
	if err := fs.Parse(ordered); err != nil {
		os.Exit(2)
	}
}

func reorderInterspersedFlags(fs *flag.FlagSet, args []string) []string {
	flags := make([]string, 0, len(args))
	positionals := make([]string, 0, len(args))
	afterDashDash := false

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if afterDashDash {
			positionals = append(positionals, arg)
			continue
		}
		if arg == "--" {
			afterDashDash = true
			continue
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positionals = append(positionals, arg)
			continue
		}

		flags = append(flags, arg)
		if strings.Contains(arg, "=") || flagIsBool(fs, arg) {
			continue
		}
		if i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}

	return append(flags, positionals...)
}

func flagIsBool(fs *flag.FlagSet, arg string) bool {
	name := strings.TrimLeft(arg, "-")
	if idx := strings.Index(name, "="); idx >= 0 {
		name = name[:idx]
	}
	f := fs.Lookup(name)
	if f == nil {
		return false
	}
	bf, ok := f.Value.(boolFlag)
	return ok && bf.IsBoolFlag()
}

func flagNamesSet(fs *flag.FlagSet, names ...string) bool {
	for _, name := range names {
		if flagNameSet(fs, name) {
			return true
		}
	}
	return false
}

func flagNameSet(fs *flag.FlagSet, name string) bool {
	seen := map[string]bool{}
	fs.Visit(func(f *flag.Flag) {
		seen[f.Name] = true
	})
	return seen[name]
}

func resolveLocalRunConfigFromFlags(
	fs *flag.FlagSet,
	workspace string,
	model string,
	thinking string,
	maxCostUSD float64,
	agentTimeout int,
) (cli.LocalRunConfig, error) {
	cost, err := positiveFloatOption(fs, "max-cost-usd", maxCostUSD, "TELOS_MAX_COST_USD", 20.0)
	if err != nil {
		return cli.LocalRunConfig{}, err
	}
	timeout, err := nonNegativeIntOption(fs, "agent-timeout-sec", agentTimeout, "TELOS_AGENT_TIMEOUT_SEC", 0)
	if err != nil {
		return cli.LocalRunConfig{}, err
	}
	return cli.LocalRunConfig{
		Workspace:       stringOption(fs, "workspace", workspace, "TELOS_WORKSPACE"),
		Model:           modelOption(fs, model),
		Thinking:        stringOptionDefault(fs, "thinking", thinking, "TELOS_THINKING", "medium"),
		MaxCostUSD:      &cost,
		AgentTimeoutSec: timeout,
	}, nil
}

type sessionRuntimeConfig struct {
	Model           string
	Thinking        string
	MaxCostUSD      *float64
	AgentTimeoutSec *int
}

func resolveSessionRuntimeConfigFromFlags(
	fs *flag.FlagSet,
	model string,
	thinking string,
	maxCostUSD float64,
	agentTimeout int,
) (sessionRuntimeConfig, error) {
	cfg := sessionRuntimeConfig{
		Model:    modelOption(fs, model),
		Thinking: stringOption(fs, "thinking", thinking, "TELOS_THINKING"),
	}
	if flagNameSet(fs, "max-cost-usd") || strings.TrimSpace(os.Getenv("TELOS_MAX_COST_USD")) != "" {
		cost, err := positiveFloatOption(fs, "max-cost-usd", maxCostUSD, "TELOS_MAX_COST_USD", 20.0)
		if err != nil {
			return sessionRuntimeConfig{}, err
		}
		cfg.MaxCostUSD = &cost
	}
	if flagNameSet(fs, "agent-timeout-sec") || strings.TrimSpace(os.Getenv("TELOS_AGENT_TIMEOUT_SEC")) != "" {
		timeout, err := nonNegativeIntOption(fs, "agent-timeout-sec", agentTimeout, "TELOS_AGENT_TIMEOUT_SEC", 0)
		if err != nil {
			return sessionRuntimeConfig{}, err
		}
		cfg.AgentTimeoutSec = &timeout
	}
	return cfg, nil
}

func applySessionRuntimeConfig(req *sessionapi.SessionCreateRequest, cfg sessionRuntimeConfig) {
	if cfg.Model != "" {
		req.Model = cfg.Model
	}
	if cfg.Thinking != "" {
		req.Thinking = cfg.Thinking
	}
	if cfg.MaxCostUSD != nil {
		req.MaxCostUSD = cfg.MaxCostUSD
	}
	if cfg.AgentTimeoutSec != nil {
		req.AgentTimeoutSec = cfg.AgentTimeoutSec
	}
}

func untilFlagValue(fs *flag.FlagSet, value int) (int, error) {
	if !flagNameSet(fs, "until") {
		return 0, nil
	}
	if value <= 0 {
		return 0, fmt.Errorf("--until must be positive")
	}
	return value, nil
}

func stringOption(fs *flag.FlagSet, name, value, envName string) string {
	if flagNameSet(fs, name) {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(os.Getenv(envName))
}

func stringOptionDefault(fs *flag.FlagSet, name, value, envName, defaultValue string) string {
	if got := stringOption(fs, name, value, envName); got != "" {
		return got
	}
	return defaultValue
}

func modelOption(fs *flag.FlagSet, value string) string {
	if flagNameSet(fs, "model") {
		return strings.TrimSpace(value)
	}
	if model := strings.TrimSpace(os.Getenv("TELOS_MODEL")); model != "" {
		return model
	}
	return ""
}

func nonNegativeIntOption(fs *flag.FlagSet, name string, value int, envName string, defaultValue int) (int, error) {
	if flagNameSet(fs, name) {
		if value < 0 {
			return 0, fmt.Errorf("--%s / %s must be non-negative", name, envName)
		}
		return value, nil
	}
	raw := strings.TrimSpace(os.Getenv(envName))
	if raw == "" {
		return defaultValue, nil
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("--%s / %s must be an integer", name, envName)
	}
	if parsed < 0 {
		return 0, fmt.Errorf("--%s / %s must be non-negative", name, envName)
	}
	return parsed, nil
}

func positiveFloatOption(fs *flag.FlagSet, name string, value float64, envName string, defaultValue float64) (float64, error) {
	if flagNameSet(fs, name) {
		if value <= 0 {
			return 0, fmt.Errorf("--%s / %s must be positive", name, envName)
		}
		return value, nil
	}
	raw := strings.TrimSpace(os.Getenv(envName))
	if raw == "" {
		return defaultValue, nil
	}
	parsed, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("--%s / %s must be a number", name, envName)
	}
	if parsed <= 0 {
		return 0, fmt.Errorf("--%s / %s must be positive", name, envName)
	}
	return parsed, nil
}

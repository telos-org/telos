package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/telos-org/telos-go/internal/cli"
	"github.com/telos-org/telos-go/internal/cloud"
	"github.com/telos-org/telos-go/internal/config"
	"github.com/telos-org/telos-go/internal/spec"
)

// -- run ----------------------------------------------------------------------

func cmdRun(args []string) {
	cmdLaunch("run", "submitted", args)
}

func cmdApply(args []string) {
	cmdLaunch("apply", "applied", args)
}

func cmdLaunch(command, action string, args []string) {
	fs := flag.NewFlagSet(command, flag.ExitOnError)
	workspace := fs.String("workspace", "", "Workspace directory")
	env := fs.String("env", "", "Cloud environment ID")
	model := fs.String("model", "", "Model name")
	thinking := fs.String("thinking", "medium", "Thinking effort")
	maxRounds := fs.Int("max-rounds", 20, "Maximum PVG rounds")
	maxCostUSD := fs.Float64("max-cost-usd", 20.0, "Maximum cost in USD")
	agentTimeout := fs.Int("agent-timeout-sec", 1800, "Agent timeout in seconds")
	readyTimeout := fs.Int("ready-timeout", 900, "Environment readiness timeout in seconds")
	noWait := fs.Bool("no-wait", false, "Do not wait for a newly created environment")
	jsonOut := fs.Bool("json", false, "JSON output")
	parseFlags(fs, args)
	localConfigSet := flagNamesSet(
		fs,
		"workspace",
		"model",
		"thinking",
		"max-rounds",
		"max-cost-usd",
		"agent-timeout-sec",
	)

	if fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "usage: telos %s SPEC.md [options]\n", command)
		os.Exit(1)
	}
	specArg := fs.Arg(0)
	specPath, hasLocalSpec := existingSpecPath(specArg)

	if ctx, ok := controllerSessionContext(); ok {
		if command == "apply" {
			fmt.Fprintln(os.Stderr, "error: telos apply is not available inside a controller session; use telos run for bounded child tasks")
			os.Exit(1)
		}
		if *env != "" {
			fmt.Fprintln(os.Stderr, "error: --env is not supported inside a controller session")
			os.Exit(1)
		}
		if localConfigSet {
			fmt.Fprintln(os.Stderr, "error: local run config flags are not supported inside a controller session")
			os.Exit(1)
		}
		runChildCloud(specArg, ctx, *jsonOut, action)
		return
	}

	platform := ""
	if hasLocalSpec {
		compiled, err := spec.CompileEnvironment(specPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		platform = compiled.Environment.Platform
	}

	launchMode, err := decideLaunchMode(
		platform,
		*env,
		config.IsConfigured(),
		localConfigSet,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	switch launchMode {
	case launchCloudExisting:
		runCloud(specArg, *env, *jsonOut, false, 0, action)
		return
	case launchCloudNew:
		runCloud(
			specArg,
			"",
			*jsonOut,
			!*noWait,
			time.Duration(*readyTimeout)*time.Second,
			action,
		)
		return
	}
	if !hasLocalSpec {
		fmt.Fprintf(os.Stderr, "error: unknown local spec: %s\n", specArg)
		os.Exit(1)
	}

	cfg, err := resolveLocalRunConfigFromFlags(
		fs,
		*workspace,
		*model,
		*thinking,
		*maxRounds,
		*maxCostUSD,
		*agentTimeout,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	session, err := cli.SubmitLocalSession(specPath, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		printJSON(map[string]interface{}{
			"session_id":  session.SessionID,
			"session_dir": session.SessionDir,
			"workspace":   session.Workspace,
			"spec_name":   session.SpecName,
			"status":      "running",
		})
	} else {
		fmt.Printf("%s %s (%s)\n", action, session.SessionID, session.SpecName)
	}
}

type launchMode string

const (
	launchLocal         launchMode = "local"
	launchCloudExisting launchMode = "cloud-existing"
	launchCloudNew      launchMode = "cloud-new"
)

func decideLaunchMode(
	platform string,
	envID string,
	cloudConfigured bool,
	localConfigSet bool,
) (launchMode, error) {
	if platform == "local" {
		if envID != "" {
			return "", fmt.Errorf("--env cannot be used with platform: local specs")
		}
		return launchLocal, nil
	}
	if localConfigSet {
		return "", fmt.Errorf("local run config flags require a platform: local spec")
	}
	if envID != "" {
		return launchCloudExisting, nil
	}
	if !cloudConfigured {
		return "", fmt.Errorf("non-local spec requires cloud config; run `telos login` first")
	}
	return launchCloudNew, nil
}

func runChildCloud(
	specArg string,
	ctx controllerContext,
	jsonOut bool,
	action string,
) {
	req, err := sessionCreateRequestForSpec(specArg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	req.ParentSessionID = &ctx.sessionID
	session, err := cloud.NewClient(ctx.endpoint, ctx.token).CreateSession(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if jsonOut {
		printJSON(map[string]any{"session": session})
		return
	}
	fmt.Printf("%s %s (status: %s)\n", action, session.SessionID, session.Status)
}

func runCloud(
	specArg string,
	envID string,
	jsonOut bool,
	waitForEnvironment bool,
	readyTimeout time.Duration,
	action string,
) {
	req, err := sessionCreateRequestForSpec(specArg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	client, env, err := cloudSessionClientForRun(envID, waitForEnvironment, readyTimeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	session, err := client.CreateSession(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if jsonOut {
		printJSON(map[string]any{
			"environment": env,
			"session":     session,
		})
	} else {
		fmt.Printf("%s %s (status: %s)\n", action, session.SessionID, session.Status)
		if env != nil {
			fmt.Printf("environment %s %s\n", env.ID, env.Handle)
		}
	}
}

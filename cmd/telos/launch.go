package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/telos-org/telos/internal/cli"
	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/config"
	"github.com/telos-org/telos/internal/sessionapi"
	"github.com/telos-org/telos/internal/spec"
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
	scope := fs.String("scope", "", "Package scope")
	model := fs.String("model", "", "Model name")
	thinking := fs.String("thinking", "medium", "Thinking effort")
	until := fs.Int("until", 0, "Run exactly N evaluator review cycles")
	maxCostUSD := fs.Float64("max-cost-usd", 20.0, "Maximum cost in USD")
	agentTimeout := fs.Int("agent-timeout-sec", 0, "Agent timeout in seconds; 0 disables")
	readyTimeout := fs.Int("ready-timeout", 900, "Environment readiness timeout in seconds")
	noWait := fs.Bool("no-wait", false, "Do not wait for a newly created environment")
	jsonOut := fs.Bool("json", false, "JSON output")
	parseFlags(fs, args)
	localConfigSet := flagNamesSet(fs, "workspace")
	untilValue, err := untilFlagValue(fs, *until)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if command == "apply" && flagNameSet(fs, "until") {
		fmt.Fprintln(os.Stderr, "error: --until is only supported with telos run")
		os.Exit(1)
	}
	if command == "apply" && *env != "" {
		fmt.Fprintln(os.Stderr, "error: --env is no longer supported with telos apply; deployments allocate environments automatically")
		os.Exit(1)
	}
	if command != "apply" && flagNameSet(fs, "scope") {
		fmt.Fprintln(os.Stderr, "error: --scope is only supported with telos apply")
		os.Exit(1)
	}

	if fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "usage: telos %s SPEC.md [options]\n", command)
		os.Exit(1)
	}
	specArg := fs.Arg(0)
	specPath, hasLocalSpec := existingSpecPath(specArg)

	if ctx, ok := rootSessionContext(); ok {
		if command == "apply" {
			fmt.Fprintln(os.Stderr, "error: telos apply is not available inside a root session; use telos run for child sessions")
			os.Exit(1)
		}
		if *env != "" {
			fmt.Fprintln(os.Stderr, "error: --env is not supported inside a root session")
			os.Exit(1)
		}
		if localConfigSet {
			fmt.Fprintln(os.Stderr, "error: local run config flags are not supported inside a root session")
			os.Exit(1)
		}
		runtimeConfig, err := resolveSessionRuntimeConfigFromFlags(fs, *model, *thinking, *maxCostUSD, *agentTimeout)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		runCloudChildSession(specArg, ctx, untilValue, runtimeConfig, *jsonOut, action)
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
	localRootID, inLocalRoot := localRootSessionID()
	if inLocalRoot {
		if command == "apply" {
			fmt.Fprintln(os.Stderr, "error: telos apply is not available inside a root session; use telos run for child sessions")
			os.Exit(1)
		}
		if launchMode != launchLocal {
			fmt.Fprintln(os.Stderr, "error: local root sessions can only launch platform: local child sessions")
			os.Exit(1)
		}
	}
	if err := validateLaunchCommand(command, launchMode); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	switch launchMode {
	case launchCloudExisting:
		runCloud(command, specArg, *env, *scope, untilValue, fs, *model, *thinking, *maxCostUSD, *agentTimeout, *jsonOut, false, 0, action)
		return
	case launchCloudNew:
		runCloud(
			command,
			specArg,
			"",
			*scope,
			untilValue,
			fs,
			*model,
			*thinking,
			*maxCostUSD,
			*agentTimeout,
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
		*maxCostUSD,
		*agentTimeout,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	cfg.SessionKind = sessionKindForCommand(command)
	cfg.Until = untilValue
	if inLocalRoot {
		cfg.ParentSessionID = &localRootID
	}

	session, err := cli.SubmitLocalSession(specPath, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		printJSON(map[string]interface{}{
			"session_id":       session.SessionID,
			"session_dir":      session.SessionDir,
			"workspace":        session.WorkspaceScope,
			"active_workspace": session.ActiveWorkspace,
			"spec_name":        session.SpecName,
			"status":           "running",
		})
	} else {
		printLocalLaunch(os.Stdout, action, session)
	}
}

func printLocalLaunch(out io.Writer, action string, session *cli.LocalSession) {
	workspace := shellQuote(session.WorkspaceScope)
	fmt.Fprintf(out, "%s %s\n\n", action, session.SpecName)
	printSummaryField(out, "Name", session.SpecName)
	printSummaryField(out, "Platform", "local")
	printSummaryField(out, "Status", "active")
	printSummaryField(out, "Cost", "-")
	printSummaryField(out, "Session", session.SessionID)
	printSummaryField(out, "Workspace", session.WorkspaceScope)
	fmt.Fprintln(out)
	printSummaryField(out, "Describe", fmt.Sprintf("cd %s && telos describe %s", workspace, session.SessionID))
	printSummaryField(out, "Logs", fmt.Sprintf("cd %s && telos logs %s", workspace, session.SessionID))
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
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

func validateLaunchCommand(command string, mode launchMode) error {
	if command == "run" && (mode == launchCloudExisting || mode == launchCloudNew) {
		return fmt.Errorf("telos run for cloud specs must be used inside a root session; use telos apply to create or update a root session")
	}
	return nil
}

func runCloudChildSession(
	specArg string,
	ctx rootContext,
	until int,
	runtimeConfig sessionRuntimeConfig,
	jsonOut bool,
	action string,
) {
	req, err := sessionCreateRequestForSpec(specArg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	req.ParentSessionID = &ctx.sessionID
	if until > 0 {
		req.Until = &until
	}
	applySessionRuntimeConfig(&req, runtimeConfig)
	session, err := cloud.NewClient(ctx.endpoint, ctx.token).CreateSession(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if jsonOut {
		printJSON(map[string]any{"session": session})
		return
	}
	printSessionReceipt(os.Stdout, action, session, nil)
}

func runCloud(
	command string,
	specArg string,
	envID string,
	scope string,
	until int,
	fs *flag.FlagSet,
	model string,
	thinking string,
	maxCostUSD float64,
	agentTimeout int,
	jsonOut bool,
	waitForEnvironment bool,
	readyTimeout time.Duration,
	action string,
) {
	if command == "apply" {
		applyCloudControl(specArg, scope, jsonOut)
		return
	}
	req, err := sessionCreateRequestForSpec(specArg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if until > 0 {
		req.Until = &until
	}
	runtimeConfig, err := resolveSessionRuntimeConfigFromFlags(fs, model, thinking, maxCostUSD, agentTimeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	applySessionRuntimeConfig(&req, runtimeConfig)
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
			"environment": environmentOutput(env),
			"session":     session,
		})
	} else {
		printSessionReceipt(os.Stdout, action, session, environmentOutput(env))
	}
}

func applyCloudControl(
	specArg string,
	scope string,
	jsonOut bool,
) {
	pkg, err := packageSpec(specArg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	control, err := cloud.ControlClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	record, err := pushSpecPackage(control, pkg, scope)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	operation, deployment, err := applyDeploymentPackage(control, pkg.name, record.Ref)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if jsonOut {
		printJSON(map[string]any{
			"operation":  operation,
			"deployment": deployment,
		})
		return
	}
	printDeploymentReceipt(os.Stdout, operation, deployment)
}

func applyDeploymentPackage(control *cloud.Client, name string, packageRef string) (string, *cloud.DeploymentRecord, error) {
	deployments, err := control.ListDeployments()
	if err != nil {
		return "", nil, err
	}
	var matches []cloud.DeploymentRecord
	for _, deployment := range deployments {
		if deployment.Name == name {
			matches = append(matches, deployment)
		}
	}
	switch len(matches) {
	case 0:
		deployment, err := control.CreateDeployment(name, packageRef)
		return "created", deployment, err
	case 1:
		deployment, err := control.UpdateDeployment(matches[0].ID, packageRef)
		return "updated", deployment, err
	default:
		return "", nil, fmt.Errorf("multiple deployments named %q; update by deployment id is not supported by telos apply yet", name)
	}
}

func printSessionReceipt(out io.Writer, operation string, session *sessionapi.Session, env *environmentJSON) {
	if session == nil {
		return
	}
	name := sessionName(*session)
	fmt.Fprintf(out, "%s %s\n\n", operation, name)
	row := displayRow(*session)
	printSummaryField(out, "Name", row.Name)
	printSummaryField(out, "Platform", row.Platform)
	printSummaryField(out, "Status", row.Status)
	printSummaryField(out, "Cost", formatDetailCost(session.TotalCostUSD))
	printSummaryField(out, "Session", row.Session)
	if env != nil {
		printSummaryField(out, "Environment", env.ID)
		if env.Handle != "" {
			printSummaryField(out, "Handle", env.Handle)
		}
	}
}

func printCloudApplyReceipt(out io.Writer, response *cloud.EnvironmentSessionApplyResponse, env *environmentJSON) {
	fmt.Fprintf(out, "%s %s\n\n", response.Operation, response.Session.Name)
	printSummaryField(out, "Name", response.Session.Name)
	printSummaryField(out, "Platform", "cloud")
	printSummaryField(out, "Status", response.Session.DesiredState)
	printSummaryField(out, "Digest", response.Session.PackageDigest)
	if env != nil {
		printSummaryField(out, "Environment", env.ID)
		if env.Handle != "" {
			printSummaryField(out, "Handle", env.Handle)
		}
	}
}

func printDeploymentReceipt(out io.Writer, operation string, deployment *cloud.DeploymentRecord) {
	fmt.Fprintf(out, "%s %s\n\n", operation, deployment.Name)
	printSummaryField(out, "Name", deployment.Name)
	printSummaryField(out, "Platform", "cloud")
	printSummaryField(out, "Status", deployment.State)
	printSummaryField(out, "Package", deployment.PackageRef)
	printSummaryField(out, "Digest", deployment.PackageDigest)
	printSummaryField(out, "Deployment", deployment.ID)
	if deployment.ServiceURL != nil {
		printSummaryField(out, "Service URL", *deployment.ServiceURL)
	}
	if deployment.DashboardURL != nil {
		printSummaryField(out, "Dashboard URL", *deployment.DashboardURL)
	}
}

func sessionKindForCommand(command string) sessionapi.SessionKind {
	if command == "apply" {
		return sessionapi.KindController
	}
	return sessionapi.KindTask
}

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
	model := fs.String("model", "", "Model name")
	thinking := fs.String("thinking", "medium", "Thinking effort")
	until := fs.Int("until", 0, "Run exactly N evaluator review cycles")
	maxCostUSD := fs.Float64("max-cost-usd", 20.0, "Maximum cost in USD")
	maxRounds := fs.Int("max-rounds", 0, "Maximum PVG rounds; 0 uses the runtime default")
	maxDurationSec := fs.Int("max-duration-sec", 0, "Maximum PVG duration in seconds; 0 uses the runtime default")
	maxInputTokens := fs.Int("max-input-tokens", 0, "Maximum input tokens across the PVG run; 0 disables")
	maxOutputTokens := fs.Int("max-output-tokens", 0, "Maximum output tokens across the PVG run; 0 disables")
	maxToolLoops := fs.Int("max-tool-loops", 0, "Maximum model-tool loop iterations per agent turn; 0 uses the runtime default")
	agentTimeout := fs.Int("agent-timeout-sec", 0, "Agent timeout in seconds; 0 disables")
	autocompactContextWindow := fs.Int("autocompact-context-window", 0, "Autocompaction context window in tokens; 0 uses the default, set explicitly to disable")
	autocompactTriggerRatio := fs.Float64("autocompact-trigger-ratio", 0, "Autocompaction trigger ratio in (0,1]; 0 uses the default")
	autocompactKeepRecentTokens := fs.Int("autocompact-keep-recent-tokens", 0, "Recent history tokens kept verbatim during autocompaction; 0 uses the default")
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
		runtimeConfig, err := resolveSessionRuntimeConfigFromFlags(fs, *model, *thinking, *maxCostUSD, budgetFlags{
			MaxRounds:       *maxRounds,
			MaxDurationSec:  *maxDurationSec,
			MaxInputTokens:  *maxInputTokens,
			MaxOutputTokens: *maxOutputTokens,
			MaxToolLoops:    *maxToolLoops,
			AgentTimeoutSec: *agentTimeout,
		})
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
		if err := ensureNoActiveControllerChild(localRootID, nil); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	}
	if err := validateLaunchCommand(command, launchMode); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	switch launchMode {
	case launchCloudExisting:
		runCloud(command, specArg, *env, untilValue, fs, *model, *thinking, *maxCostUSD, *maxRounds, *maxDurationSec, *maxInputTokens, *maxOutputTokens, *maxToolLoops, *agentTimeout, *jsonOut, false, 0, action)
		return
	case launchCloudNew:
		runCloud(
			command,
			specArg,
			"",
			untilValue,
			fs,
			*model,
			*thinking,
			*maxCostUSD,
			*maxRounds,
			*maxDurationSec,
			*maxInputTokens,
			*maxOutputTokens,
			*maxToolLoops,
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
		budgetFlags{
			MaxRounds:       *maxRounds,
			MaxDurationSec:  *maxDurationSec,
			MaxInputTokens:  *maxInputTokens,
			MaxOutputTokens: *maxOutputTokens,
			MaxToolLoops:    *maxToolLoops,
			AgentTimeoutSec: *agentTimeout,
		},
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

	if err := exportAutocompactEnv(fs, autocompactFlags{
		ContextWindow:    *autocompactContextWindow,
		TriggerRatio:     *autocompactTriggerRatio,
		KeepRecentTokens: *autocompactKeepRecentTokens,
	}); err != nil {
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
	client := cloud.NewClient(ctx.endpoint, ctx.token)
	sessions, err := client.ListSessions(0, true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: inspect controller children: %v\n", err)
		os.Exit(1)
	}
	if err := ensureNoActiveControllerChild(ctx.sessionID, sessions); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	session, err := client.CreateSession(req)
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

func ensureNoActiveControllerChild(parentID string, sessions []sessionapi.Session) error {
	if ok, _ := parallelChildrenAllowed(); ok {
		return nil
	}
	if sessions == nil {
		var err error
		sessions, err = store().List()
		if err != nil {
			return fmt.Errorf("inspect controller children: %w", err)
		}
	}
	active := activeChildSessions(sessions, parentID)
	if len(active) == 0 {
		return nil
	}
	return fmt.Errorf("controller %s already has active child session %s; wait for it to finish, stop it, or set TELOS_ALLOW_PARALLEL_CHILDREN=1 and TELOS_PARALLEL_CHILDREN_JUSTIFICATION with an explicit justification", parentID, active[0].SessionID)
}

func activeChildSessions(sessions []sessionapi.Session, parentID string) []sessionapi.Session {
	var active []sessionapi.Session
	for _, session := range sessions {
		if session.ParentSessionID == nil || *session.ParentSessionID != parentID {
			continue
		}
		if session.Status.IsTerminal() {
			continue
		}
		active = append(active, session)
	}
	return active
}

func parallelChildrenAllowed() (bool, string) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("TELOS_ALLOW_PARALLEL_CHILDREN"))) {
	case "1", "true", "yes", "on":
		justification := strings.TrimSpace(os.Getenv("TELOS_PARALLEL_CHILDREN_JUSTIFICATION"))
		return justification != "", justification
	default:
		return false, ""
	}
}

func runCloud(
	command string,
	specArg string,
	envID string,
	until int,
	fs *flag.FlagSet,
	model string,
	thinking string,
	maxCostUSD float64,
	maxRounds int,
	maxDurationSec int,
	maxInputTokens int,
	maxOutputTokens int,
	maxToolLoops int,
	agentTimeout int,
	jsonOut bool,
	waitForEnvironment bool,
	readyTimeout time.Duration,
	action string,
) {
	if command == "apply" {
		if err := rejectCloudApplyRuntimeFlags(fs); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		applyCloudControl(specArg, envID, waitForEnvironment, readyTimeout, jsonOut)
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
	runtimeConfig, err := resolveSessionRuntimeConfigFromFlags(fs, model, thinking, maxCostUSD, budgetFlags{
		MaxRounds:       maxRounds,
		MaxDurationSec:  maxDurationSec,
		MaxInputTokens:  maxInputTokens,
		MaxOutputTokens: maxOutputTokens,
		MaxToolLoops:    maxToolLoops,
		AgentTimeoutSec: agentTimeout,
	})
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

func rejectCloudApplyRuntimeFlags(fs *flag.FlagSet) error {
	var set []string
	for _, name := range []string{
		"model",
		"thinking",
		"max-cost-usd",
		"max-rounds",
		"max-duration-sec",
		"max-input-tokens",
		"max-output-tokens",
		"max-tool-loops",
		"agent-timeout-sec",
		"autocompact-context-window",
		"autocompact-trigger-ratio",
		"autocompact-keep-recent-tokens",
	} {
		if flagNameSet(fs, name) {
			set = append(set, "--"+name)
		}
	}
	if len(set) == 0 {
		return nil
	}
	return fmt.Errorf("runtime flags %s are not supported with cloud apply", strings.Join(set, ", "))
}

func applyCloudControl(
	specArg string,
	envID string,
	waitForEnvironment bool,
	readyTimeout time.Duration,
	jsonOut bool,
) {
	pkg, err := packageSpec(specArg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	control, env, err := cloudEnvironmentForApply(envID, false, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if _, err := pushSpecPackage(control, pkg); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	response, err := control.ApplyEnvironmentSession(env.ID, pkg.name, pkg.digest)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if waitForEnvironment {
		if readyTimeout <= 0 {
			readyTimeout = 15 * time.Minute
		}
		if err := cloud.WaitForEnvironment(env.Handle, readyTimeout); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	}
	if jsonOut {
		printJSON(map[string]any{
			"environment": environmentOutput(env),
			"operation":   response.Operation,
			"session":     response.Session,
		})
		return
	}
	printCloudApplyReceipt(os.Stdout, response, environmentOutput(env))
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

func updateRequestFromCreate(req sessionapi.SessionCreateRequest) sessionapi.SessionSpecUpdateRequest {
	update := sessionapi.SessionSpecUpdateRequest{}
	if req.SpecMarkdown != nil {
		update.SpecMarkdown = *req.SpecMarkdown
	}
	update.Model = req.Model
	update.Thinking = req.Thinking
	update.MaxCostUSD = req.MaxCostUSD
	update.MaxRounds = req.MaxRounds
	update.MaxDurationSec = req.MaxDurationSec
	update.MaxInputTokens = req.MaxInputTokens
	update.MaxOutputTokens = req.MaxOutputTokens
	update.MaxToolLoops = req.MaxToolLoops
	update.AgentTimeoutSec = req.AgentTimeoutSec
	return update
}

func sessionKindForCommand(command string) sessionapi.SessionKind {
	if command == "apply" {
		return sessionapi.KindController
	}
	return sessionapi.KindTask
}

package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
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
		runtimeConfig, err := resolveSessionRuntimeConfigFromFlags(fs, *model, *thinking, *maxCostUSD, *agentTimeout)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		runChildCloud(specArg, ctx, untilValue, runtimeConfig, *jsonOut, action)
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
	localParentSessionID, inLocalController := localControllerSessionID()
	if inLocalController {
		if command == "apply" {
			fmt.Fprintln(os.Stderr, "error: telos apply is not available inside a controller session; use telos run for bounded child tasks")
			os.Exit(1)
		}
		if launchMode != launchLocal {
			fmt.Fprintln(os.Stderr, "error: local controller sessions can only launch platform: local child tasks")
			os.Exit(1)
		}
	}
	switch launchMode {
	case launchCloudExisting:
		runCloud(command, specArg, *env, untilValue, fs, *model, *thinking, *maxCostUSD, *agentTimeout, *jsonOut, false, 0, action)
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
	if inLocalController {
		cfg.ParentSessionID = &localParentSessionID
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

func runChildCloud(
	specArg string,
	ctx controllerContext,
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
	kind := sessionapi.KindTask
	req.SessionKind = &kind
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
	req, err := sessionCreateRequestForSpec(specArg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	kind := sessionKindForCommand(command)
	req.SessionKind = &kind
	if until > 0 {
		req.Until = &until
	}
	runtimeConfig, err := resolveSessionRuntimeConfigFromFlags(fs, model, thinking, maxCostUSD, agentTimeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	applySessionRuntimeConfig(&req, runtimeConfig)
	if command == "apply" && envID == "" {
		applyCloudAuto(req, jsonOut, waitForEnvironment, readyTimeout)
		return
	}
	client, env, err := cloudSessionClientForRun(envID, waitForEnvironment, readyTimeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if command == "apply" {
		applyCloud(req, client, env, jsonOut)
		return
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

func applyCloud(
	req sessionapi.SessionCreateRequest,
	client *cloud.Client,
	env *cloud.Environment,
	jsonOut bool,
) {
	specName, err := specNameFromRequest(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	sessions, err := client.ListSessions(0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	existing, err := controllerForApply(sessions, specName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	var session *sessionapi.Session
	operation := "created"
	if existing != nil {
		operation = "updated"
		session, err = client.UpdateSessionSpec(existing.SessionID, updateRequestFromCreate(req))
	} else {
		session, err = client.CreateSession(req)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if jsonOut {
		printJSON(map[string]any{
			"environment": environmentOutput(env),
			"operation":   operation,
			"session":     session,
		})
		return
	}
	printSessionReceipt(os.Stdout, operation, session, environmentOutput(env))
}

type applyCloudResult struct {
	Environment *environmentJSON    `json:"environment,omitempty"`
	Operation   string              `json:"operation"`
	Session     *sessionapi.Session `json:"session"`
}

func applyCloudAuto(
	req sessionapi.SessionCreateRequest,
	jsonOut bool,
	waitForEnvironment bool,
	readyTimeout time.Duration,
) {
	specName, err := specNameFromRequest(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	targets, err := cloudSessionTargets("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	var results []applyCloudResult
	for _, target := range targets {
		sessions, err := target.client.ListSessions(0)
		if err != nil {
			continue
		}
		existing, err := controllerForApply(sessions, specName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %s: %v\n", target.env.ID, err)
			os.Exit(1)
		}
		if existing == nil {
			continue
		}
		session, err := target.client.UpdateSessionSpec(existing.SessionID, updateRequestFromCreate(req))
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %s: %v\n", target.env.ID, err)
			os.Exit(1)
		}
		env := environmentJSONFrom(&target.env)
		results = append(results, applyCloudResult{
			Environment: &env,
			Operation:   "updated",
			Session:     session,
		})
	}
	if len(results) == 0 {
		client, env, err := cloudSessionClientForRun("", waitForEnvironment, readyTimeout)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		session, err := client.CreateSession(req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		results = append(results, applyCloudResult{
			Environment: environmentOutput(env),
			Operation:   "created",
			Session:     session,
		})
	}
	if jsonOut {
		printJSON(map[string]any{"results": results})
		return
	}
	printApplyResults(os.Stdout, results)
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

func printApplyResults(out io.Writer, results []applyCloudResult) {
	if len(results) == 0 {
		return
	}
	if len(results) == 1 {
		result := results[0]
		printSessionReceipt(out, result.Operation, result.Session, result.Environment)
		return
	}

	seen := map[string]bool{}
	var operations []string
	for _, result := range results {
		if !seen[result.Operation] {
			seen[result.Operation] = true
			operations = append(operations, result.Operation)
		}
	}
	for index, operation := range operations {
		if index > 0 {
			fmt.Fprintln(out)
		}
		fmt.Fprintln(out, operation)
		fmt.Fprintln(out)
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ENV\tNAME\tPLATFORM\tSTATUS\tCOST\tSESSION")
		for _, result := range results {
			if result.Operation != operation || result.Session == nil {
				continue
			}
			row := displayRow(*result.Session)
			envID := "-"
			if result.Environment != nil && result.Environment.ID != "" {
				envID = result.Environment.ID
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
				envID,
				row.Name,
				row.Platform,
				row.Status,
				row.Cost,
				row.Session,
			)
		}
		_ = w.Flush()
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
	update.AgentTimeoutSec = req.AgentTimeoutSec
	return update
}

func specNameFromRequest(req sessionapi.SessionCreateRequest) (string, error) {
	if req.SpecMarkdown == nil {
		return "", fmt.Errorf("spec_markdown is required")
	}
	raw, _, ok := spec.ParseFrontmatter(*req.SpecMarkdown)
	if !ok {
		return "", fmt.Errorf("spec_markdown must contain YAML frontmatter")
	}
	name, ok := raw["name"].(string)
	if !ok || name == "" {
		return "", fmt.Errorf("spec frontmatter must include name")
	}
	return name, nil
}

func sessionKindForCommand(command string) sessionapi.SessionKind {
	if command == "apply" {
		return sessionapi.KindController
	}
	return sessionapi.KindTask
}

func activeControllerForSpec(sessions []sessionapi.Session, specName string) (*sessionapi.Session, error) {
	var matches []sessionapi.Session
	for _, session := range sessions {
		if !isControllerNamed(session, specName) {
			continue
		}
		if session.Status.IsTerminal() {
			continue
		}
		matches = append(matches, session)
	}
	switch len(matches) {
	case 0:
		return nil, nil
	case 1:
		return &matches[0], nil
	default:
		return nil, fmt.Errorf("multiple active controller sessions named %q; stop duplicates before applying", specName)
	}
}

func controllerForApply(sessions []sessionapi.Session, specName string) (*sessionapi.Session, error) {
	active, err := activeControllerForSpec(sessions, specName)
	if err != nil || active != nil {
		return active, err
	}
	var recoverable []sessionapi.Session
	for _, session := range sessions {
		if !isControllerNamed(session, specName) {
			continue
		}
		switch session.Status {
		case sessionapi.StatusFailed, sessionapi.StatusStale:
			recoverable = append(recoverable, session)
		}
	}
	switch len(recoverable) {
	case 0:
		return nil, nil
	case 1:
		return &recoverable[0], nil
	default:
		return nil, fmt.Errorf("multiple failed/stale controller sessions named %q; stop duplicates before applying", specName)
	}
}

func isControllerNamed(session sessionapi.Session, specName string) bool {
	return session.SessionKind != nil &&
		*session.SessionKind == sessionapi.KindController &&
		session.SpecName != nil &&
		*session.SpecName == specName
}

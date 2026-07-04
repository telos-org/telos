package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/telos-org/telos/internal/cli"
	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/config"
	"github.com/telos-org/telos/internal/runtimeclient"
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
	scope := fs.String("scope", "", "Package scope")
	sessionID := fs.String("session", "", "Managed session ID to update")
	model := fs.String("model", "", "Model name")
	thinking := fs.String("thinking", "medium", "Thinking effort")
	until := fs.Int("until", 0, "Run exactly N evaluator review cycles")
	maxCostUSD := fs.Float64("max-cost-usd", 20.0, "Maximum cost in USD")
	agentTimeout := fs.Int("agent-timeout-sec", 0, "Agent timeout in seconds; 0 disables")
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
	if command != "apply" && flagNameSet(fs, "scope") {
		fmt.Fprintln(os.Stderr, "error: --scope is only supported with telos apply")
		os.Exit(1)
	}
	if command != "apply" && flagNameSet(fs, "session") {
		fmt.Fprintln(os.Stderr, "error: --session is only supported with telos apply")
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
	case launchCloudApply:
		applyCloudControl(specArg, *scope, *sessionID, *jsonOut)
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
	printSummaryField(out, "Target", "local")
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
	launchLocal      launchMode = "local"
	launchCloudApply launchMode = "cloud-apply"
)

func decideLaunchMode(
	platform string,
	cloudConfigured bool,
	localConfigSet bool,
) (launchMode, error) {
	if platform == "local" {
		return launchLocal, nil
	}
	if localConfigSet {
		return "", fmt.Errorf("local run config flags require a platform: local spec")
	}
	if !cloudConfigured {
		return "", fmt.Errorf("non-local spec requires cloud config; run `telos login` first")
	}
	return launchCloudApply, nil
}

func validateLaunchCommand(command string, mode launchMode) error {
	if command == "run" && mode == launchCloudApply {
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
	session, err := runtimeclient.New(ctx.endpoint, ctx.token).CreateSession(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if jsonOut {
		printJSON(map[string]any{"session": session})
		return
	}
	printSessionReceipt(os.Stdout, action, session)
}

func applyCloudControl(
	specArg string,
	scope string,
	sessionID string,
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
	operation, session, err := applyCloudSessionPackage(
		control,
		pkg.name,
		record.Ref,
		sessionID,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if jsonOut {
		printJSON(map[string]any{
			"operation": operation,
			"session":   session,
		})
		return
	}
	printCloudSessionReceipt(os.Stdout, operation, session)
}

func applyCloudSessionPackage(
	control *cloud.Client,
	name string,
	packageRef string,
	sessionID string,
) (string, *cloud.SessionRecord, error) {
	if sessionID != "" {
		if !isCloudApplyID(sessionID) {
			return "", nil, fmt.Errorf("invalid cloud session id %q", sessionID)
		}
		session, err := control.UpdateSession(sessionID, packageRef)
		return "updated", session, err
	}

	session, err := control.CreateSession(name, packageRef)
	return "created", session, err
}

func printSessionReceipt(out io.Writer, operation string, session *sessionapi.Session) {
	if session == nil {
		return
	}
	name := sessionName(*session)
	fmt.Fprintf(out, "%s %s\n\n", operation, name)
	row := displayRow(*session)
	printSummaryField(out, "Name", row.Name)
	printSummaryField(out, "Target", row.Target)
	printSummaryField(out, "Status", row.Status)
	printSummaryField(out, "Cost", formatDetailCost(session.TotalCostUSD))
	printSummaryField(out, "Session", row.Session)
}

func printCloudSessionReceipt(out io.Writer, operation string, session *cloud.SessionRecord) {
	fmt.Fprintf(out, "%s %s\n\n", operation, session.Name)
	printSummaryField(out, "Name", session.Name)
	printSummaryField(out, "Target", "cloud")
	printSummaryField(out, "Status", session.State)
	printSummaryField(out, "Package", session.PackageRef)
	printSummaryField(out, "Digest", session.PackageDigest)
	printSummaryField(out, "Session", session.ID)
	if session.ServiceURL != nil {
		printSummaryField(out, "Service URL", *session.ServiceURL)
	}
	if session.DashboardURL != nil {
		printSummaryField(out, "Dashboard URL", *session.DashboardURL)
	}
}

func sessionKindForCommand(command string) sessionapi.SessionKind {
	if command == "apply" {
		return sessionapi.KindController
	}
	return sessionapi.KindTask
}

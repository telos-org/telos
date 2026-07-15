package main

import (
	"errors"
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
	"github.com/telos-org/telos/internal/sessionworker"
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
	sessionIDValue := ""
	sessionID := &sessionIDValue
	if command == "apply" {
		sessionID = fs.String("session", "", "Managed session ID to update")
	}
	model := fs.String("model", "", "pi model as <provider>/<model> (e.g. openai-codex/gpt-5.5); defaults to openai-codex/gpt-5.5 with high thinking (override with $TELOS_MODEL)")
	thinking := fs.String("thinking", "", "Thinking effort; defaults to $TELOS_THINKING, then high for local runs")
	untilValue := ""
	until := &untilValue
	if command == "run" {
		until = fs.String("until", "", "Run at most N review cycles or duration like 30m")
	}
	maxCostUSD := fs.Float64("max-cost-usd", 20.0, "Maximum cost in USD")
	jsonOut := fs.Bool("json", false, "JSON output")
	parseFlags(fs, args)
	localConfigSet := flagNamesSet(fs, "workspace")
	untilConfig, err := untilFlagValue(fs, *until)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "usage: telos %s SPEC.md [options]\n", command)
		os.Exit(1)
	}
	if command == "apply" && *sessionID != "" && *workspace != "" {
		fmt.Fprintln(os.Stderr, "error: --workspace can only seed a new session; it cannot be used with --session")
		os.Exit(1)
	}
	specArg := fs.Arg(0)
	specPath, hasLocalSpec := existingSpecPath(specArg)

	if ctx, ok := rootSessionContext(); ok {
		if command == "apply" {
			fmt.Fprintln(os.Stderr, "error: telos apply cannot be used from inside a Telos session; use telos run to launch nested specs")
			os.Exit(1)
		}
		if localConfigSet {
			fmt.Fprintln(os.Stderr, "error: local run config flags are not supported inside a Telos session")
			os.Exit(1)
		}
		runtimeConfig, err := resolveSessionRuntimeConfigFromFlags(fs, *model, *thinking, *maxCostUSD)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		runCloudChildSession(specArg, ctx, untilConfig, runtimeConfig, *jsonOut, action)
		return
	}

	platform := ""
	if hasLocalSpec {
		parsedPlatform, err := launchSpecPlatform(specPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		platform = parsedPlatform
	}
	if command == "apply" && *sessionID != "" && isLocalApplyID(*sessionID) {
		if !hasLocalSpec {
			fmt.Fprintf(os.Stderr, "error: unknown local spec: %s\n", specArg)
			os.Exit(1)
		}
		applyLocalSessionSpec(specPath, *sessionID, *jsonOut)
		return
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
			fmt.Fprintln(os.Stderr, "error: telos apply cannot be used from inside a Telos session; use telos run to launch nested specs")
			os.Exit(1)
		}
		if launchMode != launchLocal {
			fmt.Fprintln(os.Stderr, "error: a local Telos session can only launch specs with platform: local")
			os.Exit(1)
		}
	}
	if err := validateLaunchCommand(command, launchMode); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	switch launchMode {
	case launchCloudApply:
		runtimeConfig, err := resolveSessionRuntimeConfigFromFlags(fs, *model, *thinking, *maxCostUSD)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if runtimeConfig.MaxCostUSD != nil {
			fmt.Fprintln(os.Stderr, "error: --max-cost-usd is not supported for cloud apply yet")
			os.Exit(1)
		}
		if *sessionID != "" && cloudRuntimeConfigSet(runtimeConfig) {
			fmt.Fprintln(os.Stderr, "error: cloud runtime config flags can only seed a new session; they cannot update an existing session")
			os.Exit(1)
		}
		applyCloudControl(specArg, *sessionID, runtimeConfig, *jsonOut)
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
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	cfg.SessionKind = sessionKindForCommand(command)
	cfg.Until = untilConfig.ReviewCycles
	cfg.UntilSeconds = untilConfig.Seconds
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

func applyLocalSessionSpec(specPath string, sessionID string, jsonOut bool) {
	s := store()
	current, err := s.Get(sessionID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if current.SessionKind != nil && *current.SessionKind != sessionapi.KindController {
		fmt.Fprintf(os.Stderr, "error: %s is not a controller session\n", sessionID)
		os.Exit(1)
	}
	if current.Status == sessionapi.StatusStopped {
		fmt.Fprintf(os.Stderr, "error: %s is %s; create a new controller session instead\n", sessionID, current.Status)
		os.Exit(1)
	}
	data, err := os.ReadFile(specPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	response, err := s.UpdateSpecByID(sessionID, sessionapi.SessionSpecUpdateRequest{
		SpecMarkdown: string(data),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	session := response.Session
	if session != nil {
		sessionDir := ""
		if session.SessionDir != nil {
			sessionDir = *session.SessionDir
		}
		if err := sessionworker.Wake(sessionDir); err != nil {
			if !errors.Is(err, sessionworker.ErrWorkerNotRunning) {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			if err := sessionworker.Start(sessionDir, sessionapi.RuntimeLocal); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
		}
	}
	if jsonOut {
		printJSON(map[string]any{"operation": response.Operation, "session": session})
		return
	}
	printSessionReceipt(os.Stdout, response.Operation, session)
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

func launchSpecPlatform(specPath string) (string, error) {
	data, err := os.ReadFile(specPath)
	if err != nil {
		return "", err
	}
	raw, _, ok := spec.ParseFrontmatter(string(data))
	if !ok {
		return "", fmt.Errorf("%s has no valid YAML frontmatter", specPath)
	}
	platform, ok := raw["platform"]
	if !ok {
		return "", nil
	}
	value := fmt.Sprint(platform)
	if value != "local" && value != "cloud" {
		return "", fmt.Errorf("%s: invalid platform '%s' (valid: cloud, local)", specPath, value)
	}
	return value, nil
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
		return "", fmt.Errorf("this spec runs in Telos Cloud; run `telos login` first")
	}
	return launchCloudApply, nil
}

func validateLaunchCommand(command string, mode launchMode) error {
	if command == "run" && mode == launchCloudApply {
		return fmt.Errorf("use telos apply to start cloud specs; telos run can only launch cloud specs from inside an existing Telos session")
	}
	return nil
}

func runCloudChildSession(
	specArg string,
	ctx rootContext,
	until untilConfig,
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
	if until.ReviewCycles > 0 {
		req.Until = &until.ReviewCycles
	}
	if until.Seconds > 0 {
		req.UntilSeconds = &until.Seconds
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
	sessionID string,
	runtimeConfig sessionRuntimeConfig,
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
	packageRecord, err := pushSpecPackage(control, pkg, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	operation, session, err := applyCloudSessionPackage(
		control,
		pkg.name,
		packageRecord.Ref,
		sessionID,
		runtimeConfig,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if jsonOut {
		printJSON(map[string]any{
			"operation": operation,
			"package":   packageRecord,
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
	runtimeConfig sessionRuntimeConfig,
) (string, *cloud.SessionRecord, error) {
	if sessionID != "" {
		if !isCloudApplyID(sessionID) {
			return "", nil, fmt.Errorf("invalid cloud session id %q", sessionID)
		}
		session, err := control.UpdateSession(sessionID, packageRef)
		if err != nil && cloud.IsStatus(err, 409) {
			current, getErr := control.GetSession(sessionID)
			if getErr == nil && current.PackageRef == packageRef {
				return "unchanged", current, nil
			}
		}
		return "updated", session, err
	}

	session, err := control.CreateSession(cloud.SessionCreateOptions{
		Name:          name,
		PackageRef:    packageRef,
		AgentModel:    runtimeConfig.Model,
		AgentThinking: runtimeConfig.Thinking,
	})
	return "created", session, err
}

func cloudRuntimeConfigSet(cfg sessionRuntimeConfig) bool {
	return cfg.Model != "" || cfg.Thinking != ""
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

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/telos-org/telos/internal/cli"
	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/config"
	"github.com/telos-org/telos/internal/runtimeclient"
	"github.com/telos-org/telos/internal/sessionapi"
)

// -- run ----------------------------------------------------------------------

func cmdRun(args []string) {
	cmdLaunch("run", "submitted", args)
}

func cmdApply(args []string) {
	cmdLaunch("apply", "applied", args)
}

func cmdLaunch(command, action string, args []string) {
	synopsis := fmt.Sprintf("telos %s SPEC.md [flags]", command)
	if command == "apply" {
		synopsis += "\n       telos apply --session SESSION [--model MODEL] [--thinking LEVEL] [flags]"
	}
	fs := newCommandFlagSet(command, synopsis)
	workspaceValue := ""
	workspace := &workspaceValue
	if command == "run" {
		workspace = fs.String("workspace", "", "Workspace directory for local specs")
	}
	sessionIDValue := ""
	sessionID := &sessionIDValue
	forceValue := false
	force := &forceValue
	if command == "apply" {
		sessionID = fs.String("session", "", "Managed session ID to update")
		force = fs.Bool("force", false, "Deploy even if the current revision has not been snapshotted")
	}
	modelHelp := "Model as <provider>/<model> (e.g. openai-codex/gpt-5.5); defaults to $TELOS_MODEL"
	thinkingHelp := "Thinking effort: low, medium, high, or xhigh; defaults to $TELOS_THINKING, then high for local runs"
	if command == "apply" {
		modelHelp = "telos/default, telos/max, or <name>/<model-id> for a saved API key or subscription. Creation defaults to $TELOS_MODEL, then the workspace default; updates require an explicit flag without SPEC.md"
		thinkingHelp = "Creation: low, medium, high, or xhigh; defaults to $TELOS_THINKING. Updates: a model-supported level for the next turn, explicitly supplied without SPEC.md"
	}
	model := fs.String("model", "", modelHelp)
	thinking := fs.String("thinking", "", thinkingHelp)
	untilValue := ""
	until := &untilValue
	maxCostUSDValue := 0.0
	maxCostUSD := &maxCostUSDValue
	if command == "run" {
		until = fs.String("until", "", "Run at most N review cycles or duration like 30m")
		maxCostUSD = fs.Float64("max-cost-usd", 0, "Maximum local execution cost in USD; defaults to 20")
	}
	jsonOut := fs.Bool("json", false, "JSON output")
	contextValue := ""
	contextFlagValue := &contextValue
	if command == "apply" {
		contextFlagValue = cloudContextFlag(fs)
	}
	parseFlags(fs, args)
	*sessionID = strings.TrimSpace(*sessionID)
	contextOverride, err := cloudContextOverride(fs, *contextFlagValue)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	localConfigSet := flagNamesSet(fs, "workspace")
	untilConfig, err := untilFlagValue(fs, *until)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if command == "apply" {
		if insideTelosSession() {
			fmt.Fprintln(os.Stderr, "error: telos apply cannot be used from inside a Telos session; use telos run to launch nested specs")
			os.Exit(1)
		}
		if err := validateApplyInferenceFlags(fs, *sessionID, *model, *thinking); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(2)
		}
		if fs.NArg() == 0 {
			receipt, err := applySessionInference(*sessionID, strings.TrimSpace(*model), strings.TrimSpace(*thinking), contextOverride)
			if receipt != nil {
				if *jsonOut {
					printJSON(receipt)
				} else {
					printInferenceReceipt(os.Stdout, receipt)
				}
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			return
		}
	}
	requireArgCount(fs, 1, "one SPEC.md")
	if err := validateCloudSessionContext(*sessionID, contextOverride); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	specArg := fs.Arg(0)
	specPath, hasLocalSpec := existingSpecPath(specArg)

	if ctx, ok := rootSessionContext(); ok {
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

	if command == "apply" {
		if err := validateApplySession(*sessionID); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if err := validateForceApply(*force, *sessionID); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if err := requireCloudLogin(); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		var runtimeConfig sessionRuntimeConfig
		if *sessionID == "" {
			runtimeConfig, err = resolveSessionRuntimeConfigFromFlags(fs, *model, *thinking, *maxCostUSD)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
		}
		applyCloudControl(
			specArg,
			*sessionID,
			runtimeConfig,
			*force,
			*jsonOut,
			contextOverride,
		)
		return
	}
	if !hasLocalSpec {
		fmt.Fprintf(os.Stderr, "error: unknown local spec: %s\n", specArg)
		os.Exit(1)
	}
	if err := prepareRegistrySkills(specPath); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
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
	cfg.Until = untilConfig.ReviewCycles
	cfg.UntilSeconds = untilConfig.Seconds

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

// validateApplySession accepts only Telos Cloud sessions: telos apply always
// deploys to Telos Cloud, and local work is telos run.
func validateApplySession(sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	switch {
	case sessionID == "", isCloudApplyID(sessionID):
		return nil
	case isLocalApplyID(sessionID):
		return fmt.Errorf("%s is a local session; telos apply only updates Telos Cloud sessions", sessionID)
	default:
		return fmt.Errorf("invalid session id %q", sessionID)
	}
}

func requireCloudLogin() error {
	configured, err := config.IsConfigured()
	if err != nil {
		return err
	}
	if !configured {
		return errors.New("telos apply deploys to Telos Cloud; run `telos login` first")
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
	force bool,
	jsonOut bool,
	contextOverride string,
) {
	var reference *packageReference
	if strings.HasPrefix(strings.TrimSpace(specArg), "@") {
		parsed, err := parsePackageReference(specArg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		reference = &parsed
	}
	control, err := cloud.ControlClientForContext(contextOverride)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	var inference *cloud.InferenceSelection
	if sessionID == "" {
		inference, err = resolveCloudInference(control, runtimeConfig.Model)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	}
	packageName := ""
	var packageRecord *cloud.PackageVersionRecord
	if reference != nil {
		packageName = reference.name
		packageRecord, err = registryPackageForApply(control, *reference)
	} else {
		var pkg *specPackage
		pkg, err = packageSpec(specArg, contextOverride)
		if err == nil {
			packageName = pkg.name
			packageRecord, err = pushSpecPackage(control, pkg, "")
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	operation, session, err := applyCloudSessionPackage(
		control,
		packageName,
		packageRecord.Ref,
		sessionID,
		runtimeConfig,
		force,
		inference,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if jsonOut {
		printJSON(map[string]any{
			"context":   control.ContextName(),
			"operation": operation,
			"package":   packageRecord,
			"session":   session,
		})
		return
	}
	printCloudSessionReceiptForContext(
		os.Stdout,
		operation,
		session,
		control.ContextName(),
		followUpContext(control, contextOverride),
	)
}

func applyCloudSessionPackage(
	control *cloud.Client,
	name string,
	packageRef string,
	sessionID string,
	runtimeConfig sessionRuntimeConfig,
	force bool,
	inference *cloud.InferenceSelection,
) (string, *cloud.SessionRecord, error) {
	if sessionID != "" {
		if !isCloudApplyID(sessionID) {
			return "", nil, fmt.Errorf("invalid cloud session id %q", sessionID)
		}
		session, err := control.UpdateSession(sessionID, cloud.SessionUpdateOptions{
			PackageRef: packageRef,
			Force:      force,
		})
		if err != nil && cloud.IsStatus(err, 409) {
			current, getErr := control.GetSession(sessionID)
			if getErr == nil && current.PackageRef == packageRef {
				return "unchanged", current, nil
			}
		}
		return "updated", session, actionableDeploymentUpdateError(err, force)
	}

	session, err := control.CreateSession(cloud.SessionCreateOptions{
		Name:          name,
		PackageRef:    packageRef,
		AgentThinking: runtimeConfig.Thinking,
		Inference:     inference,
	})
	return "created", session, err
}

func actionableDeploymentUpdateError(err error, force bool) error {
	if force {
		return err
	}
	var apiErr *cloud.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 409 || apiErr.Code != "snapshot_pending" {
		return err
	}
	return &snapshotPendingUpdateError{cause: err}
}

type snapshotPendingUpdateError struct {
	cause error
}

func (e *snapshotPendingUpdateError) Error() string {
	return "The current revision has not been snapshotted.\n" +
		"Deploying now means you won’t be able to restore its exact workspace and runtime state.\n\n" +
		"To deploy anyway, retry the same command with --force."
}

func (e *snapshotPendingUpdateError) Unwrap() error {
	return e.cause
}

func validateForceApply(force bool, sessionID string) error {
	if force && !isCloudApplyID(sessionID) {
		return errors.New("--force requires --session with a cloud deployment ID")
	}
	return nil
}

func printSessionReceipt(out io.Writer, operation string, session *sessionapi.Session) {
	if session == nil {
		return
	}
	name := sessionName(*session)
	fmt.Fprintf(out, "%s %s\n\n", operation, name)
	row := displayRow(*session)
	printSummaryField(out, "Status", row.Status)
	printSummaryField(out, "Session", row.Session)
	if session.TotalCostUSD != nil {
		printSummaryField(out, "Cost", formatDetailCost(session.TotalCostUSD))
	}
}

func printCloudSessionReceipt(out io.Writer, operation string, session *cloud.SessionRecord) {
	printCloudSessionReceiptForContext(out, operation, session, "", "")
}

func printCloudSessionReceiptForContext(
	out io.Writer,
	operation string,
	session *cloud.SessionRecord,
	contextName string,
	logsContext string,
) {
	fmt.Fprintf(out, "%s %s\n\n", operation, session.Name)
	printSummaryField(out, "Status", cloudSessionDisplayStatus(*session))
	printSummaryField(out, "Session", session.ID)
	printSummaryField(out, "Revision", shortRevision(session.PackageDigest))
	printCloudInferenceSummary(out, *session)
	if contextName != "" {
		printSummaryField(out, "Context", contextName)
	}
	if session.ServiceURL != nil && strings.TrimSpace(*session.ServiceURL) != "" {
		printSummaryField(out, "Service", strings.TrimSpace(*session.ServiceURL))
	}
	// The hint names a context only when the saved one would select another.
	logsCommand := fmt.Sprintf("telos logs %s", session.ID)
	if logsContext != "" {
		logsCommand = fmt.Sprintf("telos logs --context %s %s", logsContext, session.ID)
	}
	printSummaryField(out, "Logs", logsCommand)
}

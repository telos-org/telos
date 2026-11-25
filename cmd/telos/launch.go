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
	synopsis := fmt.Sprintf("telos %s SPEC.md [flags]", command)
	if command == "apply" {
		synopsis = "telos apply SPEC.md [-m MSG] [flags]" +
			"\n       telos apply PROPOSAL [flags]" +
			"\n       telos apply --goal GOAL [--model MODEL] [--thinking LEVEL] [flags]"
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
	dryRunValue, diffValue := false, false
	dryRun, includeDiff := &dryRunValue, &diffValue
	messageValue := ""
	message := &messageValue
	if command == "apply" {
		sessionID = fs.String("goal", "", "ID of the Goal whose model or thinking to change")
		force = fs.Bool("force", false, "Deploy even if the current revision has not been snapshotted")
		message = fs.String("m", "", "Revision message; defaults to Deploy v<version>")
		dryRun = fs.Bool("dry-run", false, "Preview the apply without publishing or changing a Goal")
		includeDiff = fs.Bool("diff", false, "Show spec and skill file diffs")
	}
	modelHelp := "Model as <provider>/<model> (e.g. openai-codex/gpt-5.5); defaults to $TELOS_MODEL"
	thinkingHelp := "Thinking effort: low, medium, high, or xhigh; defaults to $TELOS_THINKING, then high for local runs"
	if command == "apply" {
		modelHelp = "telos/default, telos/max, or <name>/<model-id> for a saved API key or subscription. Sets a new Goal's model, or with --goal changes one; creation defaults to $TELOS_MODEL, then the workspace default"
		thinkingHelp = "Thinking effort. A new Goal takes low, medium, high, or xhigh and defaults to $TELOS_THINKING; with --goal, any level the model supports"
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
			fmt.Fprintln(os.Stderr, "error: telos apply cannot be used from inside a Telos Goal; use telos run to launch nested specs")
			os.Exit(1)
		}
		if err := validateApplyInferenceFlags(fs, *sessionID, *model, *thinking); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(2)
		}
		if fs.NArg() == 0 {
			if *dryRun {
				preview, err := previewSessionInference(*sessionID, strings.TrimSpace(*model), strings.TrimSpace(*thinking), contextOverride)
				if err != nil {
					exitWithError(err)
				}
				printApplyPreview(os.Stdout, preview, *jsonOut)
				return
			}
			change, err := applySessionInference(*sessionID, strings.TrimSpace(*model), strings.TrimSpace(*thinking), contextOverride)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			if *jsonOut {
				printJSON(change)
			} else {
				printInferenceChange(os.Stdout, change)
			}
			return
		}
		requireArgCount(fs, 1, "one SPEC.md or PROPOSAL")
		requireIDKind("apply", fs.Arg(0), "SPEC.md or a PROPOSAL", "goal", "proposal")
		if isProposalID(fs.Arg(0)) {
			if flagNamesSet(fs, "m", "model", "thinking") {
				exitUsage("-m, --model, and --thinking apply only to a spec")
			}
			if *dryRun || *includeDiff {
				exitUsage("--dry-run and --diff are not yet supported for proposals")
			}
			applyProposal(fs.Arg(0), *force, *jsonOut, contextOverride)
			return
		}
	}
	requireArgCount(fs, 1, "one SPEC.md")
	specArg := fs.Arg(0)
	specPath, hasLocalSpec := existingSpecPath(specArg)

	if ctx, ok := rootSessionContext(); ok {
		if localConfigSet {
			fmt.Fprintln(os.Stderr, "error: local run config flags are not supported inside a Telos Goal")
			os.Exit(1)
		}
		runtimeConfig, err := resolveSessionRuntimeConfigFromFlags(fs, *model, *thinking, *maxCostUSD)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		// Without flags, telosd copies the parent's current settings; the
		// environment may hold the model the worker started with.
		if !flagNameSet(fs, "model") {
			runtimeConfig.Model = ""
		}
		if !flagNameSet(fs, "thinking") {
			runtimeConfig.Thinking = ""
		}
		runCloudChildSession(specArg, ctx, untilConfig, runtimeConfig, *jsonOut, action)
		return
	}

	if command == "apply" {
		if !hasLocalSpec && !strings.HasPrefix(strings.TrimSpace(specArg), "@") {
			fmt.Fprintf(os.Stderr, "error: %v\n", specFileNotFound(specArg))
			os.Exit(1)
		}
		if err := requireCloudLogin(); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		applyCloudControl(specArg, cloudApplyOptions{
			context:     contextOverride,
			json:        *jsonOut,
			dryRun:      *dryRun,
			includeDiff: *includeDiff,
			message:     strings.TrimSpace(*message),
			force:       *force,
			settingsSet: flagNamesSet(fs, "model", "thinking"),
			newGoalSettings: func() (sessionRuntimeConfig, error) {
				return resolveSessionRuntimeConfigFromFlags(fs, *model, *thinking, *maxCostUSD)
			},
		})
		return
	}
	if !hasLocalSpec {
		fmt.Fprintf(os.Stderr, "error: %v\n", specFileNotFound(specArg))
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
			"goal_id":          session.SessionID,
			"goal_dir":         session.SessionDir,
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
	printSummaryField(out, "Goal", session.SessionID)
	printSummaryField(out, "Workspace", session.WorkspaceScope)
	fmt.Fprintln(out)
	printSummaryField(out, "Describe", fmt.Sprintf("cd %s && telos describe %s", workspace, session.SessionID))
	printSummaryField(out, "Logs", fmt.Sprintf("cd %s && telos logs %s", workspace, session.SessionID))
}

// shellArg quotes s only when a shell would otherwise split or expand it.
func shellArg(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789@%+=:,./_-") == "" {
		return s
	}
	return shellQuote(s)
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// validateApplySession accepts only Telos Cloud Goals: telos apply always
// deploys to Telos Cloud, and local work is telos run.
func validateApplySession(sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if isLocalApplyID(sessionID) {
		return fmt.Errorf("%s is a local Goal; telos apply only updates Telos Cloud Goals", sessionID)
	}
	return nil
}

func requireCloudLogin() error {
	configured, err := config.IsConfigured()
	if err != nil {
		return err
	}
	if !configured {
		return errors.New("this command uses Telos Cloud; run `telos login` first")
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
		printJSON(map[string]any{"goal": goalJSON(session)})
		return
	}
	printSessionReceipt(os.Stdout, action, session)
}

type cloudApplyOptions struct {
	context         string
	message         string
	force           bool
	json            bool
	dryRun          bool
	includeDiff     bool
	settingsSet     bool
	newGoalSettings func() (sessionRuntimeConfig, error)
}

func applyCloudControl(
	specArg string,
	options cloudApplyOptions,
) {
	control, err := cloud.ControlClientForContext(options.context)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	prepared, err := prepareCloudApply(control, specArg, options)
	if err != nil {
		exitWithError(err)
	}
	sessionID := ""
	if prepared.goal != nil {
		sessionID = prepared.goal.ID
	}
	if options.dryRun {
		preview, err := previewCloudApply(control, prepared, sessionID, prepared.runtime, options)
		if err != nil {
			exitWithError(err)
		}
		printApplyPreview(os.Stdout, preview, options.json)
		return
	}
	if prepared.baselineErr != nil {
		fmt.Fprintf(os.Stderr,
			"warning: could not read deployed baseline; continuing with server-side validation: %v\n",
			prepared.baselineErr,
		)
	}
	packageRecord := prepared.record
	if prepared.local != nil {
		packageRecord, err = publishSpecPackage(control, prepared.local, prepared.skills, "", prepared.local.version, "")
		if err != nil {
			exitWithError(err)
		}
	}
	var details *applyPreview
	if options.includeDiff {
		details = &applyPreview{Current: prepared.current, Proposed: prepared.proposed}
		if prepared.baselineErr == nil {
			if prepared.local != nil {
				markdown, _, err := spec.ApplyPackageSpec(prepared.local.bytes)
				if err != nil {
					exitWithError(err)
				}
				details.Proposed.Spec = string(markdown)
			}
			addApplyDiff(control, details)
		} else {
			details.Notes = []string{"Content diffs are unavailable."}
		}
	}
	operation, session, err := applyCloudSessionPackage(
		control,
		prepared.name,
		packageRecord.Ref,
		sessionID,
		prepared.runtime,
		options,
		prepared.inference,
	)
	if apiCode(err) == "proposal_required" {
		err = proposalRequiredError(prepared.goal, specArg, err)
	}
	if err != nil {
		exitWithError(err)
	}
	currentRef := ""
	if prepared.current != nil {
		currentRef = prepared.current.Ref
		if session.PackageDigest != "" && session.PackageDigest == prepared.current.Digest {
			operation = "unchanged"
		}
	}
	if options.json {
		result := map[string]any{
			"context":   control.ContextName(),
			"operation": operation,
			"package":   packageRecord,
			"goal":      session,
		}
		if details != nil {
			result["spec_diff"] = details.SpecDiff
			result["skill_diffs"] = details.SkillDiffs
			result["notes"] = details.Notes
		}
		printJSON(result)
		return
	}
	printCloudSessionReceiptForContext(
		os.Stdout,
		operation,
		session,
		control.ContextName(),
		followUpContext(control, options.context),
		currentRef,
	)
	if details != nil {
		printApplyDetails(os.Stdout, details)
	}
}

// applyCloudSessionPackage deploys packageRef to the Goal sessionID, or
// creates a Goal named name when sessionID is empty.
func applyCloudSessionPackage(
	control *cloud.Client,
	name string,
	packageRef string,
	sessionID string,
	runtimeConfig sessionRuntimeConfig,
	opts cloudApplyOptions,
	inference *cloud.InferenceSelection,
) (string, *cloud.SessionRecord, error) {
	if sessionID != "" {
		session, err := control.UpdateSession(sessionID, cloud.SessionUpdateOptions{
			PackageRef:      packageRef,
			RevisionMessage: opts.message,
			Force:           opts.force,
		})
		if err != nil && cloud.IsStatus(err, 409) {
			current, getErr := control.GetSession(sessionID)
			if getErr == nil && current.PackageRef == packageRef {
				return "unchanged", current, nil
			}
		}
		return "updated", session, actionableDeploymentUpdateError(goalNotFound(err, sessionID, control.ContextName()), opts.force)
	}

	session, err := control.CreateSession(cloud.SessionCreateOptions{
		Name:            name,
		PackageRef:      packageRef,
		AgentThinking:   runtimeConfig.Thinking,
		Inference:       inference,
		RevisionMessage: opts.message,
	})
	return "created", session, err
}

// proposalRequiredError says a Goal takes proposals instead of a direct
// deploy, and how to propose specArg.
func proposalRequiredError(goal *cloud.SessionRecord, specArg string, cause error) error {
	if goal == nil {
		return ruleError(cause, "To propose: "+proposeHint(specArg))
	}
	return &messageError{
		message: fmt.Sprintf("%s (%s) requires proposals\nTo propose: %s", goal.Name, goal.ID, proposeHint(specArg)),
		cause:   cause,
	}
}

// goalNamed returns the Goal in control's context named name, or nil when
// there is none.
func goalNamed(control *cloud.Client, name string) (*cloud.SessionRecord, error) {
	goals, err := control.ListSessions()
	if err != nil {
		return nil, err
	}
	for index := range goals {
		// Goals shared by link belong to other workspaces.
		if goals[index].Name == name && goals[index].State != "deleted" && goals[index].AccessSource != "link" {
			return &goals[index], nil
		}
	}
	return nil, nil
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

func printSessionReceipt(out io.Writer, operation string, session *sessionapi.Session) {
	if session == nil {
		return
	}
	name := sessionName(*session)
	fmt.Fprintf(out, "%s %s\n\n", operation, name)
	row := displayRow(*session)
	printSummaryField(out, "Status", row.Status)
	printSummaryField(out, "Goal", row.Session)
	if session.TotalCostUSD != nil {
		printSummaryField(out, "Cost", formatDetailCost(session.TotalCostUSD))
	}
}

func printCloudSessionReceipt(out io.Writer, operation string, session *cloud.SessionRecord) {
	printCloudSessionReceiptForContext(out, operation, session, "", "", "")
}

func printCloudSessionReceiptForContext(
	out io.Writer,
	operation string,
	session *cloud.SessionRecord,
	contextName string,
	logsContext string,
	currentRef string,
) {
	printApplyHeadline(out, operation, session.Name)
	printSummaryField(out, "Status", cloudSessionDisplayStatus(*session))
	printSummaryField(out, "Goal", session.ID)
	printApplyPackage(out, currentRef, session.PackageRef)
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

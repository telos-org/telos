package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/config"
	"github.com/telos-org/telos/internal/sessionapi"
)

func cmdApply(args []string) {
	fs := newCommandFlagSet("apply", "telos apply SPEC.md [flags]\n       telos apply --goal GOAL [--model MODEL] [--thinking LEVEL] [flags]")
	goal := fs.String("goal", "", "ID of the Goal to update")
	force := fs.Bool("force", false, "Deploy even if the current revision has not been snapshotted")
	model := fs.String("model", "", "telos/default, telos/max, or <name>/<model-id> for a saved API key or subscription. Creation defaults to $TELOS_MODEL, then the workspace default; updates require an explicit flag without SPEC.md")
	thinking := fs.String("thinking", "", "Creation: low, medium, high, or xhigh; defaults to $TELOS_THINKING. Updates: a model-supported level for the next turn, explicitly supplied without SPEC.md")
	jsonOut := fs.Bool("json", false, "JSON output")
	contextFlag := cloudContextFlag(fs)
	parseFlags(fs, args)
	*goal = strings.TrimSpace(*goal)
	contextOverride, err := cloudContextOverride(fs, *contextFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	if insideTelosSession() {
		exitWithError(errors.New("telos apply cannot be used from inside a Telos Goal; use telos run to launch nested specs"))
	}
	if err := validateApplyInferenceFlags(fs, *goal, *model, *thinking); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	if fs.NArg() == 0 {
		change, err := applySessionInference(*goal, strings.TrimSpace(*model), strings.TrimSpace(*thinking), contextOverride)
		if err != nil {
			exitWithError(err)
		}
		if *jsonOut {
			printJSON(change)
		} else {
			printInferenceChange(os.Stdout, change)
		}
		return
	}
	requireArgCount(fs, 1, "one SPEC.md")
	if err := validateCloudSessionContext(*goal, contextOverride); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	specArg := fs.Arg(0)
	_, hasLocalSpec := existingSpecPath(specArg)
	if err := validateApplySession(*goal); err != nil {
		exitWithError(err)
	}
	if err := validateForceApply(*force, *goal); err != nil {
		exitWithError(err)
	}
	if !hasLocalSpec && !strings.HasPrefix(strings.TrimSpace(specArg), "@") {
		exitWithError(specFileNotFound(specArg))
	}
	if err := requireCloudLogin(); err != nil {
		exitWithError(err)
	}
	var runtimeConfig sessionRuntimeConfig
	if *goal == "" {
		runtimeConfig, err = resolveSessionRuntimeConfigFromFlags(fs, *model, *thinking, 0)
		if err != nil {
			exitWithError(err)
		}
	}
	applyCloudControl(specArg, *goal, runtimeConfig, *force, *jsonOut, contextOverride)
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
		return errors.New("telos apply deploys to Telos Cloud; run `telos login` first")
	}
	return nil
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
	var pkg *specPackage
	packageName := ""
	if reference != nil {
		packageName = reference.name
	} else {
		pkg, err = packageSpec(specArg, contextOverride)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		packageName = pkg.name
	}
	var inference *cloud.InferenceSelection
	if sessionID == "" {
		if err := refuseDuplicateGoal(control, "apply", specArg, packageName, contextOverride); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		inference, err = resolveCloudInference(control, runtimeConfig.Model)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	}
	var packageRecord *cloud.PackageVersionRecord
	if reference != nil {
		packageRecord, err = registryPackageForApply(control, *reference)
	} else {
		packageRecord, err = pushSpecPackage(control, pkg, "")
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
			"goal":      session,
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
			return "", nil, fmt.Errorf("invalid Goal ID %q", sessionID)
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
		return "updated", session, actionableDeploymentUpdateError(goalNotFound(err, sessionID, control.ContextName()), force)
	}

	session, err := control.CreateSession(cloud.SessionCreateOptions{
		Name:          name,
		PackageRef:    packageRef,
		AgentThinking: runtimeConfig.Thinking,
		Inference:     inference,
	})
	return "created", session, err
}

// refuseDuplicateGoal fails when control's context already has a Goal named
// name. Names are unique within a context: apply creates a Goal, and only
// --goal updates one.
func refuseDuplicateGoal(control *cloud.Client, command, specArg, name, contextOverride string) error {
	goals, err := control.ListSessions()
	if err != nil {
		return err
	}
	for _, goal := range goals {
		if goal.Name == name && goal.State != "deleted" {
			return &duplicateGoalError{
				name:     name,
				context:  control.ContextName(),
				id:       goal.ID,
				command:  command,
				specArg:  specArg,
				followUp: followUpContext(control, contextOverride),
			}
		}
	}
	return nil
}

type duplicateGoalError struct {
	name     string
	context  string
	id       string
	command  string
	specArg  string
	followUp string
}

func (e *duplicateGoalError) Error() string {
	update := fmt.Sprintf("telos %s %s --goal %s", e.command, shellArg(e.specArg), e.id)
	if e.followUp != "" {
		update += " --context " + e.followUp
	}
	return fmt.Sprintf("a Goal named %s already exists in %s (%s)\nTo update it: %s", e.name, e.context, e.id, update)
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
		return errors.New("--force requires --goal")
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
	printSummaryField(out, "Goal", row.Session)
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
	printSummaryField(out, "Goal", session.ID)
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

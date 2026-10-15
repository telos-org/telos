package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/telos-org/telos/internal/cloud"
	"golang.org/x/term"
)

type cloudPlanInput struct {
	specArg         string
	sessionID       string
	runtimeConfig   sessionRuntimeConfig
	force           bool
	contextOverride string
	mode            string
	yes             bool
	revisionMessage string
}

type cloudPlanResult struct {
	request    *cloud.ChangeRequestRecord
	specName   string
	currentRef string
}

func normalizePlanMessage(message string, required bool) (string, error) {
	if !utf8.ValidString(message) {
		return "", fmt.Errorf("--message must contain valid UTF-8 text")
	}
	for _, character := range message {
		if unicode.IsControl(character) || character == '\u2028' || character == '\u2029' {
			return "", fmt.Errorf("--message must be a single line without control characters")
		}
	}
	message = strings.TrimSpace(message)
	if required && message == "" {
		return "", fmt.Errorf("--message is required when saving or applying a plan; describe the change with --message \"Record who added each book\"")
	}
	if utf8.RuneCountInString(message) > 200 {
		return "", fmt.Errorf("--message must be 200 characters or fewer")
	}
	return message, nil
}

func checkFreshApplyConfirmation(yes, jsonOut, stdinTTY, promptTTY bool) error {
	if yes {
		return nil
	}
	if jsonOut || !stdinTTY || !promptTTY {
		return fmt.Errorf("interactive confirmation is unavailable; use `telos apply SPEC.md --message \"Describe the change\" --yes` to confirm automatically, or `telos plan SPEC.md --out=change.plan --message \"Describe the change\"` to save a plan for later")
	}
	return nil
}

func cloudPlanPreflight(control *cloud.Client, sessionID, mode string) error {
	capabilities, err := control.DeploymentCapabilities()
	if err != nil {
		return err
	}
	if !capabilities.DeploymentPlans {
		return fmt.Errorf("this Cloud server does not support deployment plans; update Cloud before using plan or apply")
	}
	access, err := control.DeploymentPlanAccess(sessionID)
	if err != nil {
		return err
	}
	if mode == "apply" && !access.CanApply {
		if access.RequiresChangeRequests != nil && !*access.RequiresChangeRequests {
			return fmt.Errorf("deployment editing permission is required; use `telos plan SPEC.md --out=change.plan --message \"Describe the change\"` to submit a Change Request for an authorized editor to confirm")
		}
		return fmt.Errorf("Apply permission is required; use `telos plan SPEC.md --out=change.plan --message \"Describe the change\"` to submit a Change Request for an owner or admin to confirm")
	}
	if !access.CanPlan {
		return fmt.Errorf("you do not have permission to plan changes for this deployment or context")
	}
	return nil
}

func createCloudPlan(control *cloud.Client, input cloudPlanInput) (*cloudPlanResult, error) {
	message, err := normalizePlanMessage(input.revisionMessage, input.mode != "preview")
	if err != nil {
		return nil, err
	}
	if err := cloudPlanPreflight(control, input.sessionID, input.mode); err != nil {
		return nil, err
	}
	options := cloud.DeploymentPlanOptions{
		Mode: input.mode, DeploymentID: input.sessionID,
	}
	var currentRef string
	if input.sessionID != "" {
		current, err := control.GetSession(input.sessionID)
		if err != nil {
			return nil, err
		}
		currentRef = current.PackageRef
		options.Update = &cloud.SessionUpdateOptions{
			Force: input.force, ExpectedCurrentRevisionID: current.CurrentRevisionID,
			RevisionMessage: message,
		}
	} else {
		inference, err := resolveCloudInference(control, input.runtimeConfig.Model)
		if err != nil {
			return nil, err
		}
		options.Create = &cloud.SessionCreateOptions{
			AgentThinking: input.runtimeConfig.Thinking, Inference: inference,
			RevisionMessage: message,
		}
	}
	record, name, err := stageCloudPlanPackage(control, input)
	if err != nil {
		return nil, err
	}
	if options.Create != nil {
		options.Create.Name = name
		options.Create.PackageRef = record.Ref
	} else {
		options.Update.PackageRef = record.Ref
	}
	request, err := control.CreateDeploymentPlan(options)
	if err != nil {
		return nil, actionableDeploymentUpdateError(err, input.force)
	}
	if request.Mode != input.mode && (request.Kind == "plan" || input.mode == "preview" || request.Mode == "preview") {
		return nil, fmt.Errorf("Cloud returned a %q request for a %q plan", request.Mode, input.mode)
	}
	if input.mode != "apply" && request.Preview == nil {
		return nil, fmt.Errorf("Cloud returned a plan without a preview; request %s", request.ID)
	}
	return &cloudPlanResult{
		request: request, specName: name, currentRef: currentRef,
	}, nil
}

func stageCloudPlanPackage(control *cloud.Client, input cloudPlanInput) (*cloud.PackageVersionRecord, string, error) {
	checkName := func(name string) error {
		if input.sessionID != "" {
			return nil
		}
		command := "plan"
		if input.mode == "apply" {
			command = "apply"
		}
		return refuseDuplicateGoal(control, command, input.specArg, name, input.contextOverride)
	}
	if strings.HasPrefix(strings.TrimSpace(input.specArg), "@") {
		reference, err := parsePackageReference(input.specArg)
		if err != nil {
			return nil, "", err
		}
		if err := checkName(reference.name); err != nil {
			return nil, "", err
		}
		record, err := registryPackageForApply(control, reference)
		return record, reference.name, err
	}
	path, ok := existingSpecPath(input.specArg)
	if !ok {
		return nil, "", specFileNotFound(input.specArg)
	}
	var record *cloud.PackageVersionRecord
	var name string
	err := withPlanRegistrySkills(path, input.contextOverride, func() error {
		pkg, err := packageSpec(path, input.contextOverride)
		if err != nil {
			return err
		}
		name = pkg.name
		if err := checkName(name); err != nil {
			return err
		}
		record, err = pushSpecPackage(control.PlanArtifactClient(), pkg, "")
		return err
	})
	return record, name, err
}

func runCloudPlan(input cloudPlanInput, output string, jsonOut bool) error {
	var writer *savedPlanWriter
	var err error
	if input.mode == "saved" {
		writer, err = prepareSavedPlanWriter(output)
		if err != nil {
			return err
		}
		defer writer.close()
	}
	control, err := cloud.ControlClientForContext(input.contextOverride)
	if err != nil {
		return err
	}
	var orgID string
	if writer != nil {
		orgID, err = cloudPlanOrgID(control)
		if err != nil {
			return err
		}
	}
	plan, err := createCloudPlan(control, input)
	if err != nil {
		return err
	}
	request := plan.request
	if writer != nil {
		if err := writer.save(newSavedDeploymentPlan(control, request, orgID)); err != nil {
			return fmt.Errorf("Plan %s was saved, but its local reference could not be written: %w; review it at %s", request.ID, err, cloudRequestReviewURL(control, *request))
		}
	}
	if jsonOut {
		printDeploymentPlanJSON(control, request, output)
		return nil
	}
	if request.Mode == "preview" {
		printDeploymentPreview(os.Stdout, control, plan)
	} else {
		printDeploymentPlan(os.Stdout, control, request)
	}
	if output != "" {
		printSummaryField(os.Stdout, "Saved", output)
	}
	return nil
}

func runCloudApply(input cloudPlanInput, jsonOut bool) error {
	if err := checkFreshApplyConfirmation(input.yes, jsonOut,
		term.IsTerminal(int(os.Stdin.Fd())), term.IsTerminal(int(os.Stderr.Fd()))); err != nil {
		return err
	}
	control, err := cloud.ControlClientForContext(input.contextOverride)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	plan, err := createCloudPlan(control.WithContext(ctx), input)
	if err != nil {
		return err
	}
	request := plan.request
	if !jsonOut {
		printDeploymentPlan(os.Stdout, control, request)
	}
	request, err = awaitCloudApply(ctx, control, request, input.yes, os.Stdin,
		planOutputWriter(jsonOut), os.Stderr, time.Second)
	if err != nil {
		return err
	}
	if jsonOut {
		printDeploymentPlanJSON(control, request, "")
	} else {
		printDeploymentPlanResult(os.Stdout, control, request)
	}
	return nil
}

func runSavedCloudApply(bookmark *savedDeploymentPlan, contextOverride string, jsonOut bool) error {
	control, err := cloud.ControlClientForContext(contextOverride)
	if err != nil {
		return err
	}
	if err := validateSavedDeploymentPlan(control, bookmark); err != nil {
		return err
	}
	planID := bookmark.ChangeRequestID
	if bookmark.PlanID != "" {
		planID = bookmark.PlanID
	}
	request, err := control.GetChangeRequest(bookmark.DeploymentID, planID)
	if err != nil {
		return err
	}
	if request.Mode != "saved" && request.Mode != "apply" {
		return fmt.Errorf("this file does not identify a saved plan; preview-only plans cannot be applied")
	}
	if request.ID != planID || request.DeploymentID != bookmark.DeploymentID || (request.Kind == "plan") != (bookmark.PlanID != "") {
		return fmt.Errorf("Cloud returned a different plan than the saved file")
	}
	if err := validateSavedRequest(bookmark, request); err != nil {
		return err
	}
	sessionID := bookmark.DeploymentID
	if request.Kind == "plan" && request.Action == "create" {
		sessionID = ""
	}
	if err := cloudPlanPreflight(control, sessionID, "apply"); err != nil {
		return err
	}
	if !jsonOut {
		printDeploymentPlan(os.Stdout, control, request)
	}
	if request.Kind == "plan" && request.Status != "applied" {
		request, err = confirmCloudRequest(control, request)
		if err != nil {
			return err
		}
	} else if request.Status != "applied" && request.Status != "applying" && request.Status != "confirmed" {
		if request.Status != "awaiting_confirmation" {
			return requestStoppedError(request)
		}
		request, err = confirmCloudRequest(control, request)
		if err != nil {
			return err
		}
	}
	// Explicit saved-plan application is itself confirmation. Once confirmed,
	// a disconnected terminal cannot revoke that authorization.
	if jsonOut {
		printDeploymentPlanJSON(control, request, "")
	} else {
		printDeploymentPlanResult(os.Stdout, control, request)
	}
	return nil
}

func requestStarted(request *cloud.ChangeRequestRecord) bool {
	return request.Status == "confirmed" || request.Status == "applying" || request.Status == "applied"
}

func requestStoppedError(request *cloud.ChangeRequestRecord) error {
	reason := ""
	if request.Error != nil {
		reason = ": " + *request.Error
	}
	return fmt.Errorf("%s %s is %s%s; inspect its dashboard page before submitting a new plan", proposalLabel(request), request.ID, changeRequestStatus(request.Status), reason)
}

func proposalLabel(request *cloud.ChangeRequestRecord) string {
	if request.Kind == "plan" {
		return "Plan"
	}
	return "Change Request"
}

func directPlanError(control *cloud.Client, request *cloud.ChangeRequestRecord) error {
	reason := "The plan has not been applied."
	if request.Error != nil {
		reason = *request.Error
	}
	return fmt.Errorf("%s Inspect the plan and retry at %s", reason, cloudRequestReviewURL(control, *request))
}

func snapshotConfirmationError(control *cloud.Client, request *cloud.ChangeRequestRecord) error {
	if request.Status != "awaiting_confirmation" || request.ErrorCode == nil || *request.ErrorCode != "snapshot_pending" {
		return nil
	}
	reason := "The current deployment has no available snapshot."
	if request.Error != nil {
		reason = *request.Error
	}
	return fmt.Errorf("%s %s is still pending. %s Confirm again after the snapshot is ready, or choose Apply Now to proceed without one: %s", proposalLabel(request), request.ID, reason, cloudRequestReviewURL(control, *request))
}

// A poll or recovery read must never replace the proposal the user reviewed.
func sameCloudPlan(expected, current *cloud.ChangeRequestRecord) bool {
	if expected.ID != current.ID || expected.DeploymentID != current.DeploymentID || (expected.Kind == "plan") != (current.Kind == "plan") {
		return false
	}
	if expected.UpdateNumber > 0 && expected.UpdateNumber != current.UpdateNumber {
		return false
	}
	if expected.PreparedPlanID != "" {
		return expected.PreparedPlanID == current.PreparedPlanID
	}
	return current.LegacyConfirmationValid == nil || *current.LegacyConfirmationValid
}

func replacedPlanError(request *cloud.ChangeRequestRecord) error {
	return fmt.Errorf("the plan for %s changed while you were reviewing; nothing was confirmed by this command; review the latest update in the dashboard", request.ID)
}

func confirmCloudRequest(control *cloud.Client, request *cloud.ChangeRequestRecord) (*cloud.ChangeRequestRecord, error) {
	confirmed, err := control.ConfirmChangeRequest(*request)
	if err == nil {
		if !sameCloudPlan(request, confirmed) {
			return nil, replacedPlanError(request)
		}
		if err := snapshotConfirmationError(control, confirmed); err != nil {
			return nil, err
		}
		if confirmed.Kind == "plan" && (confirmed.Error != nil || !requestStarted(confirmed)) {
			return nil, directPlanError(control, confirmed)
		}
		if !requestStarted(confirmed) {
			return nil, requestStoppedError(confirmed)
		}
		return confirmed, nil
	}
	// A dashboard confirmation or a lost acknowledgement may have won the race.
	current, readErr := control.GetChangeRequest(request.DeploymentID, request.ID)
	if readErr == nil {
		if !sameCloudPlan(request, current) {
			return nil, replacedPlanError(request)
		}
		if err := snapshotConfirmationError(control, current); err != nil {
			return nil, err
		}
	}
	if readErr == nil && requestStarted(current) {
		if current.Kind == "plan" && current.Error != nil {
			return nil, directPlanError(control, current)
		}
		return current, nil
	}
	return nil, err
}

func awaitCloudApply(ctx context.Context, control *cloud.Client, request *cloud.ChangeRequestRecord,
	yes bool, input io.Reader, previewOut, promptOut io.Writer, pollInterval time.Duration,
) (*cloud.ChangeRequestRecord, error) {
	stopped := fmt.Errorf("stopped waiting for %s; review or discard it at %s", request.ID, cloudRequestReviewURL(control, *request))
	control = control.WithContext(ctx)
	confirm := func() (*cloud.ChangeRequestRecord, error) {
		confirmed, err := confirmCloudRequest(control, request)
		if err != nil && ctx.Err() != nil {
			return nil, stopped
		}
		return confirmed, err
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	var answer <-chan bool
	printedPreview := request.Preview != nil
	for {
		if request.Preview != nil && !printedPreview {
			printDeploymentPlan(previewOut, control, request)
			printedPreview = true
		}
		if err := snapshotConfirmationError(control, request); err != nil {
			return nil, err
		}
		if request.Kind == "plan" && request.Error != nil {
			return nil, directPlanError(control, request)
		}
		if requestStarted(request) {
			return request, nil
		}
		if ctx.Err() != nil {
			return nil, stopped
		}
		if request.Status != "queued" && request.Status != "awaiting_confirmation" {
			return nil, requestStoppedError(request)
		}
		if request.Status == "awaiting_confirmation" && answer == nil {
			if request.Preview == nil {
				return nil, fmt.Errorf("Cloud did not provide a reviewable plan for request %s", request.ID)
			}
			if request.PlanStale {
				return nil, fmt.Errorf("the deployment changed after this plan was prepared; nothing was confirmed; review a new plan at %s", cloudRequestReviewURL(control, *request))
			}
			// --yes authorizes this exact prepared plan, never a future update.
			// In particular, do not send auto_confirm when creating the proposal.
			if yes {
				return confirm()
			}
			fmt.Fprint(promptOut, "Apply these changes? Type yes to confirm: ")
			response := make(chan bool, 1)
			answer = response
			go func() {
				line, err := bufio.NewReader(input).ReadString('\n')
				// EOF, including a partial line, never supplies confirmation.
				response <- err == nil && strings.TrimSpace(line) == "yes"
			}()
		}
		select {
		case <-ctx.Done():
			return nil, stopped
		case yes := <-answer:
			if !yes || ctx.Err() != nil {
				return nil, stopped
			}
			return confirm()
		case <-ticker.C:
			current, err := control.GetChangeRequest(request.DeploymentID, request.ID)
			if err != nil {
				if ctx.Err() != nil {
					return nil, stopped
				}
				return nil, fmt.Errorf("could not observe plan %s: %w; inspect it at %s", request.ID, err, cloudRequestReviewURL(control, *request))
			}
			if !sameCloudPlan(request, current) {
				return nil, replacedPlanError(request)
			}
			request = current
		}
	}
}

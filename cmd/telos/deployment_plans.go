package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/telos-org/telos/internal/cloud"
)

type cloudPlanInput struct {
	specArg         string
	sessionID       string
	runtimeConfig   sessionRuntimeConfig
	force           bool
	contextOverride string
	mode            string
	revisionMessage string
}

type cloudPlanResult struct {
	request    *cloud.ChangeRequestRecord
	specName   string
	currentRef string
}

func resolveCloudPlanRuntimeConfig(fs *flag.FlagSet, goalID, model, thinking string) (sessionRuntimeConfig, error) {
	if goalID != "" {
		if flagNamesSet(fs, "model", "thinking") {
			return sessionRuntimeConfig{}, fmt.Errorf("--model and --thinking can only configure a new Cloud deployment")
		}
		return sessionRuntimeConfig{}, nil
	}
	return resolveSessionRuntimeConfigFromFlags(fs, model, thinking, 0)
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
		return "", fmt.Errorf("--message is required when saving a plan; describe the change with --message \"Record who added each book\"")
	}
	if utf8.RuneCountInString(message) > 200 {
		return "", fmt.Errorf("--message must be 200 characters or fewer")
	}
	return message, nil
}

func cloudPlanPreflight(control *cloud.Client, sessionID string) error {
	capabilities, err := control.DeploymentCapabilities()
	if err != nil {
		return err
	}
	if !capabilities.DeploymentPlans {
		return fmt.Errorf("this Cloud server does not support deployment plans; update Cloud before using plan")
	}
	access, err := control.DeploymentPlanAccess(sessionID)
	if err != nil {
		return err
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
	if err := cloudPlanPreflight(control, input.sessionID); err != nil {
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
	if input.sessionID != "" && request.DeploymentID != input.sessionID {
		return nil, fmt.Errorf("Cloud returned a plan for a different Goal; expected %s, got %s", input.sessionID, request.DeploymentID)
	}
	if request.Mode != input.mode && (request.IsPlan() || input.mode == "preview" || request.Mode == "preview") {
		return nil, fmt.Errorf("Cloud returned a %q request for a %q plan", request.Mode, input.mode)
	}
	if request.Preview == nil {
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
		return refuseDuplicateGoal(control, "plan", input.specArg, name, input.contextOverride)
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
	err := withPlanRegistrySkills(path, control, func() error {
		pkg, err := compileSpecPackage(path)
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

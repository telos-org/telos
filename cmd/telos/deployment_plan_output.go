package main

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	internaldiff "github.com/rogpeppe/go-internal/diff"
	"github.com/telos-org/telos/internal/cloud"
)

func deploymentPlanState(markdown string, skills []cloud.DeploymentPlanSkill) planSpecState {
	state := planSpecState{Skills: []planSkillLock{}}
	if strings.TrimSpace(markdown) != "" {
		if parsed, err := planSpecStateFromMarkdown([]byte(markdown)); err == nil {
			state = parsed
		}
	}
	for _, skill := range skills {
		state.Skills = append(state.Skills, planSkillLock{Name: skill.Name, Ref: skill.Ref, Digest: skill.Digest, Starred: skill.Starred})
	}
	slices.SortFunc(state.Skills, func(a, b planSkillLock) int { return strings.Compare(a.Name, b.Name) })
	return state
}

func printDeploymentPreview(out io.Writer, control *cloud.Client, plan *cloudPlanResult) {
	request := plan.request
	printSummaryField(out, "Spec", plan.specName)
	printSummaryField(out, "Target", "cloud")
	printSummaryField(out, "Context", control.ContextName())
	printSummaryField(out, "Goal", request.DeploymentID)
	if plan.currentRef != "" {
		printSummaryField(out, "Current", plan.currentRef)
	}
	if request.Kind == "plan" {
		printSummaryField(out, "Preview", cloudRequestReviewURL(control, *request))
	} else {
		printSummaryField(out, "Review", cloudRequestReviewURL(control, *request))
	}
	if request.Error != nil {
		printSummaryField(out, "Reason", *request.Error)
	}
	printDeploymentPlanDetails(out, request)
}

func printDeploymentPlan(out io.Writer, control *cloud.Client, request *cloud.ChangeRequestRecord) {
	if request.Kind == "plan" {
		printSummaryField(out, "Plan", request.ID)
	} else {
		printSummaryField(out, "Request", request.ID)
		if request.UpdateNumber > 0 {
			printSummaryField(out, "Update", fmt.Sprint(request.UpdateNumber))
		}
		printSummaryField(out, "Status", changeRequestStatus(request.Status))
	}
	if request.Message != "" {
		printSummaryField(out, "Message", request.Message)
	}
	if request.Error != nil {
		printSummaryField(out, "Reason", *request.Error)
	}
	printSummaryField(out, "Context", control.ContextName())
	printSummaryField(out, "Goal", request.DeploymentID)
	if request.Kind == "plan" {
		printSummaryField(out, "Preview", cloudRequestReviewURL(control, *request))
	} else {
		printSummaryField(out, "Review", cloudRequestReviewURL(control, *request))
	}
	printDeploymentPlanDetails(out, request)
}

func printDeploymentPlanDetails(out io.Writer, request *cloud.ChangeRequestRecord) {
	if creation := request.Creation; creation != nil {
		if request.Mode != "preview" {
			printSummaryField(out, "Name", creation.Name)
		}
		if creation.AgentModel != nil {
			printSummaryField(out, "Model", *creation.AgentModel)
		}
		if creation.AgentThinking != nil {
			printSummaryField(out, "Thinking", *creation.AgentThinking)
		}
		if creation.Inference != nil {
			selection, _ := json.Marshal(creation.Inference)
			printSummaryField(out, "Inference", string(selection))
		}
		printSummaryField(out, "Secrets", firstNonEmpty(strings.Join(creation.SecretIDs, ", "), "none"))
	}
	if request.Force {
		printSummaryField(out, "Snapshot", "bypass allowed (--force)")
		fmt.Fprintln(out, "The current revision may not have a restore point when this change applies.")
	}
	if request.Preview == nil {
		fmt.Fprintln(out, "Preparing the plan.")
		return
	}
	if request.BaseRevisionID != nil {
		printSummaryField(out, "Base", *request.BaseRevisionID)
	}
	preview := request.Preview
	printPlanStateDelta(out, deploymentPlanState(preview.BaseSpec, preview.BaseSkills), deploymentPlanState(preview.ProposedSpec, preview.ProposedSkills))
	diff := internaldiff.Diff("deployed/SPEC.md", []byte(preview.BaseSpec), "proposed/SPEC.md", []byte(preview.ProposedSpec))
	if len(diff) > 0 {
		fmt.Fprintln(out)
		fmt.Fprint(out, string(diff))
	} else {
		fmt.Fprintln(out, "No spec changes.")
	}
	if request.Mode == "preview" {
		fmt.Fprintln(out, "Preview only. Use --out=FILE with --message to save a Change Request that can be applied.")
	}
}

func printDeploymentPlanResult(out io.Writer, control *cloud.Client, request *cloud.ChangeRequestRecord) {
	fmt.Fprintln(out)
	if request.Kind == "plan" {
		printSummaryField(out, "Plan", request.ID)
		printSummaryField(out, "Deployment", cloudPlanDeploymentURL(control, *request))
	} else {
		printSummaryField(out, "Request", request.ID)
	}
	printSummaryField(out, "Status", changeRequestStatus(request.Status))
	if request.Error != nil {
		printSummaryField(out, "Reason", *request.Error)
	}
	if request.ResultRevisionID != nil {
		printSummaryField(out, "Revision", *request.ResultRevisionID)
	}
	printSummaryField(out, "Describe", "telos describe "+request.DeploymentID+" --context "+control.ContextName())
}

func printDeploymentPlanJSON(control *cloud.Client, request *cloud.ChangeRequestRecord, output string) {
	operation := request.Status
	if request.Mode == "preview" {
		operation = "preview"
	} else if !requestStarted(request) {
		operation = "requested"
	}
	receipt := map[string]any{
		"operation": operation, "context": control.ContextName(), "change_request": request,
		"review_url": cloudRequestReviewURL(control, *request), "goal_id": request.DeploymentID,
	}
	if request.Kind == "plan" {
		delete(receipt, "change_request")
		receipt["plan"] = request
		receipt["preview_url"] = cloudRequestReviewURL(control, *request)
		if operation == "requested" {
			receipt["operation"] = "planned"
		}
		if requestStarted(request) {
			receipt["deployment_url"] = cloudPlanDeploymentURL(control, *request)
		}
	}
	if request.PackageRef != "" {
		// Cloud may prepare a new package version before confirmation. Report that final artifact,
		// never the original upload or its metadata.
		pkg := map[string]string{"ref": request.PackageRef, "digest": request.PackageDigest}
		if reference, err := parsePackageReference(request.PackageRef); err == nil {
			pkg["scope"], pkg["name"], pkg["version"] = reference.scope, reference.name, reference.version
		}
		receipt["package"] = pkg
	}
	if output != "" {
		receipt["plan_file"] = output
	}
	printJSON(receipt)
}

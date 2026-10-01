package main

import (
	"fmt"
	"os"

	"github.com/telos-org/telos/internal/cloud"
)

func runCloudRequestUpdate(specArg, requestID, contextOverride, message, output string, jsonOut bool) error {
	message, err := normalizePlanMessage(message, false)
	if err != nil {
		return err
	}
	control, err := cloud.ControlClientForContext(contextOverride)
	if err != nil {
		return err
	}
	request, err := editableCloudRequest(control, requestID)
	if err != nil {
		return err
	}
	if request.PlanStale {
		return &requestMergeError{code: "merge_required", message: fmt.Sprintf("request %s is based on an older deployment; reconcile it with telos plan --request %s --reconcile DIR before updating; your local files are unchanged", requestID, requestID)}
	}
	var writer *savedPlanWriter
	var orgID string
	if output != "" {
		writer, err = prepareSavedPlanWriter(output)
		if err != nil {
			return err
		}
		defer writer.close()
		orgID, err = cloudPlanOrgID(control)
		if err != nil {
			return err
		}
	}
	record, _, err := stageCloudPlanPackage(control, specArg, contextOverride)
	if err != nil {
		return err
	}
	updated, err := control.UpdateChangeRequest(*request, cloud.RequestUpdateOptions{
		ExpectedUpdateNumber: request.UpdateNumber, ExpectedCurrentRevisionID: request.CurrentRevisionID,
		PackageRef: record.Ref, RevisionMessage: message,
	})
	if err != nil {
		return err
	}
	if updated.ID != request.ID || updated.DeploymentID != request.DeploymentID || updated.UpdateNumber != request.UpdateNumber+1 {
		return fmt.Errorf("Cloud returned an unexpected request update; inspect request %s in the dashboard", requestID)
	}
	return writeUpdatedRequestPlan(control, updated, writer, orgID, output, jsonOut)
}

func editableCloudRequest(control *cloud.Client, requestID string) (*cloud.ChangeRequestRecord, error) {
	request, err := control.FindChangeRequest(requestID)
	if err != nil {
		return nil, err
	}
	if request.ID != requestID || request.UpdateNumber < 1 {
		return nil, fmt.Errorf("Cloud did not return a versioned Change Request; update Cloud before using --request")
	}
	if !request.CanEdit || (request.Action != "create" && request.Action != "update") {
		return nil, fmt.Errorf("request %s cannot be edited; only authorized authors/managers can update an unconfirmed create or update request", requestID)
	}
	return request, nil
}

func writeUpdatedRequestPlan(control *cloud.Client, updated *cloud.ChangeRequestRecord, writer *savedPlanWriter, orgID, output string, jsonOut bool) error {
	if writer != nil {
		if updated.PreparedPlanID == "" {
			return fmt.Errorf("request %s was updated but has no prepared plan; inspect %s", updated.ID, cloudRequestReviewURL(control, *updated))
		}
		if err := writer.save(newSavedDeploymentPlan(control, updated, orgID)); err != nil {
			return fmt.Errorf("request %s was updated, but its saved reference could not be written: %w; review %s", updated.ID, err, cloudRequestReviewURL(control, *updated))
		}
	}
	if jsonOut {
		printDeploymentPlanJSON(control, updated, output)
	} else {
		printDeploymentPlan(os.Stdout, control, updated)
		if output != "" {
			printSummaryField(os.Stdout, "Saved", output)
		}
	}
	return nil
}

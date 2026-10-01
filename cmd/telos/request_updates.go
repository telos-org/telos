package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/telos-org/telos/internal/cloud"
)

func runCloudRequestUpdate(specArg, requestID, contextOverride, message, output string, jsonOut bool, choices ...requestConflictChoice) error {
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
	if strings.HasPrefix(specArg, "@") {
		return fmt.Errorf("updating a request needs its local working files; run telos get %s --output DIR, edit its SPEC.md, then rerun plan with --request %s", requestID, requestID)
	}
	root, name, err := requestRoot(specArg)
	if err != nil {
		return err
	}
	unlock, err := lockRequestWorkspace(root)
	if err != nil {
		return err
	}
	defer unlock()
	if err := checkOtherRequestMerges(root, requestID); err != nil {
		return err
	}
	workspace, err := loadRequestMergeWorkspace(root, name, requestID)
	if err != nil {
		return err
	}
	if workspace == nil {
		return &requestMergeError{code: "untracked_request", message: fmt.Sprintf("this directory does not track request %s; run telos get %s --output DIR and make your edits in that checkout, or use the original tracked directory; your files are unchanged", requestID, requestID)}
	}
	if err := validateSavedDeploymentPlan(control, &savedDeploymentPlan{Context: workspace.Context, OrgID: workspace.OrgID, APIEndpoint: workspace.APIEndpoint}); err != nil {
		return err
	}
	if workspace.DeploymentID != request.DeploymentID {
		return fmt.Errorf("request deployment changed; local files are preserved")
	}
	if workspace.Saved == nil && workspace.Outgoing == nil && (workspace.Merge.UpdateNumber != request.UpdateNumber || workspace.Merge.PreparedPlanID != request.PreparedPlanID) {
		return &requestMergeError{code: "stale_request", message: fmt.Sprintf("request %s changed since this directory's last plan; check out the latest with telos get %s --output DIR and transfer your edits after reviewing it; your files are preserved", requestID, requestID)}
	}
	var writer *savedPlanWriter
	if output != "" {
		writer, err = prepareSavedPlanWriter(output)
		if err != nil {
			return err
		}
		defer writer.close()
	}
	if workspace.Saved != nil {
		if workspace.Saved.UpdateNumber != request.UpdateNumber || workspace.Saved.PreparedPlanID != request.PreparedPlanID {
			return fmt.Errorf("request changed while local synchronization was pending; local files and recovery state are preserved; inspect %s", cloudRequestReviewURL(control, *request))
		}
		return finishMergedRequest(control, workspace, writer, output, jsonOut)
	}
	if workspace.Merge.MergeID != "" {
		if workspace.Outgoing == nil && !sameRequestRevision(workspace.Merge.CurrentRevisionID, request.CurrentRevisionID) {
			return &requestMergeError{code: "stale_merge", message: fmt.Sprintf("the deployment changed while you resolved conflicts; your files are preserved; check out request %s with telos get %s --output DIR, transfer your intended edits, and run plan again", requestID, requestID)}
		}
		if !workspace.Materialized {
			merged := map[string]*cloud.MergeFile{}
			for _, file := range workspace.Merge.Files {
				merged[file.Path] = file.Merged
			}
			if err := workspace.materialize(merged, true); err != nil {
				return err
			}
			workspace.Materialized = true
			if err := workspace.save(); err != nil {
				return err
			}
			if len(workspace.Merge.Conflicts) > 0 {
				return requestMergeConflictError(workspace, workspace.Merge.Conflicts)
			}
		}
		return saveMergedRequest(control, request, workspace, choices, message, writer, output, jsonOut)
	}
	if len(choices) > 0 {
		return fmt.Errorf("--resolve requires an existing local conflict; first run plan with --request %s", requestID)
	}
	platform, err := launchSpecPlatform(specArg)
	if err != nil {
		return err
	}
	if platform == "local" {
		return fmt.Errorf("--request requires a Cloud spec")
	}
	if request.PlanStale && request.Action == "update" {
		record, err := stageRequestMergePackage(control, contextOverride, workspace)
		if err != nil {
			return err
		}
		merge, err := control.PrepareRequestMergePackage(*request, record.Ref)
		if err != nil {
			return err
		}
		workspace.Merge = *merge
		workspace.PackageRef = record.Ref
		if err := workspace.validateMergeFiles(); err != nil {
			return err
		}
		// Include newly deployed skills in the snapshot (they must be absent locally).
		before, err := workspace.snapshot()
		if err != nil {
			return err
		}
		if !sameLocalSnapshot(before, workspace.Original) {
			return fmt.Errorf("local files changed while preparing the plan; your files are unchanged")
		}
		merged := map[string]*cloud.MergeFile{}
		for _, file := range merge.Files {
			merged[file.Path] = file.Merged
		}
		if _, err := workspace.localFiles(merged); err != nil {
			return err
		}
		if err := workspace.save(); err != nil {
			return err
		}
		if err := workspace.materialize(merged, true); err != nil {
			return err
		}
		workspace.Materialized = true
		if err := workspace.save(); err != nil {
			return err
		}
		if len(merge.Conflicts) > 0 {
			return requestMergeConflictError(workspace, merge.Conflicts)
		}
		return saveMergedRequest(control, request, workspace, nil, message, writer, output, jsonOut)
	}
	record, err := stageRequestUpdatePackage(control, specArg, contextOverride)
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
	if err := recordRequestWorkspace(control, updated, specArg); err != nil {
		return fmt.Errorf("request %s was updated but local tracking could not be saved: %w", requestID, err)
	}
	return writeUpdatedRequestPlan(control, updated, writer, workspace.OrgID, output, jsonOut)
}

func sameRequestRevision(a, b *string) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
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

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/telos-org/telos/internal/cloud"
)

type revisionActionReceipt struct {
	SessionID          string                   `json:"session_id"`
	Context            string                   `json:"context"`
	Action             string                   `json:"action"`
	PreviousRevisionID string                   `json:"previous_revision_id"`
	SourceRevision     *cloud.Revision          `json:"source_revision"`
	Operation          *cloud.RevisionOperation `json:"operation"`
	ResultRevision     *cloud.Revision          `json:"result_revision,omitempty"`
	ObservationError   string                   `json:"observation_error,omitempty"`
}

func cmdRevisionAction(action string, args []string) {
	fs := newCommandFlagSet(action, "telos "+action+" SESSION --revision REVISION [flags]")
	selector := fs.String("revision", "", "Source revision number or full revision ID (required)")
	message := fs.String("message", "", "Revision message (at most 200 characters)")
	wait := fs.Bool("wait", false, "Wait for the operation and its exact result revision")
	timeout := fs.Duration("timeout", 10*time.Minute, "Maximum observation time with --wait")
	jsonOut := fs.Bool("json", false, "JSON receipt; with --wait, print the final result")
	contextValue := cloudContextFlag(fs)
	parseFlags(fs, args)
	requireArgCount(fs, 1, "one SESSION")
	if _, err := parseRevisionSelector(*selector); err != nil {
		exitWithError(err)
	}
	if *timeout <= 0 || (flagNameSet(fs, "timeout") && !*wait) {
		exitWithError(fmt.Errorf("--timeout must be positive and requires --wait"))
	}
	normalizedMessage, err := normalizeCLIRevisionMessage(*message)
	if err != nil {
		exitWithError(err)
	}
	control, session, capabilities, err := revisionCommandTarget(fs, *contextValue)
	if err != nil {
		exitWithError(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	page, err := control.ListSessionRevisions(ctx, session.ID, revisionPageSize, 0)
	if err != nil {
		exitWithError(err)
	}
	source, err := resolveRevision(ctx, control, session.ID, *selector, page)
	if err != nil {
		exitWithError(err)
	}
	if err := revisionActionAllowed(action, source, page.CurrentRevisionID, capabilities); err != nil {
		exitWithError(err)
	}
	operation, err := control.StartRevisionOperation(ctx, session.ID, source.ID, action, cloud.RevisionActionOptions{
		ExpectedCurrentRevisionID: page.CurrentRevisionID,
		RevisionMessage:           normalizedMessage,
	})
	if err != nil {
		exitWithError(revisionActionError(action, source.Sequence, err))
	}
	receipt := revisionActionReceipt{
		SessionID: session.ID, Context: control.ContextName(), Action: action,
		PreviousRevisionID: page.CurrentRevisionID, SourceRevision: source, Operation: operation,
	}
	if !*jsonOut {
		fmt.Printf("%s requested\n\n", strings.ToUpper(action[:1])+action[1:])
		printRevisionField(os.Stdout, "Goal", session.Name)
		printRevisionField(os.Stdout, "Session", session.ID)
		printRevisionField(os.Stdout, "Context", control.ContextName())
		printRevisionField(os.Stdout, "Previous revision", revisionLabel(page.CurrentRevisionID, page))
		printRevisionField(os.Stdout, "Source revision", fmt.Sprint(source.Sequence))
		printRevisionField(os.Stdout, "Operation", operation.ID)
		printRevisionField(os.Stdout, "Operation status", operation.Status)
	}
	if *wait {
		if !*jsonOut {
			fmt.Fprintf(os.Stderr, "\nWaiting for %s…\n", action)
		}
		waitCtx, cancel := context.WithTimeout(ctx, *timeout)
		defer cancel()
		receipt.Operation, receipt.ResultRevision, err = waitRevisionOperation(waitCtx, control, session.ID, action, page.CurrentRevisionID, source, operation, time.Second)
		if err != nil && receipt.Operation.Status != "failed" {
			receipt.ObservationError = err.Error()
			err = fmt.Errorf("stopped waiting for operation %s (last observed status: %s): %w; the Cloud operation was not cancelled", operation.ID, receipt.Operation.Status, err)
		}
	} else if operation.Status == "failed" {
		err = revisionOperationFailure(operation)
	}
	if *jsonOut {
		printJSON(receipt)
	} else if receipt.ResultRevision != nil {
		fmt.Fprintln(os.Stdout, "\nOperation succeeded.")
		printRevisionField(os.Stdout, "Created revision", fmt.Sprint(receipt.ResultRevision.Sequence))
		printRevisionField(os.Stdout, "Revision ID", receipt.ResultRevision.ID)
		label := "Restored from"
		if action == "redeploy" {
			label = "Redeployed from"
		}
		printRevisionField(os.Stdout, label, fmt.Sprint(source.Sequence))
	} else if err == nil {
		fmt.Fprintf(os.Stdout, "\nCheck resulting history: telos history %s --context %s\n", session.ID, control.ContextName())
	}
	if err != nil {
		exitWithError(err)
	}
}

func revisionActionError(action string, sequence int, err error) error {
	var apiErr *cloud.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode < 500 {
		switch apiErr.Code {
		case "change_requests_client_required":
			return fmt.Errorf("this deployment requires a change request; use the Telos web UI to %s this revision: %w", action, err)
		case "stale_current_revision", "current_revision", "no_snapshot", "snapshot_pending", "snapshot_lost", "snapshot_incompatible", "operation_in_progress", "deployment_not_ready", "runtime_unsupported", "package_unavailable", "runtime_unavailable":
			return fmt.Errorf("%s revision %d: %w", action, sequence, err)
		}
	}
	// Runtime dispatch can fail after Cloud commits the new revision, including
	// with an HTTP 409. An unrecognized HTTP error does not prove rejection.
	return fmt.Errorf("could not confirm whether %s revision %d was accepted: %w; check history before retrying", action, sequence, err)
}

func normalizeCLIRevisionMessage(message string) (string, error) {
	for _, r := range message {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return "", fmt.Errorf("revision messages must be a single line without control characters")
		}
	}
	message = strings.TrimSpace(message)
	if utf8.RuneCountInString(message) > 200 {
		return "", fmt.Errorf("revision messages must be at most 200 characters")
	}
	return message, nil
}

func revisionActionAllowed(action string, source *cloud.Revision, currentID string, capabilities *cloud.Capabilities) error {
	capability := revisionActionCapability(action, source, currentID, capabilities)
	if !capability.Allowed {
		reason := revisionReason(revisionString(capability.Reason))
		if action == "restore" && revisionActionCapability("redeploy", source, currentID, capabilities).Allowed {
			reason += "; package redeploy is available with telos redeploy SESSION --revision " + fmt.Sprint(source.Sequence)
		}
		return fmt.Errorf("cannot %s revision %d: %s", action, source.Sequence, reason)
	}
	return nil
}

func revisionActionCapability(action string, source *cloud.Revision, currentID string, capabilities *cloud.Capabilities) cloud.RevisionCapability {
	enabled, capability := capabilities.DeploymentSnapshotRestore, source.RestoreCapability
	if action == "redeploy" {
		enabled, capability = capabilities.DeploymentPackageRedeploy, source.RedeployCapability
	}
	reason := ""
	switch {
	case source.ID == currentID:
		reason = "current_revision"
	case !enabled:
		reason = "unsupported"
	// Match the web's snapshot capture gate, even if package redeploy is allowed.
	case source.Snapshot.Status == "capturing" || revisionString(source.RestoreCapability.Reason) == "snapshot_pending":
		reason = "snapshot_pending"
	}
	if reason != "" {
		return cloud.RevisionCapability{Reason: &reason}
	}
	return capability
}

func waitRevisionOperation(ctx context.Context, control *cloud.Client, sessionID, action, previousID string, source *cloud.Revision, operation *cloud.RevisionOperation, interval time.Duration) (*cloud.RevisionOperation, *cloud.Revision, error) {
	for {
		if operation.Status == "failed" {
			return operation, nil, revisionOperationFailure(operation)
		}
		if operation.Status == "succeeded" && revisionString(operation.ResultRevisionID) != "" {
			revision, err := control.GetSessionRevision(ctx, sessionID, *operation.ResultRevisionID)
			if err == nil {
				if revision.Kind != action || revisionString(revision.RestoredFromRevisionID) != source.ID || revisionString(revision.ParentRevisionID) != previousID || revision.PackageDigest != source.PackageDigest {
					return operation, nil, fmt.Errorf("operation result revision does not match the requested %s", action)
				}
				return operation, revision, nil
			}
			if !cloud.IsStatus(err, 404) {
				return operation, nil, err
			}
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return operation, nil, ctx.Err()
		case <-timer.C:
		}
		next, err := control.GetRevisionOperation(ctx, sessionID, operation.ID)
		if err != nil {
			return operation, nil, err
		}
		if next.Kind != "" && next.Kind != action {
			return operation, nil, fmt.Errorf("Cloud returned a different operation kind")
		}
		operation = next
	}
}

func revisionOperationFailure(operation *cloud.RevisionOperation) error {
	if operation.Error != nil {
		return fmt.Errorf("operation %s failed: %s (%s)", operation.ID, revisionText(operation.Error.Message), revisionText(operation.Error.Code))
	}
	return fmt.Errorf("operation %s failed", operation.ID)
}

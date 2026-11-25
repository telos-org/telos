package main

import (
	"cmp"
	"errors"
	"fmt"
	"os"

	"github.com/telos-org/telos/internal/cloud"
)

// applyProposal deploys a proposal to its Goal and prints the Goal. A restore
// keeps applying after Cloud answers, so it reads as in progress.
func applyProposal(id string, force, jsonOut bool, contextOverride string) {
	control := cloudClient(contextOverride)
	applied, err := control.ApplyProposal(id, force)
	if err != nil {
		hint := ""
		if apiCode(err) == "proposal_stale" {
			if proposal, getErr := control.GetProposal(id); getErr == nil {
				hint = staleApplyHint(proposal.Goal)
			}
		}
		exitWithError(ruleError(err, hint))
	}
	if err := unappliedError(applied); err != nil {
		exitWithError(err)
	}
	goal, err := control.GetSession(applied.Goal)
	if err != nil {
		exitWithError(err)
	}
	if jsonOut {
		printJSON(map[string]any{"context": control.ContextName(), "proposal": applied, "goal": goal})
		return
	}
	printCloudSessionReceiptForContext(
		os.Stdout,
		fmt.Sprintf("%s %q to %s", applied.Status, applied.Title, goal.Name),
		goal,
		control.ContextName(),
		followUpContext(control, contextOverride),
		"",
	)
}

// unappliedError says why Cloud didn't apply a proposal it returned, or is
// nil when it applied it or is still applying it.
func unappliedError(proposal *cloud.Proposal) error {
	switch {
	case proposal.Status == "applied" || proposal.Status == "applying":
		return nil
	case proposal.ErrorCode == "snapshot_pending":
		return &snapshotPendingUpdateError{}
	case proposal.Status == "stale":
		return fmt.Errorf("%q is stale because %s changed since it was opened\n%s",
			proposal.Title, proposal.GoalName, staleApplyHint(proposal.Goal))
	}
	return errors.New(cmp.Or(proposal.Error, fmt.Sprintf("%q is %s", proposal.Title, proposal.Status)))
}

// staleApplyHint says how to start over from a stale proposal's Goal.
func staleApplyHint(goalID string) string {
	return fmt.Sprintf("To start over: telos pull %s, then %s", goalID, proposeHint("SPEC.md"))
}

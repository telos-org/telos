package main

import (
	"fmt"
	"io"
	"os"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/sessionapi"
)

// -- delete -------------------------------------------------------------------

func cmdDelete(args []string) {
	fs := newCommandFlagSet("delete", "telos delete GOAL|PROPOSAL [flags]")
	jsonOut := fs.Bool("json", false, "JSON output")
	contextValue := cloudContextFlag(fs)
	parseFlags(fs, args)
	contextOverride, err := cloudContextOverride(fs, *contextValue)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}

	requireArgCount(fs, 1, "one GOAL or PROPOSAL")
	sessionID := fs.Arg(0)
	requireIDKind("delete", sessionID, "a GOAL or PROPOSAL", "goal", "proposal")
	if isProposalID(sessionID) {
		closeProposal(sessionID, *jsonOut, contextOverride)
		return
	}
	if err := validateCloudSessionContext(sessionID, contextOverride); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}

	if isCloudApplyID(sessionID) || contextOverride != "" {
		cloudSession, contextName, err := deleteCloudSessionForContext(sessionID, contextOverride)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if *jsonOut {
			printCloudSessionDeleteJSON(cloudSession, contextName)
			return
		}
		printCloudSessionDeleteReceiptForContext(os.Stdout, *cloudSession, contextName)
		return
	}

	session, err := stopSessionAnywhere(sessionID)
	if err == nil {
		if *jsonOut {
			printJSON(goalJSON(session))
			return
		}
		printLocalSessionDeleteReceipt(os.Stdout, *session)
		return
	}

	cloudSession, contextName, found, cloudErr := deleteCloudSessionIfConfigured(sessionID, "")
	if cloudErr != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", cloudErr)
		os.Exit(1)
	}
	if found {
		if *jsonOut {
			printCloudSessionDeleteJSON(cloudSession, contextName)
			return
		}
		printCloudSessionDeleteReceiptForContext(os.Stdout, *cloudSession, contextName)
		return
	}

	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}

func printCloudSessionDeleteJSON(session *cloud.SessionRecord, contextName string) {
	printJSON(struct {
		*cloud.SessionRecord
		Context string `json:"context"`
	}{
		SessionRecord: session,
		Context:       contextName,
	})
}

func deleteCloudSessionForContext(
	sessionID string,
	contextOverride string,
) (*cloud.SessionRecord, string, error) {
	control, err := cloud.ControlClientForContext(contextOverride)
	if err != nil {
		return nil, "", err
	}
	session, err := control.DeleteSession(sessionID)
	if err != nil {
		return nil, "", goalNotFound(err, sessionID, control.ContextName())
	}
	return session, control.ContextName(), nil
}

func deleteCloudSessionIfConfigured(
	sessionID string,
	contextOverride string,
) (*cloud.SessionRecord, string, bool, error) {
	if _, contextName, found, err := getCloudSessionIfConfigured(sessionID, contextOverride); err != nil || !found {
		return nil, contextName, found, err
	}
	cloudSession, contextName, err := deleteCloudSessionForContext(sessionID, contextOverride)
	if err != nil {
		return nil, contextName, true, err
	}
	return cloudSession, contextName, true, nil
}

func printCloudSessionDeleteReceipt(out io.Writer, session cloud.SessionRecord) {
	printCloudSessionDeleteReceiptForContext(out, session, "")
}

func printCloudSessionDeleteReceiptForContext(
	out io.Writer,
	session cloud.SessionRecord,
	contextName string,
) {
	switch session.State {
	case "deleted":
		fmt.Fprintf(out, "deleted %s\n\n", session.Name)
	default:
		fmt.Fprintf(out, "delete requested for %s\n\n", session.Name)
	}
	printSummaryField(out, "Status", cloudSessionDisplayStatus(session))
	printSummaryField(out, "Goal", session.ID)
	if contextName != "" {
		printSummaryField(out, "Context", contextName)
	}
}

// closeProposal closes a proposal without deploying it.
func closeProposal(id string, jsonOut bool, contextOverride string) {
	proposal, err := cloudClient(contextOverride).CloseProposal(id)
	if err != nil {
		exitWithError(ruleError(err, ""))
	}
	if jsonOut {
		printJSON(proposal)
		return
	}
	fmt.Printf("closed %q\n", proposal.Title)
}

func printLocalSessionDeleteReceipt(out io.Writer, session sessionapi.Session) {
	fmt.Fprintf(out, "deleted %s (history preserved)\n\n", deletedSessionName(session))
	row := displayRow(session)
	printSummaryField(out, "Status", row.Status)
	printSummaryField(out, "Goal", row.Session)
	if session.TotalCostUSD != nil {
		printSummaryField(out, "Cost", formatDetailCost(session.TotalCostUSD))
	}
}

func deletedSessionName(session sessionapi.Session) string {
	if name := sessionName(session); name != "-" {
		return name
	}
	if session.SessionID != "" {
		return session.SessionID
	}
	return "-"
}

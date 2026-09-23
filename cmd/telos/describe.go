package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/sessionapi"
)

// -- describe -----------------------------------------------------------------

func cmdDescribe(args []string) {
	fs := newCommandFlagSet("describe", "telos describe SESSION [flags]")
	jsonOut := fs.Bool("json", false, "JSON output")
	contextValue := cloudContextFlag(fs)
	parseFlags(fs, args)
	contextOverride, err := cloudContextOverride(fs, *contextValue)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}

	requireArgCount(fs, 1, "one SESSION")
	sessionID := fs.Arg(0)
	if err := validateCloudSessionContext(sessionID, contextOverride); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	if contextOverride != "" {
		cloudSession, contextName, err := getCloudSessionForContext(sessionID, contextOverride)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		describeCloudSession(cloudSession, contextName, *jsonOut)
		return
	}

	session, err := getSessionFromAnywhere(sessionID)
	if err == nil {
		if *jsonOut {
			printJSON(session)
			return
		}

		printSessionDescription(os.Stdout, *session)
		return
	}

	cloudSession, contextName, found, cloudErr := getCloudSessionIfConfigured(sessionID, "")
	if cloudErr != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", cloudErr)
		os.Exit(1)
	}
	if found {
		describeCloudSession(cloudSession, contextName, *jsonOut)
		return
	}

	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}

func describeCloudSession(
	session *cloud.SessionRecord,
	contextName string,
	jsonOut bool,
) {
	control, err := cloud.ControlClientForContext(contextName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	requests, err := pendingCloudChangeRequests(control, session.ID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: read change requests: %v\n", err)
		os.Exit(1)
	}
	if jsonOut {
		printCloudSessionJSON(session, contextName, requests...)
		return
	}
	printCloudSessionDescriptionForContext(os.Stdout, *session, contextName)
	printPendingCloudChangeRequests(os.Stdout, control, requests)
}

func printCloudSessionJSON(session *cloud.SessionRecord, contextName string, requests ...cloud.ChangeRequestRecord) {
	printJSON(struct {
		*cloud.SessionRecord
		Context               string                      `json:"context,omitempty"`
		PendingChangeRequests []cloud.ChangeRequestRecord `json:"pending_change_requests,omitempty"`
	}{
		SessionRecord:         session,
		Context:               contextName,
		PendingChangeRequests: requests,
	})
}

func getCloudSession(sessionID, contextOverride string) (*cloud.SessionRecord, error) {
	session, _, err := getCloudSessionForContext(sessionID, contextOverride)
	return session, err
}

func getCloudSessionForContext(
	sessionID string,
	contextOverride string,
) (*cloud.SessionRecord, string, error) {
	control, err := cloud.ControlClientForContext(contextOverride)
	if err != nil {
		return nil, "", err
	}
	session, err := control.GetSession(sessionID)
	if err != nil {
		return nil, "", err
	}
	return session, control.ContextName(), nil
}

func printCloudSessionDescription(out io.Writer, session cloud.SessionRecord) {
	printCloudSessionDescriptionForContext(out, session, "")
}

func printCloudSessionDescriptionForContext(
	out io.Writer,
	session cloud.SessionRecord,
	contextName string,
) {
	printSummaryField(out, "Name", session.Name)
	printSummaryField(out, "Status", cloudSessionDisplayStatus(session))
	printSummaryField(out, "Session", session.ID)
	if session.State == "pending" && session.CurrentRevisionID == "" {
		printSummaryField(out, "Revision", "not deployed yet")
	} else {
		printSummaryField(out, "Revision", session.PackageDigest)
	}
	if contextName != "" {
		printSummaryField(out, "Context", contextName)
	}
	if session.ServiceURL != nil && strings.TrimSpace(*session.ServiceURL) != "" {
		printSummaryField(out, "Service", strings.TrimSpace(*session.ServiceURL))
	}
	if reason := cloudSessionReason(session); reason != "" {
		printSummaryField(out, "Reason", reason)
	}
}

func cloudSessionDisplayStatus(session cloud.SessionRecord) string {
	if session.Status != "" {
		return session.Status
	}
	return session.State
}

func cloudSessionReason(session cloud.SessionRecord) string {
	if session.FailureReason != nil && strings.TrimSpace(*session.FailureReason) != "" {
		return strings.TrimSpace(*session.FailureReason)
	}
	if session.State == "pending" && session.CurrentRevisionID == "" {
		return strings.TrimSpace(session.StatusReason)
	}
	switch strings.ToLower(strings.TrimSpace(cloudSessionDisplayStatus(session))) {
	case "needs_attention", "needs attention", "failed", "stopped":
		return strings.TrimSpace(session.StatusReason)
	default:
		return ""
	}
}

func printSessionDescription(out io.Writer, session sessionapi.Session) {
	row := displayRow(session)
	printSummaryField(out, "Name", row.Name)
	printSummaryField(out, "Target", row.Target)
	printSummaryField(out, "Status", row.Status)
	printSummaryField(out, "Session", row.Session)
	if session.TotalCostUSD != nil {
		printSummaryField(out, "Cost", formatDetailCost(session.TotalCostUSD))
	}
	if session.CurrentSpecVersion != nil {
		printSummaryField(out, "Revision", fmt.Sprint(*session.CurrentSpecVersion))
	}
	if session.ParentSessionID != nil && *session.ParentSessionID != "" {
		printSummaryField(out, "Parent", *session.ParentSessionID)
	}
	if serviceURL := sessionServiceURL(session); serviceURL != "" {
		printSummaryField(out, "Service", serviceURL)
	}
	if session.Error != nil && strings.TrimSpace(*session.Error) != "" {
		printSummaryField(out, "Reason", strings.TrimSpace(*session.Error))
	}
}

func printSummaryField(out io.Writer, label string, value string) {
	fmt.Fprintf(out, "%-9s %s\n", label, orDash(value))
}

func printDetailField(out io.Writer, label string, value string) {
	fmt.Fprintf(out, "  %-14s %s\n", label, orDash(value))
}

func orDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

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
		describeCloudGoal(cloudSession, contextName, contextOverride, *jsonOut)
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
		describeCloudGoal(cloudSession, contextName, "", *jsonOut)
		return
	}

	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}

func describeCloudGoal(
	session *cloud.SessionRecord,
	contextName string,
	contextOverride string,
	jsonOut bool,
) {
	var control *cloud.Client
	if hasCost(*session) && billedByProvider(*session) {
		control, _ = cloud.ControlClientForContext(contextOverride)
	}
	cost := goalCosts(control, []cloud.SessionRecord{*session})[0]
	if jsonOut {
		printCloudSessionJSON(session, contextName, cost)
		return
	}
	printCloudSessionDescriptionForContext(os.Stdout, *session, contextName, cost)
}

// cloudSessionJSON is a Cloud session as describe and list print it. Callers
// read status and status_reason; the raw lifecycle state is left out.
type cloudSessionJSON struct {
	*cloud.SessionRecord
	// State and Billing stay nil so they hide the record's raw state and
	// billing, which cost presents.
	State   *string   `json:"state,omitempty"`
	Billing *struct{} `json:"billing,omitempty"`
	Status  string    `json:"status,omitempty"`
	Cost    *goalCost `json:"cost,omitempty"`
}

func newCloudSessionJSON(session *cloud.SessionRecord) cloudSessionJSON {
	return cloudSessionJSON{SessionRecord: session, Status: cloudSessionDisplayStatus(*session)}
}

func printCloudSessionJSON(
	session *cloud.SessionRecord,
	contextName string,
	cost *goalCost,
) {
	record := newCloudSessionJSON(session)
	record.Cost = cost
	printJSON(struct {
		cloudSessionJSON
		Context string `json:"context,omitempty"`
	}{
		cloudSessionJSON: record,
		Context:          contextName,
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
	printCloudSessionDescriptionForContext(out, session, "", nil)
}

func printCloudSessionDescriptionForContext(
	out io.Writer,
	session cloud.SessionRecord,
	contextName string,
	cost *goalCost,
) {
	printSummaryField(out, "Name", session.Name)
	printSummaryField(out, "Status", cloudSessionDisplayStatus(session))
	printSummaryField(out, "Session", session.ID)
	printSummaryField(out, "Revision", shortRevision(session.PackageDigest))
	printCloudInferenceSummary(out, session)
	if contextName != "" {
		printSummaryField(out, "Context", contextName)
	}
	if session.ServiceURL != nil && strings.TrimSpace(*session.ServiceURL) != "" {
		printSummaryField(out, "Service", strings.TrimSpace(*session.ServiceURL))
	}
	printGoalCost(out, cost)
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

// shortRevision abbreviates a sha256 package digest to its first 12 hex
// digits for human output. --json keeps the full digest.
func shortRevision(digest string) string {
	hex, ok := strings.CutPrefix(digest, "sha256:")
	if !ok || len(hex) <= 12 {
		return digest
	}
	return "sha256:" + hex[:12]
}

func cloudSessionReason(session cloud.SessionRecord) string {
	if session.FailureReason != nil && strings.TrimSpace(*session.FailureReason) != "" {
		return strings.TrimSpace(*session.FailureReason)
	}
	switch strings.ToLower(strings.TrimSpace(cloudSessionDisplayStatus(session))) {
	case "needs_attention", "needs attention", "failed", "stopped":
		return strings.TrimSpace(session.StatusReason)
	default:
		return ""
	}
}

// cloudSessionModel names a Goal's model the way --model selects it:
// telos/<tier> for managed inference, <connection-name>/<model-id> for a
// saved API key or subscription. Viewers who cannot see the connection name
// get the model ID and its source instead.
func cloudSessionModel(session cloud.SessionRecord) string {
	model := session.AgentModel
	summary := session.Inference
	if summary == nil {
		return model
	}
	if summary.Model != "" {
		model = summary.Model
	}
	if summary.Source == "managed" {
		if model == "" && summary.Tier != "" {
			model = "telos/" + summary.Tier
		}
		if model == "telos-bifrost/telos/default" || model == "telos-bifrost/telos/max" {
			model = strings.TrimPrefix(model, "telos-bifrost/")
		}
		return model
	}
	if model == "" {
		return ""
	}
	if summary.ConnectionName != "" {
		return summary.ConnectionName + "/" + model
	}
	return model + " (" + inferenceSourceLabel(summary.Source) + ")"
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

func printCloudInferenceSummary(out io.Writer, session cloud.SessionRecord) {
	if model := cloudSessionModel(session); model != "" {
		printSummaryField(out, "Model", model)
	}
	if session.AgentThinking != "" {
		printSummaryField(out, "Thinking", session.AgentThinking)
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

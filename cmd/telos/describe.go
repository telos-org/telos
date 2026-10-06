package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/config"
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
	var localErr error
	if contextOverride == "" {
		session, err := getSessionFromAnywhere(sessionID)
		if err == nil {
			if *jsonOut {
				printJSON(session)
			} else {
				printSessionDescription(os.Stdout, *session)
			}
			return
		}
		localErr = err
		configured, err := config.IsConfigured()
		if err != nil || !configured {
			if err == nil {
				err = localErr
			}
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	}
	control, err := cloud.ControlClientForContext(contextOverride)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	session, err := control.GetSession(sessionID)
	if err != nil {
		if localErr != nil && cloud.IsStatus(err, 404) {
			err = localErr
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	sessions := []cloud.SessionRecord{*session}
	control.PopulateSessionCosts(sessions)
	if *jsonOut {
		printCloudSessionJSON(&sessions[0], control.ContextName())
	} else {
		printCloudSessionDescriptionForContext(os.Stdout, sessions[0], control.ContextName())
	}
}

func printCloudSessionJSON(
	session *cloud.SessionRecord,
	contextName string,
) {
	printJSON(struct {
		*cloud.SessionRecord
		Context string `json:"context,omitempty"`
	}{
		SessionRecord: session,
		Context:       contextName,
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
	fields := []descriptionField{
		{label: "Name", value: orDash(session.Name)},
		{label: "Status", value: orDash(cloudSessionDisplayStatus(session))},
		{label: "Session", value: orDash(session.ID)},
		{label: "Revision", value: orDash(session.PackageDigest)},
	}
	fields = append(fields, cloudInferenceFields(session)...)
	if contextName != "" {
		fields = append(fields, descriptionField{label: "Context", value: contextName})
	}
	if session.ServiceURL != nil && strings.TrimSpace(*session.ServiceURL) != "" {
		fields = append(fields, descriptionField{label: "Service", value: strings.TrimSpace(*session.ServiceURL)})
	}
	if reason := cloudSessionReason(session); reason != "" {
		fields = append(fields, descriptionField{label: "Reason", value: reason})
	}
	fields = append(fields, cloudCostFields(session)...)
	printDescriptionFields(out, fields)
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

func printCloudInferenceSummary(out io.Writer, session cloud.SessionRecord) {
	for _, field := range cloudInferenceFields(session) {
		printSummaryField(out, field.label, field.value)
	}
}

func cloudInferenceFields(session cloud.SessionRecord) []descriptionField {
	var fields []descriptionField
	model := session.AgentModel
	if summary := session.Inference; summary != nil {
		fields = append(fields, descriptionField{label: "Inference", value: inferenceSourceLabel(summary.Source)})
		if summary.ConnectionName != "" {
			fields = append(fields, descriptionField{label: "Connection", value: summary.ConnectionName})
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
		}
	}
	if model != "" {
		fields = append(fields, descriptionField{label: "Model", value: model})
	}
	if session.AgentThinking != "" {
		fields = append(fields, descriptionField{label: "Thinking", value: session.AgentThinking + " (requested)"})
	}
	return fields
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

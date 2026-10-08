package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
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
	if contextOverride != "" {
		description, err := describeCloudSession(sessionID, contextOverride)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		printCloudDescription(description, *jsonOut)
		return
	}

	session, err := getSessionFromAnywhere(sessionID)
	if err == nil {
		var settings *inferenceDescription
		var settingsError string
		if isLocalApplyID(sessionID) {
			state, readErr := store().Inference(sessionID)
			if readErr != nil {
				settingsError = readErr.Error()
			} else {
				settings = describeInference(localInferenceReceipt(sessionID, state))
			}
		}
		if *jsonOut {
			printJSON(struct {
				*sessionapi.Session
				InferenceState *inferenceDescription `json:"inference_state,omitempty"`
				InferenceError string                `json:"inference_error,omitempty"`
			}{session, settings, settingsError})
			return
		}

		printSessionDescription(os.Stdout, *session)
		if settings != nil {
			printInferenceDescription(os.Stdout, settings)
		}
		if settingsError != "" {
			printSummaryField(os.Stdout, "Settings", "unavailable: "+settingsError)
		}
		return
	}

	configured, configErr := config.IsConfigured()
	if configErr != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", configErr)
		os.Exit(1)
	}
	if configured {
		description, cloudErr := describeCloudSession(sessionID, "")
		if cloudErr == nil {
			printCloudDescription(description, *jsonOut)
			return
		}
		var apiErr *cloud.APIError
		if !errors.As(cloudErr, &apiErr) || apiErr.StatusCode != http.StatusNotFound {
			fmt.Fprintf(os.Stderr, "error: %v\n", cloudErr)
			os.Exit(1)
		}
	}

	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}

type inferenceDescription struct {
	Settings sessionapi.InferenceSettings `json:"settings"`
	// Queued targets are displayed only in text output.
	queuedModel    string
	queuedThinking string
	displayModel   string
}

func describeInference(receipt *inferenceReceipt) *inferenceDescription {
	description := &inferenceDescription{Settings: receipt.Settings, displayModel: receipt.displayModel}
	if receipt.Status == "pending" || receipt.Status == "applying" {
		description.queuedModel = receipt.RequestedModel
		description.queuedThinking = receipt.RequestedThinking
	}
	return description
}

func printInferenceDescription(out io.Writer, description *inferenceDescription) {
	model := strings.TrimPrefix(description.Settings.Model, "telos-bifrost/")
	if description.displayModel != "" {
		model = description.displayModel
	}
	queuedModel := strings.TrimPrefix(description.queuedModel, "telos-bifrost/")
	printSummaryField(out, "Model", inferenceSettingChange(model, queuedModel, "pending"))
	printSummaryField(out, "Thinking", inferenceSettingChange(description.Settings.Thinking, description.queuedThinking, "pending"))
}

type cloudDescription struct {
	*cloud.SessionRecord
	Context        string                `json:"context,omitempty"`
	InferenceState *inferenceDescription `json:"inference_state,omitempty"`
	InferenceError string                `json:"inference_error,omitempty"`
}

// Only describe fetches live settings; ordinary session reads keep their contract.
func describeCloudSession(sessionID, contextOverride string) (*cloudDescription, error) {
	control, err := cloud.ControlClientForContext(contextOverride)
	if err != nil {
		return nil, err
	}
	session, err := control.GetSession(sessionID)
	if err != nil {
		return nil, err
	}
	description := &cloudDescription{SessionRecord: session, Context: control.ContextName()}
	state, err := control.GetDeploymentInference(sessionID)
	if err == nil {
		var receipt *inferenceReceipt
		receipt, err = cloudInferenceReceipt(sessionID, description.Context, state)
		if err == nil {
			description.InferenceState = describeInference(receipt)
			// This read may be newer than GetSession; keep displayed values consistent.
			session.AgentModel, session.AgentThinking = state.AgentModel, state.AgentThinking
			session.Inference = &state.Inference
		}
	}
	if err != nil {
		var apiErr *cloud.APIError
		if !errors.As(err, &apiErr) || (apiErr.StatusCode != http.StatusNotFound && apiErr.StatusCode != http.StatusMethodNotAllowed && apiErr.StatusCode != http.StatusNotImplemented) {
			description.InferenceError = err.Error()
		}
	}
	return description, nil
}

func printCloudDescription(description *cloudDescription, jsonOut bool) {
	if jsonOut {
		printJSON(description)
		return
	}
	printCloudSessionDetails(os.Stdout, *description.SessionRecord, description.Context, description.InferenceState)
	if description.InferenceError != "" {
		printSummaryField(os.Stdout, "Settings", "unavailable: "+description.InferenceError)
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
	printCloudSessionDetails(out, session, contextName, nil)
}

func printCloudSessionDetails(out io.Writer, session cloud.SessionRecord, contextName string, settings *inferenceDescription) {
	printSummaryField(out, "Name", session.Name)
	printSummaryField(out, "Status", cloudSessionDisplayStatus(session))
	printSummaryField(out, "Session", session.ID)
	printSummaryField(out, "Revision", session.PackageDigest)
	if settings == nil {
		printCloudInferenceSummary(out, session)
	} else {
		if summary := session.Inference; summary != nil && summary.ConnectionName != "" {
			printSummaryField(out, "Connection", summary.ConnectionName)
		}
		printInferenceDescription(out, settings)
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
		printSummaryField(out, "Thinking", session.AgentThinking+" (requested)")
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

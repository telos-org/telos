package main

import (
	"crypto/rand"
	"flag"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/sessionapi"
)

var thinkingSyntax = regexp.MustCompile(`^[a-z]{1,32}$`)

// A receipt distinguishes requested values from the last confirmed settings.
type inferenceReceipt struct {
	SessionID         string                       `json:"session_id"`
	Context           string                       `json:"context,omitempty"`
	Settings          sessionapi.InferenceSettings `json:"settings"`
	Revision          int                          `json:"revision"`
	RequestID         string                       `json:"request_id,omitempty"`
	Status            string                       `json:"status,omitempty"`
	RequestedModel    string                       `json:"requested_model,omitempty"`
	RequestedThinking string                       `json:"requested_thinking,omitempty"`
	Error             string                       `json:"error,omitempty"`
}

func validateApplyInferenceFlags(fs *flag.FlagSet, sessionID, model, thinking string) error {
	settingsSet := flagNamesSet(fs, "model", "thinking")
	if fs.NArg() > 0 {
		if sessionID != "" && settingsSet {
			return fmt.Errorf("apply spec and model/thinking changes separately: first apply SPEC.md --session %s, then apply --session %s --model MODEL and/or --thinking LEVEL", sessionID, sessionID)
		}
		return nil
	}
	if sessionID == "" || !settingsSet {
		return fmt.Errorf("provide a SPEC.md, or --session SESSION with --model and/or --thinking")
	}
	if !isCloudApplyID(sessionID) && !isLocalApplyID(sessionID) {
		return fmt.Errorf("invalid session id %q", sessionID)
	}
	if flagNamesSet(fs, "workspace", "force", "max-cost-usd") {
		return fmt.Errorf("--workspace, --force, and --max-cost-usd cannot be used for a settings-only apply")
	}
	if flagNameSet(fs, "model") && strings.TrimSpace(model) == "" {
		return fmt.Errorf("--model requires a non-empty model")
	}
	if flagNameSet(fs, "thinking") && !thinkingSyntax.MatchString(strings.TrimSpace(thinking)) {
		return fmt.Errorf("--thinking requires a model-supported level using 1 to 32 lowercase letters")
	}
	return nil
}

func applySessionInference(sessionID, model, thinking, contextOverride string) (*inferenceReceipt, error) {
	if err := validateCloudSessionContext(sessionID, contextOverride); err != nil {
		return nil, err
	}
	requestID := "cli_" + rand.Text()
	if isLocalApplyID(sessionID) {
		return applyLocalInference(sessionID, model, thinking, requestID)
	}
	control, err := cloud.ControlClientForContext(contextOverride)
	if err != nil {
		return nil, err
	}
	selection, err := resolveCloudInference(control, model)
	if err != nil {
		return nil, err
	}
	current, err := control.GetDeploymentInference(sessionID)
	if err != nil {
		return nil, fmt.Errorf("cannot read deployment settings: %w", err)
	}
	if _, err := cloudInferenceReceipt(sessionID, control.ContextName(), current); err != nil {
		return nil, err
	}
	request := cloud.DeploymentInferenceRequest{
		ApplyAt:   "next_turn",
		RequestID: requestID, ExpectedRevision: current.Revision, Inference: selection,
	}
	if thinking != "" {
		request.AgentThinking = &thinking
	}
	state, err := control.UpdateDeploymentInference(sessionID, request)
	if err != nil {
		return nil, inferenceSubmissionError(sessionID, control.ContextName(), requestID, err)
	}
	receipt, err := cloudInferenceReceipt(sessionID, control.ContextName(), state)
	if err == nil {
		err = validateInferenceReply(receipt, requestID, current.Revision+1)
	}
	if err != nil {
		return nil, inferenceSubmissionError(sessionID, control.ContextName(), requestID, err)
	}
	return receipt, inferenceOutcomeError(receipt)
}

func applyLocalInference(sessionID, model, thinking, requestID string) (*inferenceReceipt, error) {
	s := store()
	current, err := s.Inference(sessionID)
	if err != nil {
		return nil, err
	}
	request := sessionapi.InferenceUpdateRequest{ApplyAt: "next_turn", RequestID: requestID, ExpectedRevision: current.Revision}
	if model != "" {
		request.Model = &model
	}
	if thinking != "" {
		request.Thinking = &thinking
	}
	// Repeating the same pending request reuses its identity.
	if pending := current.Update; pending != nil && pending.Status == "pending" &&
		matchesRequestedSetting(pending.Model, model) && matchesRequestedSetting(pending.Thinking, thinking) {
		request = pending.InferenceUpdateRequest
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	state, err := s.UpdateInference(sessionID, request)
	if err != nil {
		return nil, inferenceSubmissionError(sessionID, "", request.RequestID, err)
	}
	receipt := localInferenceReceipt(sessionID, state)
	if err := validateInferenceReply(receipt, request.RequestID, request.ExpectedRevision+1); err != nil {
		return nil, inferenceSubmissionError(sessionID, "", request.RequestID, err)
	}
	return receipt, inferenceOutcomeError(receipt)
}

func matchesRequestedSetting(saved *string, value string) bool {
	return (saved == nil && value == "") || (saved != nil && *saved == value)
}

func cloudInferenceReceipt(sessionID, contextName string, state *cloud.DeploymentInferenceState) (*inferenceReceipt, error) {
	if state == nil || state.AgentModel == "" || state.AgentThinking == "" || state.Revision < 0 {
		return nil, fmt.Errorf("Cloud returned invalid deployment settings")
	}
	receipt := &inferenceReceipt{
		SessionID: sessionID, Context: contextName,
		Settings: sessionapi.InferenceSettings{Model: state.AgentModel, Thinking: state.AgentThinking},
		Revision: state.Revision, Status: state.Status, Error: state.Error,
	}
	if state.Request != nil {
		receipt.RequestID = state.Request.RequestID
		if selection := state.Request.Inference; selection != nil {
			if selection.Source == "managed" {
				receipt.RequestedModel = "telos/" + selection.Tier
			} else {
				receipt.RequestedModel = selection.Model
				if state.Inference.ConnectionName != "" {
					receipt.RequestedModel = state.Inference.ConnectionName + "/" + selection.Model
				}
			}
		}
		if state.Request.AgentThinking != nil {
			receipt.RequestedThinking = *state.Request.AgentThinking
		}
	}
	if (state.Status == "") != (state.Request == nil) || (state.Status != "" && (!validInferenceStatus(state.Status) || receipt.RequestID == "")) {
		return nil, fmt.Errorf("Cloud returned invalid settings change status")
	}
	return receipt, nil
}

func localInferenceReceipt(sessionID string, state *sessionapi.InferenceResponse) *inferenceReceipt {
	receipt := &inferenceReceipt{SessionID: sessionID, Settings: state.Settings, Revision: state.Revision}
	if update := state.Update; update != nil {
		receipt.RequestID, receipt.Status, receipt.Error = update.RequestID, update.Status, update.Error
		if update.Model != nil {
			receipt.RequestedModel = *update.Model
		}
		if update.Thinking != nil {
			receipt.RequestedThinking = *update.Thinking
		}
	}
	return receipt
}

func validInferenceStatus(status string) bool {
	switch status {
	case "pending", "applying", "applied", "partial", "rejected", "unknown":
		return true
	}
	return false
}

func validateInferenceReply(receipt *inferenceReceipt, requestID string, revision int) error {
	if receipt.RequestID != requestID || receipt.Revision != revision || !validInferenceStatus(receipt.Status) {
		return fmt.Errorf("settings response did not confirm the submitted request")
	}
	return nil
}

func inferenceOutcomeError(receipt *inferenceReceipt) error {
	switch receipt.Status {
	case "pending", "applying", "applied":
		return nil
	default:
		return fmt.Errorf("settings change %s; %s", receipt.Status, orDash(receipt.Error))
	}
}

func inferenceSubmissionError(sessionID, contextName, requestID string, err error) error {
	return fmt.Errorf("settings request %s: %w\nInspect its outcome before retrying: %s", requestID, err, inferenceDescribeCommand(sessionID, contextName))
}

func inferenceDescribeCommand(sessionID, contextName string) string {
	command := "telos describe " + shellQuote(sessionID)
	if contextName != "" {
		command += " --context " + shellQuote(contextName)
	}
	return command + " --json"
}

func printInferenceReceipt(out io.Writer, receipt *inferenceReceipt) {
	printSummaryField(out, "Session", receipt.SessionID)
	if receipt.Context != "" {
		printSummaryField(out, "Context", receipt.Context)
	}
	status := receipt.Status
	if status == "" {
		status = "no change requested"
	}
	printSummaryField(out, "Settings", fmt.Sprintf("%s (revision %d)", status, receipt.Revision))
	printInferenceSettings(out, receipt.Settings)
	if receipt.RequestID != "" {
		printSummaryField(out, "Request", receipt.RequestID)
	}
	if receipt.RequestedModel != "" {
		printDetailField(out, "Requested model", receipt.RequestedModel)
	}
	if receipt.RequestedThinking != "" {
		printDetailField(out, "Requested thinking", receipt.RequestedThinking)
	}
	if receipt.Error != "" {
		printSummaryField(out, "Error", receipt.Error)
	}
	if receipt.Status == "pending" || receipt.Status == "applying" || receipt.Status == "unknown" {
		fmt.Fprintln(out, "Model and thinking above are the last confirmed settings; this change is not confirmed.")
	}
	if receipt.Status == "pending" || receipt.Status == "applying" {
		fmt.Fprintf(out, "\nQueued for the next prover or verifier turn. Idle sessions wait for their next scheduled or triggered turn.\nCheck with: %s\n", inferenceDescribeCommand(receipt.SessionID, receipt.Context))
	}
}

func printInferenceSettings(out io.Writer, settings sessionapi.InferenceSettings) {
	printSummaryField(out, "Model", strings.TrimPrefix(settings.Model, "telos-bifrost/"))
	printSummaryField(out, "Thinking", settings.Thinking)
}

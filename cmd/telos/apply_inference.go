package main

import (
	"flag"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/telos-org/telos/internal/cloud"
)

var thinkingSyntax = regexp.MustCompile(`^[a-z]{1,32}$`)

type inferenceChange struct {
	SessionID string `json:"goal_id"`
	Context   string `json:"context,omitempty"`
	*cloud.DeploymentInference
}

func validateApplyInferenceFlags(fs *flag.FlagSet, sessionID, model, thinking string) error {
	settingsSet := flagNamesSet(fs, "model", "thinking")
	if fs.NArg() > 0 {
		if sessionID != "" && settingsSet {
			return fmt.Errorf("apply spec and model/thinking changes separately: first apply SPEC.md --goal %s, then apply --goal %s --model MODEL and/or --thinking LEVEL", sessionID, sessionID)
		}
		return nil
	}
	if sessionID == "" || !settingsSet {
		return fmt.Errorf("provide a SPEC.md, or --goal GOAL with --model and/or --thinking")
	}
	if err := validateApplySession(sessionID); err != nil {
		return err
	}
	if flagNameSet(fs, "force") {
		return fmt.Errorf("--force cannot be used for a settings-only apply")
	}
	if flagNameSet(fs, "model") && strings.TrimSpace(model) == "" {
		return fmt.Errorf("--model requires a non-empty model")
	}
	if flagNameSet(fs, "thinking") && !thinkingSyntax.MatchString(strings.TrimSpace(thinking)) {
		return fmt.Errorf("--thinking requires a model-supported level using 1 to 32 lowercase letters")
	}
	return nil
}

func applySessionInference(sessionID, model, thinking, contextOverride string) (*inferenceChange, error) {
	if err := validateApplySession(sessionID); err != nil {
		return nil, err
	}
	if err := validateCloudSessionContext(sessionID, contextOverride); err != nil {
		return nil, err
	}
	control, err := cloud.ControlClientForContext(contextOverride)
	if err != nil {
		return nil, err
	}
	selection, err := resolveCloudInference(control, model)
	if err != nil {
		return nil, err
	}
	request := cloud.DeploymentInferenceRequest{Inference: selection}
	if thinking != "" {
		request.AgentThinking = &thinking
	}
	state, err := control.UpdateDeploymentInference(sessionID, request)
	if err != nil {
		return nil, err
	}
	return &inferenceChange{SessionID: sessionID, Context: control.ContextName(), DeploymentInference: state}, nil
}

func printInferenceChange(out io.Writer, change *inferenceChange) {
	printSummaryField(out, "Goal", change.SessionID)
	if change.Context != "" {
		printSummaryField(out, "Context", change.Context)
	}
	printCloudInferenceSummary(out, cloud.SessionRecord{AgentModel: change.AgentModel, AgentThinking: change.AgentThinking, Inference: &change.Inference})
	if change.Applied {
		printSummaryField(out, "Applies", "next turn")
	} else {
		printSummaryField(out, "Applies", "next turn after the Goal's runtime receives it")
	}
}

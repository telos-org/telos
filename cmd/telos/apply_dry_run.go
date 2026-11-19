package main

import (
	"fmt"
	"io"
	"strings"

	internaldiff "github.com/rogpeppe/go-internal/diff"
	"github.com/telos-org/telos/internal/cloud"
)

type previewSettingSource string

const (
	previewSourceGoal      previewSettingSource = "goal"
	previewSourceWorkspace previewSettingSource = "workspace"
	previewSourceCloud     previewSettingSource = "cloud"
	previewSourceOverride  previewSettingSource = "override"
)

type applyPreview struct {
	DryRun     bool                   `json:"dry_run"`
	Operation  string                 `json:"operation"`
	Context    string                 `json:"context"`
	GoalID     string                 `json:"goal_id,omitempty"`
	Force      bool                   `json:"force,omitempty"`
	Current    *applyPreviewState     `json:"current,omitempty"`
	Proposed   applyPreviewState      `json:"proposed"`
	SpecDiff   string                 `json:"spec_diff,omitempty"`
	SkillDiffs []previewSkillFileDiff `json:"skill_diffs,omitempty"`
	Notes      []string               `json:"notes"`
}

type applyPreviewState struct {
	applyPackageState
	Inference      *cloud.InferenceSelection `json:"inference,omitempty"`
	Model          string                    `json:"model,omitempty"`
	ModelSource    previewSettingSource      `json:"model_source,omitempty"`
	Thinking       string                    `json:"thinking,omitempty"`
	ThinkingSource previewSettingSource      `json:"thinking_source,omitempty"`
}

func newApplyPreview(control *cloud.Client, goalID string) *applyPreview {
	operation := "create"
	if goalID != "" {
		operation = "update"
	}
	return &applyPreview{
		DryRun:    true,
		Operation: operation,
		Context:   control.ContextName(),
		GoalID:    goalID,
		Notes:     []string{"Other server-side checks run when applying."},
	}
}

func previewCloudApply(
	control *cloud.Client,
	prepared *preparedCloudApply,
	goalID string,
	runtime sessionRuntimeConfig,
	options cloudApplyOptions,
) (*applyPreview, error) {
	preview := newApplyPreview(control, goalID)
	preview.Force = options.force
	proposed := prepared.proposed
	preview.Proposed.applyPackageState = proposed
	if prepared.pending {
		preview.Notes = append(preview.Notes,
			"New skill references and the final package digest are assigned during publication. "+
				"The spec preview uses local skill paths.",
		)
	}
	if goalID == "" {
		if err := previewCreationInference(control, preview, prepared.inference, runtime); err != nil {
			return nil, err
		}
		previewSkillChanges(control, preview, nil, proposed.data, options.includeDiff)
		return preview, nil
	}
	current := prepared.current
	preview.Current = &applyPreviewState{applyPackageState: *current}
	preview.SpecDiff = string(internaldiff.Diff(
		"deployed/SPEC.md", []byte(current.Spec),
		"proposed/SPEC.md", []byte(proposed.Spec),
	))
	preview.Proposed.ModelSource = previewSourceGoal
	preview.Proposed.ThinkingSource = previewSourceGoal
	if proposed.Digest != "" && proposed.Digest == current.Digest && proposed.Ref == current.Ref {
		preview.Operation = "unchanged"
	}
	if options.force {
		preview.Notes = append(preview.Notes, "The server's snapshot check would be bypassed by --force.")
	}
	previewSkillChanges(control, preview, current.data, proposed.data, options.includeDiff)
	return preview, nil
}

func previewCreationInference(
	control *cloud.Client,
	preview *applyPreview,
	selection *cloud.InferenceSelection,
	runtime sessionRuntimeConfig,
) error {
	var preference *cloud.InferencePreference
	if selection == nil || runtime.Thinking == "" {
		var err error
		preference, err = control.InferencePreference()
		if err != nil {
			return fmt.Errorf("read workspace inference defaults: %w", err)
		}
	}
	state := &preview.Proposed
	state.Inference = selection
	state.Model = runtime.Model
	state.ModelSource = previewSourceOverride
	if selection == nil {
		state.Inference = &preference.Selection
		state.ModelSource = previewSourceWorkspace
		var connections []cloud.InferenceConnection
		if preference.Selection.Source != "managed" {
			var err error
			connections, err = control.ListInferenceConnections()
			if err != nil {
				return err
			}
		}
		state.Model = inferenceSelectionName(preference.Selection, connections)
	}
	state.Thinking = runtime.Thinking
	state.ThinkingSource = previewSourceOverride
	if runtime.Thinking == "" {
		state.ThinkingSource = previewSourceCloud
		if preference.AgentThinking != nil && *preference.AgentThinking != "" {
			state.Thinking = *preference.AgentThinking
			state.ThinkingSource = previewSourceWorkspace
		}
		preview.Notes = append(preview.Notes,
			"The server resolves inherited thinking for the selected model when applying.",
		)
	}
	return nil
}

func previewSessionInference(goalID, model, thinking, contextOverride string) (*applyPreview, error) {
	control, request, err := prepareSessionInference(goalID, model, thinking, contextOverride)
	if err != nil {
		return nil, err
	}
	current, err := control.GetDeploymentInference(goalID)
	if err != nil {
		return nil, goalNotFound(err, goalID, control.ContextName())
	}
	preview := newApplyPreview(control, goalID)
	preview.Current = &applyPreviewState{
		Inference: current.Selection,
		Model: cloudSessionModel(cloud.SessionRecord{
			AgentModel: current.AgentModel,
			Inference:  &current.Inference,
		}),
		Thinking:       current.AgentThinking,
		ModelSource:    previewSourceGoal,
		ThinkingSource: previewSourceGoal,
	}
	preview.Proposed = *preview.Current
	modelUnchanged := request.Inference == nil ||
		current.Selection != nil && *request.Inference == *current.Selection
	thinkingUnchanged := request.AgentThinking == nil || *request.AgentThinking == current.AgentThinking
	if modelUnchanged && thinkingUnchanged {
		preview.Operation = "unchanged"
	}
	if request.Inference != nil {
		preview.Proposed.Inference = request.Inference
		preview.Proposed.Model = model
		preview.Proposed.ModelSource = previewSourceOverride
		if !modelUnchanged && request.AgentThinking == nil {
			preview.Notes = append(preview.Notes,
				"The server resolves inherited thinking for the selected model when applying.",
			)
		}
	}
	if request.AgentThinking != nil {
		preview.Proposed.Thinking = thinking
		preview.Proposed.ThinkingSource = previewSourceOverride
	}
	return preview, nil
}

func printApplyPreview(out io.Writer, preview *applyPreview, jsonOut bool) {
	if jsonOut {
		printJSON(preview)
		return
	}
	printSummaryField(out, "Operation", preview.Operation)
	printSummaryField(out, "Context", preview.Context)
	if preview.GoalID != "" {
		printSummaryField(out, "Goal", preview.GoalID)
	}
	proposed := preview.Proposed
	if proposed.Name != "" {
		printSummaryField(out, "Spec", proposed.Name)
		printSummaryField(out, "Proposed", proposed.Ref)
		if preview.Current == nil {
			printSummaryField(out, "Version", proposed.Version)
			if proposed.IntervalSeconds != nil {
				printSummaryField(out, "Interval", formatPreviewInterval(proposed.IntervalSeconds))
			}
			if len(proposed.Skills) > 0 {
				printSummaryField(out, "Skills", formatPreviewSkillLocks(proposed.Skills))
			}
		} else {
			printSummaryField(out, "Current", preview.Current.Ref)
			printPreviewStateDelta(out, preview.Current.applySpecState, proposed.applySpecState)
		}
	}
	model := previewInferenceValue(proposed.Model, proposed.ModelSource)
	thinking := previewInferenceValue(proposed.Thinking, proposed.ThinkingSource)
	if preview.Current != nil && proposed.Name == "" {
		model = previewInferenceDelta(preview.Current.Model, proposed.Model, proposed.ModelSource)
		thinking = previewInferenceDelta(preview.Current.Thinking, proposed.Thinking, proposed.ThinkingSource)
	}
	printSummaryField(out, "Model", model)
	printSummaryField(out, "Thinking", thinking)
	if preview.SpecDiff != "" {
		fmt.Fprintln(out)
		fmt.Fprint(out, preview.SpecDiff)
	}
	for _, file := range preview.SkillDiffs {
		fmt.Fprintln(out)
		fmt.Fprintf(out, "%s skills/%s/%s", file.Change, file.Skill, file.Path)
		if file.OldMode != "" && file.NewMode != "" && file.OldMode != file.NewMode {
			fmt.Fprintf(out, " (mode %s)", previewDeltaValue(file.OldMode, file.NewMode))
		}
		if file.ContentOmitted != "" {
			fmt.Fprintf(out, " (%s)", file.ContentOmitted)
		}
		fmt.Fprintln(out)
		fmt.Fprint(out, file.Diff)
	}
	fmt.Fprintln(out)
	for _, note := range preview.Notes {
		fmt.Fprintln(out, note)
	}
	fmt.Fprintln(out, "Nothing was applied.")
}

func previewInferenceDelta(current, proposed string, source previewSettingSource) string {
	value := previewInferenceValue(proposed, source)
	if current == proposed || source == previewSourceGoal {
		return value
	}
	return previewDeltaValue(current, value)
}

func previewInferenceValue(value string, source previewSettingSource) string {
	switch source {
	case previewSourceGoal:
		if value == "" {
			return "unchanged"
		}
		return value + " (Goal setting)"
	case previewSourceWorkspace:
		return value + " (workspace default)"
	case previewSourceCloud:
		return "cloud default (resolved on apply)"
	default:
		return strings.TrimSpace(value)
	}
}

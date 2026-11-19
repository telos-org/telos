package main

import (
	"fmt"
	"io"
	"strings"

	internaldiff "github.com/rogpeppe/go-internal/diff"
	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/spec"
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
	previewSpecState
	Name           string                    `json:"name,omitempty"`
	Ref            string                    `json:"ref,omitempty"`
	Digest         string                    `json:"digest,omitempty"`
	Spec           string                    `json:"spec,omitempty"`
	Inference      *cloud.InferenceSelection `json:"inference,omitempty"`
	Model          string                    `json:"model,omitempty"`
	ModelSource    string                    `json:"model_source,omitempty"`
	Thinking       string                    `json:"thinking,omitempty"`
	ThinkingSource string                    `json:"thinking_source,omitempty"`
}

func newApplyPreview(control *cloud.Client, goalID string) *applyPreview {
	operation := "create"
	if goalID != "" {
		operation = "update"
	}
	return &applyPreview{
		DryRun: true, Operation: operation, Context: control.ContextName(), GoalID: goalID,
		Notes: []string{"Server-side checks were not evaluated."},
	}
}

func previewCloudApply(control *cloud.Client, prepared *preparedCloudApply, goalID string, runtime sessionRuntimeConfig, force, includeDiff bool) (*applyPreview, error) {
	preview := newApplyPreview(control, goalID)
	preview.Force = force
	proposed, pending, err := previewApplyPackage(control, prepared)
	if err != nil {
		return nil, err
	}
	preview.Proposed = proposed
	var proposedData []byte
	if prepared.local != nil {
		proposedData = prepared.local.bytes
	} else {
		proposedData = prepared.registry.data
	}
	if pending {
		preview.Notes = append(preview.Notes, "New skill versions and the final package digest are assigned during publication.")
	}
	if goalID == "" {
		if err := previewCreationInference(control, preview, prepared.inference, runtime); err != nil {
			return nil, err
		}
		previewSkillChanges(control, preview, nil, proposedData, includeDiff)
		return preview, nil
	}
	currentPackage, err := packageForSession(control, goalID)
	if err != nil {
		return nil, err
	}
	current, err := previewPackageState(currentPackage)
	if err != nil {
		return nil, err
	}
	preview.Current = &current
	preview.SpecDiff = string(internaldiff.Diff("deployed/SPEC.md", []byte(current.Spec), "proposed/SPEC.md", []byte(proposed.Spec)))
	preview.Proposed.ModelSource = "goal"
	preview.Proposed.ThinkingSource = "goal"
	if proposed.Digest != "" && proposed.Digest == current.Digest && proposed.Ref == current.Ref {
		preview.Operation = "unchanged"
	}
	if force {
		preview.Notes = append(preview.Notes, "The server's snapshot check would be bypassed by --force.")
	} else {
		preview.Notes = append(preview.Notes, "The server's snapshot check was not evaluated.")
	}
	previewSkillChanges(control, preview, currentPackage.data, proposedData, includeDiff)
	return preview, nil
}

func previewPackageState(pkg *pulledPackage) (applyPreviewState, error) {
	markdown, manifest, err := verifiedPackageContents(pkg)
	if err != nil {
		return applyPreviewState{}, err
	}
	state, err := previewSpecStateFromMarkdown(markdown, manifest)
	return applyPreviewState{
		previewSpecState: state, Name: pkg.reference.name, Ref: pkg.reference.ref,
		Digest: pkg.digest, Spec: string(markdown),
	}, err
}

func previewApplyPackage(control *cloud.Client, prepared *preparedCloudApply) (applyPreviewState, bool, error) {
	if prepared.registry != nil {
		state, err := previewPackageState(prepared.registry)
		return state, false, err
	}
	pkg := prepared.local
	scope, err := defaultPublishScope(control)
	if err != nil {
		return applyPreviewState{}, false, err
	}
	markdown, manifest, err := spec.ApplyPackageSpec(pkg.bytes)
	if err != nil {
		return applyPreviewState{}, false, err
	}
	refs := map[string]string{}
	pending := false
	for _, publication := range prepared.skills {
		ref := publication.ref
		if publication.skill != nil {
			if publication.skill.version != "" {
				ref = fmt.Sprintf("@%s/%s:%s", scope, publication.name, publication.skill.version)
			} else {
				record, err := control.GetSkill(scope, publication.name)
				if err != nil && !cloud.IsStatus(err, 404) {
					return applyPreviewState{}, false, err
				}
				if err == nil && record.Digest == manifest.Skills[publication.name].Digest {
					ref = record.Ref
				}
			}
		}
		if ref == "" {
			pending = true
		}
		refs[publication.name] = ref
	}
	digest := ""
	if !pending {
		resolved, err := spec.BuildApplyPackageWithSkillRefs(pkg.compiled, refs)
		if err != nil {
			return applyPreviewState{}, false, err
		}
		markdown, manifest, err = spec.ApplyPackageSpec(resolved.Bytes)
		if err != nil {
			return applyPreviewState{}, false, err
		}
		digest = resolved.Digest
	}
	state, err := previewSpecStateFromMarkdown(markdown, manifest)
	if err != nil {
		return applyPreviewState{}, false, err
	}
	for i := range state.Skills {
		state.Skills[i].Ref = refs[state.Skills[i].Name]
	}
	return applyPreviewState{
		previewSpecState: state, Name: pkg.name, Ref: fmt.Sprintf("@%s/%s:%s", scope, pkg.name, pkg.version),
		Digest: digest, Spec: string(markdown),
	}, pending, nil
}

func previewCreationInference(control *cloud.Client, preview *applyPreview, selection *cloud.InferenceSelection, runtime sessionRuntimeConfig) error {
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
	state.Model, state.ModelSource = runtime.Model, "override"
	if selection == nil {
		state.Inference = &preference.Selection
		state.ModelSource = "workspace"
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
	state.Thinking, state.ThinkingSource = runtime.Thinking, "override"
	if runtime.Thinking == "" {
		state.ThinkingSource = "cloud"
		if preference.AgentThinking != nil && *preference.AgentThinking != "" {
			state.Thinking, state.ThinkingSource = *preference.AgentThinking, "workspace"
		}
		preview.Notes = append(preview.Notes, "The server resolves inherited thinking for the selected model when applying.")
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
		Inference: current.Selection, Model: cloudSessionModel(cloud.SessionRecord{AgentModel: current.AgentModel, Inference: &current.Inference}),
		Thinking: current.AgentThinking, ModelSource: "goal", ThinkingSource: "goal",
	}
	preview.Proposed = *preview.Current
	modelUnchanged := request.Inference == nil || current.Selection != nil && *request.Inference == *current.Selection
	thinkingUnchanged := request.AgentThinking == nil || *request.AgentThinking == current.AgentThinking
	if modelUnchanged && thinkingUnchanged {
		preview.Operation = "unchanged"
	}
	if request.Inference != nil {
		preview.Proposed.Inference = request.Inference
		preview.Proposed.Model, preview.Proposed.ModelSource = model, "override"
		if !modelUnchanged && request.AgentThinking == nil {
			preview.Notes = append(preview.Notes, "The server resolves inherited thinking for the selected model when applying.")
		}
	}
	if request.AgentThinking != nil {
		preview.Proposed.Thinking, preview.Proposed.ThinkingSource = thinking, "override"
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
			printPreviewStateDelta(out, preview.Current.previewSpecState, proposed.previewSpecState)
		}
	}
	model, thinking := previewInferenceValue(proposed.Model, proposed.ModelSource), previewInferenceValue(proposed.Thinking, proposed.ThinkingSource)
	if preview.Current != nil && proposed.Name == "" {
		model = previewDeltaValue(preview.Current.Model, model)
		thinking = previewDeltaValue(preview.Current.Thinking, thinking)
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
		if file.OldMode != file.NewMode {
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

func previewInferenceValue(value, source string) string {
	switch source {
	case "goal":
		if value == "" {
			return "unchanged"
		}
		return value + " (Goal setting)"
	case "workspace":
		return value + " (workspace default)"
	case "cloud":
		return "cloud default (resolved on apply)"
	default:
		return strings.TrimSpace(value)
	}
}

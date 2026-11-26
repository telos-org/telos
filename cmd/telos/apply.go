package main

import (
	"bytes"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	internaldiff "github.com/rogpeppe/go-internal/diff"
	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/spec"
)

type preparedCloudApply struct {
	name        string
	local       *specPackage
	record      *cloud.PackageVersionRecord
	skills      []packageSkillPublication
	inference   *cloud.InferenceSelection
	proposed    applyState
	current     *applyState
	baselineErr error
}

func prepareCloudApply(control *cloud.Client, input, goalID, model, contextOverride string) (*preparedCloudApply, error) {
	prepared := &preparedCloudApply{}
	var err error
	if strings.HasPrefix(strings.TrimSpace(input), "@") {
		reference, err := parsePackageReference(input)
		if err != nil {
			return nil, err
		}
		pkg, record, err := resolvePackageReference(control, reference)
		if err != nil {
			return nil, err
		}
		prepared.record = record
		prepared.proposed, err = registryApplyPackageState(pkg)
		if err != nil {
			return nil, err
		}
		prepared.name = reference.name
	} else {
		prepared.local, err = packageSpec(input, contextOverride)
		if err != nil {
			return nil, err
		}
		prepared.name = prepared.local.name
		prepared.local.version, err = normalizePackageVersion(prepared.local.version)
		if err != nil {
			return nil, err
		}
	}
	if goalID == "" {
		if err := refuseDuplicateGoal(control, input, prepared.name, contextOverride); err != nil {
			return nil, err
		}
		prepared.inference, err = resolveCloudInference(control, model)
		if err != nil {
			return nil, err
		}
	}
	if prepared.local != nil {
		prepared.skills, err = preparePackageSkills(control, prepared.local.compiled)
		if err != nil {
			return nil, err
		}
		prepared.proposed, err = prepareLocalApplyPackage(control, prepared.local, prepared.skills)
		if err != nil {
			return nil, err
		}
	}
	if goalID != "" {
		session, err := control.GetSession(goalID)
		if err != nil {
			return nil, goalNotFound(err, goalID, control.ContextName())
		}
		pkg, err := packageForSessionRecord(control, session)
		if err != nil {
			prepared.baselineErr = err
			return prepared, nil
		}
		current, err := registryApplyPackageState(pkg)
		if err != nil {
			prepared.baselineErr = err
			return prepared, nil
		}
		if current.Name != "" && prepared.proposed.Name != current.Name {
			return nil, fmt.Errorf(
				"cannot change Goal %s spec name from %q to %q", goalID, current.Name, prepared.proposed.Name,
			)
		}
		prepared.current = &current
	}
	return prepared, nil
}

type applyState struct {
	Name            string           `json:"name,omitempty"`
	Version         string           `json:"version,omitempty"`
	IntervalSeconds *int             `json:"interval_seconds,omitempty"`
	Skills          []applySkillLock `json:"skills,omitempty"`

	Ref    string `json:"ref,omitempty"`
	Digest string `json:"digest,omitempty"`
	Spec   string `json:"spec,omitempty"`
	data   []byte

	Inference      *cloud.InferenceSelection `json:"inference,omitempty"`
	Model          string                    `json:"model,omitempty"`
	ModelSource    previewSettingSource      `json:"model_source,omitempty"`
	Thinking       string                    `json:"thinking,omitempty"`
	ThinkingSource previewSettingSource      `json:"thinking_source,omitempty"`
}

func registryApplyPackageState(pkg *pulledPackage) (applyState, error) {
	markdown, manifest, err := verifiedPackageContents(pkg)
	if err != nil {
		return applyState{}, err
	}
	return readApplyPackageState(pkg, markdown, manifest)
}

func prepareLocalApplyPackage(
	control *cloud.Client,
	pkg *specPackage,
	skills []packageSkillPublication,
) (applyState, error) {
	scope, err := defaultPublishScope(control)
	if err != nil {
		return applyState{}, err
	}
	markdown, manifest, err := spec.ApplyPackageSpec(pkg.bytes)
	if err != nil {
		return applyState{}, err
	}
	refs := map[string]string{}
	unversioned := map[string]bool{}
	pending := false
	for _, publication := range skills {
		ref, err := applySkillRef(control, scope, publication, manifest.Skills[publication.name].Digest)
		if err != nil {
			return applyState{}, err
		}
		if ref == "" {
			pending = true
		}
		refs[publication.name] = ref
		if publication.skill != nil && publication.skill.version == "" {
			unversioned[publication.name] = true
		}
	}
	var resolved *spec.ApplyPackage
	if !pending {
		resolved, err = spec.BuildApplyPackageWithSkillRefs(pkg.compiled, refs)
		if err != nil {
			return applyState{}, err
		}
	}
	ref := fmt.Sprintf("@%s/%s:%s", scope, pkg.name, pkg.version)
	existing, err := control.GetPackageVersion(scope, pkg.name, pkg.version)
	if err != nil && !cloud.IsStatus(err, 404) {
		return applyState{}, fmt.Errorf("check package version %s: %w", ref, err)
	}
	if err == nil {
		if (resolved == nil || resolved.Digest != existing.Digest) && len(unversioned) > 0 {
			resolved, err = matchExistingApplyPackage(control, pkg, scope, refs, unversioned, existing.Digest)
			if err != nil {
				return applyState{}, fmt.Errorf("check package version %s: %w", ref, err)
			}
		}
		if resolved == nil || resolved.Digest != existing.Digest {
			return applyState{}, fmt.Errorf(
				"package version %s already exists; bump version in SPEC.md before changing the spec or its skills", ref,
			)
		}
	}
	digest := ""
	if resolved != nil {
		markdown, manifest, err = spec.ApplyPackageSpec(resolved.Bytes)
		if err != nil {
			return applyState{}, err
		}
		digest = resolved.Digest
	}
	// Keep publication tied to the selected locks, including older matching versions.
	for i, publication := range skills {
		if publication.skill != nil && publication.skill.version == "" {
			skills[i].ref = refs[publication.name]
			if resolved != nil {
				skills[i].ref = manifest.Skills[publication.name].Ref
			}
		}
	}
	state, err := readApplyPackageState(&pulledPackage{
		reference: packageReference{ref: ref},
		digest:    digest,
		data:      pkg.bytes,
	}, markdown, manifest)
	if resolved == nil {
		for i := range state.Skills {
			state.Skills[i].Ref = refs[state.Skills[i].Name]
		}
	}
	return state, err
}

func matchExistingApplyPackage(
	control *cloud.Client,
	pkg *specPackage,
	scope string,
	refs map[string]string,
	unversioned map[string]bool,
	existingDigest string,
) (*spec.ApplyPackage, error) {
	data, err := control.DownloadPackageVersionBundle(scope, pkg.name, pkg.version)
	if err != nil {
		return nil, err
	}
	_, existing, err := verifiedPackageContents(&pulledPackage{
		reference: packageReference{ref: fmt.Sprintf("@%s/%s:%s", scope, pkg.name, pkg.version)},
		digest:    existingDigest,
		data:      data,
	})
	if err != nil {
		return nil, err
	}
	comparisonRefs := maps.Clone(refs)
	for name := range unversioned {
		oldRef := existing.Skills[name].Ref
		parsed, ok := spec.ParseRegistrySkillRef(oldRef)
		if !ok || parsed.Scope != scope || parsed.Name != name || parsed.Version == "" {
			return nil, nil
		}
		comparisonRefs[name] = oldRef
	}
	comparison, err := spec.BuildApplyPackageWithSkillRefs(pkg.compiled, comparisonRefs)
	if err != nil {
		return nil, err
	}
	if comparison.Digest != existingDigest {
		return nil, nil
	}
	return comparison, nil
}

func applySkillRef(control *cloud.Client, scope string, publication packageSkillPublication, digest string) (string, error) {
	if publication.skill == nil {
		return publication.ref, nil
	}
	name, version := publication.name, publication.skill.version
	ref := fmt.Sprintf("@%s/%s", scope, name)
	if version != "" {
		ref += ":" + version
		record, err := control.GetSkillVersion(scope, name, version)
		switch {
		case cloud.IsStatus(err, 404):
			return ref, nil
		case err != nil:
			return "", fmt.Errorf("check skill version %s: %w", ref, err)
		case record.Digest != digest:
			return "", fmt.Errorf("skill version %s already exists with different content; bump version in SKILL.md", ref)
		default:
			return ref, nil
		}
	}
	record, err := control.GetSkill(scope, name)
	switch {
	case cloud.IsStatus(err, 404):
		return "", nil
	case err != nil:
		return "", err
	case record.Digest != digest:
		return "", nil
	case record.Scope != scope || record.Name != name:
		return "", fmt.Errorf("skill %s resolved to %s", ref, record.Ref)
	default:
		return record.Ref, nil
	}
}

type applySkillLock struct {
	Name    string `json:"name"`
	Ref     string `json:"ref,omitempty"`
	Digest  string `json:"digest"`
	Starred bool   `json:"required_rubric,omitempty"`
}

func readApplyPackageState(
	pkg *pulledPackage,
	markdown []byte,
	manifest *spec.ApplyPackageManifest,
) (applyState, error) {
	raw, _, ok := spec.ParseFrontmatter(string(markdown))
	if !ok {
		return applyState{}, fmt.Errorf("spec has no valid YAML frontmatter")
	}
	state := applyState{
		Ref: pkg.reference.ref, Digest: pkg.digest, Spec: string(markdown), data: pkg.data,
	}
	if name, ok := raw["name"]; ok {
		state.Name = strings.TrimSpace(fmt.Sprint(name))
	}
	if version, ok := raw["version"].(string); ok {
		state.Version = strings.TrimSpace(version)
	}
	if interval, ok := raw["interval"]; ok {
		duration, err := time.ParseDuration(strings.TrimSpace(fmt.Sprint(interval)))
		if err != nil || duration < 0 || duration%time.Second != 0 {
			return applyState{}, fmt.Errorf("invalid interval %q", interval)
		}
		seconds := int(duration / time.Second)
		state.IntervalSeconds = &seconds
	}
	for _, name := range slices.Sorted(maps.Keys(manifest.Skills)) {
		lock := manifest.Skills[name]
		ref := strings.TrimSpace(lock.Ref)
		if ref == "" {
			ref = strings.TrimSpace(manifest.SkillProvenance[name].Ref)
		}
		state.Skills = append(state.Skills, applySkillLock{
			Name:    name,
			Ref:     ref,
			Digest:  strings.TrimSpace(lock.Digest),
			Starred: lock.Starred,
		})
	}
	return state, nil
}

type previewSettingSource string

const (
	previewSourceGoal      previewSettingSource = "goal"
	previewSourceWorkspace previewSettingSource = "workspace"
	previewSourceCloud     previewSettingSource = "cloud"
	previewSourceOverride  previewSettingSource = "override"
	previewSourceInherited previewSettingSource = "inherited"
)

type applyPreview struct {
	DryRun     bool                   `json:"dry_run"`
	Operation  string                 `json:"operation"`
	Context    string                 `json:"context"`
	GoalID     string                 `json:"goal_id,omitempty"`
	Force      bool                   `json:"force,omitempty"`
	Current    *applyState            `json:"current,omitempty"`
	Proposed   applyState             `json:"proposed"`
	SpecDiff   string                 `json:"spec_diff,omitempty"`
	SkillDiffs []previewSkillFileDiff `json:"skill_diffs,omitempty"`
	Notes      []string               `json:"notes"`
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
		Notes:     []string{},
	}
}

func previewCloudApply(
	control *cloud.Client,
	prepared *preparedCloudApply,
	goalID string,
	runtime sessionRuntimeConfig,
	options cloudApplyOptions,
) (*applyPreview, error) {
	if prepared.baselineErr != nil {
		return nil, fmt.Errorf("read deployed baseline: %w", prepared.baselineErr)
	}
	preview := newApplyPreview(control, goalID)
	preview.Force = options.force
	proposed := prepared.proposed
	preview.Proposed = proposed
	if proposed.Digest == "" {
		preview.Notes = append(preview.Notes,
			"Skill references and the final package digest are resolved during publication. "+
				"The spec preview uses local skill paths.",
		)
	}
	if goalID == "" {
		if err := previewCreationInference(control, preview, prepared.inference, runtime); err != nil {
			return nil, err
		}
	} else {
		current := prepared.current
		preview.Current = current
		preview.SpecDiff = string(internaldiff.Diff(
			"deployed/SPEC.md", []byte(current.Spec),
			"proposed/SPEC.md", []byte(proposed.Spec),
		))
		preview.Proposed.ModelSource = previewSourceGoal
		preview.Proposed.ThinkingSource = previewSourceGoal
		if proposed.Digest != "" && proposed.Digest == current.Digest {
			preview.Operation = "unchanged"
			if proposed.Ref != current.Ref {
				preview.Proposed.Ref = current.Ref
				preview.Notes = append(preview.Notes, "The package contents are unchanged. The Goal keeps its current package reference.")
			}
		}
		if options.force {
			preview.Notes = append(preview.Notes, "The server's snapshot check would be bypassed by --force.")
		}
	}
	previewSkillChanges(control, preview, options.includeDiff)
	return preview, nil
}

func printApplyHeadline(out io.Writer, operation, name string) {
	var verb string
	switch operation {
	case "create":
		verb = "Would create"
	case "update":
		verb = "Would update"
	case "created":
		verb = "Created"
	case "updated":
		verb = "Updated"
	case "unchanged":
		verb = "No changes to"
	default:
		return
	}
	fmt.Fprintf(out, "%s %s\n\n", verb, name)
}

func printApplyPackage(out io.Writer, current, proposed string) {
	if current != "" && current != proposed {
		proposed = previewDeltaValue(current, proposed)
	}
	printSummaryField(out, "Package", proposed)
}

func printApplyPreview(out io.Writer, preview *applyPreview, jsonOut bool) {
	if jsonOut {
		printJSON(preview)
		return
	}
	proposed := preview.Proposed
	printApplyHeadline(out, preview.Operation, firstNonEmpty(proposed.Name, preview.GoalID))
	printSummaryField(out, "Context", preview.Context)
	if preview.GoalID != "" {
		printSummaryField(out, "Goal", preview.GoalID)
	}
	if proposed.Name != "" {
		if preview.Current == nil {
			printApplyPackage(out, "", proposed.Ref)
			if proposed.IntervalSeconds != nil {
				printSummaryField(out, "Interval", formatPreviewInterval(proposed.IntervalSeconds))
			}
			if len(proposed.Skills) > 0 {
				printSummaryField(out, "Skills", formatPreviewSkillLocks(proposed.Skills))
			}
		} else {
			printApplyPackage(out, preview.Current.Ref, proposed.Ref)
			printPreviewStateDelta(out, *preview.Current, proposed)
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
	if len(preview.Notes) > 0 {
		fmt.Fprintln(out)
	}
	for _, note := range preview.Notes {
		fmt.Fprintln(out, note)
	}
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
	case previewSourceInherited:
		return "inherited (resolved on apply)"
	default:
		return strings.TrimSpace(value)
	}
}

func printPreviewStateDelta(out io.Writer, current, proposed applyState) {
	currentInterval := formatPreviewInterval(current.IntervalSeconds)
	proposedInterval := formatPreviewInterval(proposed.IntervalSeconds)
	intervalChanged := currentInterval != proposedInterval
	if intervalChanged {
		printSummaryField(out, "Interval", previewDeltaValue(currentInterval, proposedInterval))
	}
	if !slices.Equal(current.Skills, proposed.Skills) {
		if intervalChanged {
			fmt.Fprintln(out)
		}
		fmt.Fprintln(out, "Skill locks")
		printDetailField(out, "current", formatPreviewSkillLocks(current.Skills))
		printDetailField(out, "proposed", formatPreviewSkillLocks(proposed.Skills))
	}
}

func previewDeltaValue(current, proposed string) string {
	return firstNonEmpty(current, "-") + " -> " + firstNonEmpty(proposed, "-")
}

// formatPreviewInterval writes an interval the way specs do, such as 6h or 1h30m.
func formatPreviewInterval(seconds *int) string {
	if seconds == nil {
		return "-"
	}
	value := (time.Duration(*seconds) * time.Second).String()
	if strings.HasSuffix(value, "m0s") {
		value = strings.TrimSuffix(value, "0s")
	}
	if strings.HasSuffix(value, "h0m") {
		value = strings.TrimSuffix(value, "0m")
	}
	return value
}

func formatPreviewSkillLocks(skills []applySkillLock) string {
	if len(skills) == 0 {
		return "-"
	}
	values := make([]string, 0, len(skills))
	for _, skill := range skills {
		value := skill.Name
		if skill.Ref != "" {
			value += " " + skill.Ref
		}
		value += " " + skill.Digest
		if skill.Starred {
			value += " *"
		}
		values = append(values, value)
	}
	return strings.Join(values, ", ")
}

type previewSkillFileDiff struct {
	Skill          string `json:"skill"`
	Path           string `json:"path"`
	Change         string `json:"change"`
	OldMode        string `json:"old_mode,omitempty"`
	NewMode        string `json:"new_mode,omitempty"`
	Diff           string `json:"diff,omitempty"`
	ContentOmitted string `json:"content_omitted,omitempty"`
}

func previewSkillChanges(control *cloud.Client, preview *applyPreview, includeDiff bool) {
	current := map[string]string{}
	var currentData []byte
	if preview.Current != nil {
		currentData = preview.Current.data
		for _, skill := range preview.Current.Skills {
			current[skill.Name] = skill.Digest
		}
	}
	changed := map[string]bool{}
	for _, skill := range preview.Proposed.Skills {
		if current[skill.Name] != skill.Digest {
			changed[skill.Name] = true
		}
		delete(current, skill.Name)
	}
	for name := range current {
		changed[name] = true
	}
	if len(changed) == 0 {
		return
	}
	if !includeDiff {
		preview.Notes = append(preview.Notes, "Use --diff to include skill file changes.")
		return
	}
	before, err := previewSkillFiles(control, currentData, changed)
	if err != nil {
		preview.Notes = append(preview.Notes, fmt.Sprintf("Deployed skill file diffs are unavailable: %v", err))
		return
	}
	after, err := previewSkillFiles(control, preview.Proposed.data, changed)
	if err != nil {
		preview.Notes = append(preview.Notes, fmt.Sprintf("Proposed skill file diffs are unavailable: %v", err))
		return
	}
	for _, name := range slices.Sorted(maps.Keys(changed)) {
		preview.SkillDiffs = append(preview.SkillDiffs, diffSkillFiles(name, before[name], after[name])...)
	}
}

func previewSkillFiles(control *cloud.Client, data []byte, names map[string]bool) (map[string]map[string]cloud.SkillFile, error) {
	result := map[string]map[string]cloud.SkillFile{}
	if len(data) == 0 {
		return result, nil
	}
	hydrated, manifest, err := spec.HydrateApplyPackage(data, registrySkillFetcher(control))
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "telos-skill-diff-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	if _, err := spec.ExtractApplyPackage(hydrated, dir); err != nil {
		return nil, err
	}
	for name := range names {
		if _, exists := manifest.Skills[name]; !exists {
			continue
		}
		files, err := readSkillPublishFiles(filepath.Join(dir, "skills", name))
		if err != nil {
			return nil, err
		}
		result[name] = files
	}
	return result, nil
}

func diffSkillFiles(name string, before, after map[string]cloud.SkillFile) []previewSkillFileDiff {
	paths := append(slices.Collect(maps.Keys(before)), slices.Collect(maps.Keys(after))...)
	slices.Sort(paths)
	var changes []previewSkillFileDiff
	for _, path := range slices.Compact(paths) {
		old, hadOld := before[path]
		proposed, hasNew := after[path]
		if hadOld && hasNew && old.Mode == proposed.Mode && bytes.Equal(old.Data, proposed.Data) {
			continue
		}
		change := previewSkillFileDiff{Skill: name, Path: path, Change: "modified", OldMode: old.Mode, NewMode: proposed.Mode}
		if !hadOld {
			change.Change = "added"
		} else if !hasNew {
			change.Change = "removed"
		}
		switch {
		case !utf8.Valid(old.Data) || !utf8.Valid(proposed.Data) || bytes.IndexByte(old.Data, 0) >= 0 || bytes.IndexByte(proposed.Data, 0) >= 0:
			change.ContentOmitted = "binary content"
		case len(old.Data)+len(proposed.Data) > 256*1024:
			change.ContentOmitted = "content exceeds 256 KiB"
		default:
			oldPath := "deployed/skills/" + name + "/" + path
			newPath := "proposed/skills/" + name + "/" + path
			if !hadOld {
				oldPath = "/dev/null"
			}
			if !hasNew {
				newPath = "/dev/null"
			}
			change.Diff = string(internaldiff.Diff(oldPath, old.Data, newPath, proposed.Data))
		}
		changes = append(changes, change)
	}
	return changes
}

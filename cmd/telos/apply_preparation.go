package main

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/spec"
)

type preparedCloudApply struct {
	name        string
	local       *specPackage
	record      *cloud.PackageVersionRecord
	skills      []packageSkillPublication
	inference   *cloud.InferenceSelection
	proposed    applyPackageState
	current     *applyPackageState
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
		// Publishing unversioned content can select a different matching version.
		resolvedRefs := make(map[string]string, len(prepared.proposed.Skills))
		for _, lock := range prepared.proposed.Skills {
			resolvedRefs[lock.Name] = lock.Ref
		}
		for i, publication := range prepared.skills {
			if publication.skill != nil && publication.skill.version == "" {
				prepared.skills[i].ref = resolvedRefs[publication.name]
			}
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

type applyPackageState struct {
	Name            string           `json:"name,omitempty"`
	Version         string           `json:"version,omitempty"`
	IntervalSeconds *int             `json:"interval_seconds,omitempty"`
	Skills          []applySkillLock `json:"skills,omitempty"`

	Ref    string `json:"ref,omitempty"`
	Digest string `json:"digest,omitempty"`
	Spec   string `json:"spec,omitempty"`
	data   []byte
}

func registryApplyPackageState(pkg *pulledPackage) (applyPackageState, error) {
	markdown, manifest, err := verifiedPackageContents(pkg)
	if err != nil {
		return applyPackageState{}, err
	}
	return readApplyPackageState(pkg, markdown, manifest)
}

func prepareLocalApplyPackage(
	control *cloud.Client,
	pkg *specPackage,
	skills []packageSkillPublication,
) (applyPackageState, error) {
	scope, err := defaultPublishScope(control)
	if err != nil {
		return applyPackageState{}, err
	}
	markdown, manifest, err := spec.ApplyPackageSpec(pkg.bytes)
	if err != nil {
		return applyPackageState{}, err
	}
	refs := map[string]string{}
	unversioned := map[string]bool{}
	pending := false
	for _, publication := range skills {
		ref, err := applySkillRef(control, scope, publication, manifest.Skills[publication.name].Digest)
		if err != nil {
			return applyPackageState{}, err
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
			return applyPackageState{}, err
		}
	}
	ref := fmt.Sprintf("@%s/%s:%s", scope, pkg.name, pkg.version)
	existing, err := control.GetPackageVersion(scope, pkg.name, pkg.version)
	if err != nil && !cloud.IsStatus(err, 404) {
		return applyPackageState{}, fmt.Errorf("check package version %s: %w", ref, err)
	}
	if err == nil {
		if (resolved == nil || resolved.Digest != existing.Digest) && len(unversioned) > 0 {
			resolved, err = matchExistingApplyPackage(control, pkg, scope, refs, unversioned, existing.Digest)
			if err != nil {
				return applyPackageState{}, fmt.Errorf("check package version %s: %w", ref, err)
			}
		}
		if resolved == nil || resolved.Digest != existing.Digest {
			return applyPackageState{}, fmt.Errorf(
				"package version %s already exists; bump version in SPEC.md before changing the spec or its skills", ref,
			)
		}
	}
	digest := ""
	if resolved != nil {
		markdown, manifest, err = spec.ApplyPackageSpec(resolved.Bytes)
		if err != nil {
			return applyPackageState{}, err
		}
		digest = resolved.Digest
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
) (applyPackageState, error) {
	raw, _, ok := spec.ParseFrontmatter(string(markdown))
	if !ok {
		return applyPackageState{}, fmt.Errorf("spec has no valid YAML frontmatter")
	}
	state := applyPackageState{
		Ref: pkg.reference.ref, Digest: pkg.digest, Spec: string(markdown), data: pkg.data,
	}
	if name, ok := raw["name"].(string); ok {
		state.Name = strings.TrimSpace(name)
	}
	if version, ok := raw["version"].(string); ok {
		state.Version = strings.TrimSpace(version)
	}
	if interval, ok := raw["interval"]; ok {
		duration, err := time.ParseDuration(strings.TrimSpace(fmt.Sprint(interval)))
		if err != nil || duration < 0 || duration%time.Second != 0 {
			return applyPackageState{}, fmt.Errorf("invalid interval %q", interval)
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

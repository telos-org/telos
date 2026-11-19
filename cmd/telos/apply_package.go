package main

import (
	"fmt"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/spec"
)

type applyPackageState struct {
	applySpecState
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
	state, err := applySpecStateFromMarkdown(markdown, manifest)
	return applyPackageState{
		applySpecState: state,
		Ref:            pkg.reference.ref,
		Digest:         pkg.digest,
		Spec:           string(markdown),
		data:           pkg.data,
	}, err
}

func prepareLocalApplyPackage(
	control *cloud.Client,
	pkg *specPackage,
	skills []packageSkillPublication,
) (applyPackageState, bool, error) {
	scope, err := defaultPublishScope(control)
	if err != nil {
		return applyPackageState{}, false, err
	}
	markdown, manifest, err := spec.ApplyPackageSpec(pkg.bytes)
	if err != nil {
		return applyPackageState{}, false, err
	}
	data := pkg.bytes
	refs := map[string]string{}
	pending := false
	for _, publication := range skills {
		ref := publication.ref
		if publication.skill != nil {
			if publication.skill.version != "" {
				ref = fmt.Sprintf("@%s/%s:%s", scope, publication.name, publication.skill.version)
				record, err := control.GetSkillVersion(scope, publication.name, publication.skill.version)
				if err != nil && !cloud.IsStatus(err, 404) {
					return applyPackageState{}, false, fmt.Errorf("check skill version %s: %w", ref, err)
				}
				if err == nil && record.Digest != manifest.Skills[publication.name].Digest {
					return applyPackageState{}, false, fmt.Errorf(
						"skill version %s already exists with different content; bump version in SKILL.md", ref,
					)
				}
			} else {
				record, err := control.GetSkill(scope, publication.name)
				if err != nil && !cloud.IsStatus(err, 404) {
					return applyPackageState{}, false, err
				}
				if err == nil && record.Digest == manifest.Skills[publication.name].Digest {
					if record.Scope != scope || record.Name != publication.name {
						return applyPackageState{}, false, fmt.Errorf(
							"skill @%s/%s resolved to %s", scope, publication.name, record.Ref,
						)
					}
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
			return applyPackageState{}, false, err
		}
		markdown, manifest, err = spec.ApplyPackageSpec(resolved.Bytes)
		if err != nil {
			return applyPackageState{}, false, err
		}
		digest = resolved.Digest
		data = resolved.Bytes
	}
	state, err := applySpecStateFromMarkdown(markdown, manifest)
	if err != nil {
		return applyPackageState{}, false, err
	}
	for i := range state.Skills {
		state.Skills[i].Ref = refs[state.Skills[i].Name]
	}
	ref := fmt.Sprintf("@%s/%s:%s", scope, pkg.name, pkg.version)
	existing, err := control.GetPackageVersion(scope, pkg.name, pkg.version)
	if err != nil && !cloud.IsStatus(err, 404) {
		return applyPackageState{}, false, fmt.Errorf("check package version %s: %w", ref, err)
	}
	if err == nil && (pending || existing.Digest != digest) {
		return applyPackageState{}, false, fmt.Errorf(
			"package version %s already exists; bump version in SPEC.md before changing the spec or its skills", ref,
		)
	}
	return applyPackageState{
		applySpecState: state,
		Ref:            ref,
		Digest:         digest,
		Spec:           string(markdown),
		data:           data,
	}, pending, nil
}

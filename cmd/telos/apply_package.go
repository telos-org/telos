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
	if err == nil {
		matches := existing.Digest == digest
		if pending {
			matches, err = pendingPackageMatches(control, pkg, scope, refs, manifest, existing.Digest)
			if err != nil {
				return applyPackageState{}, false, fmt.Errorf("check package version %s: %w", ref, err)
			}
		}
		if !matches {
			return applyPackageState{}, false, fmt.Errorf(
				"package version %s already exists; bump version in SPEC.md before changing the spec or its skills", ref,
			)
		}
	}
	return applyPackageState{
		applySpecState: state,
		Ref:            ref,
		Digest:         digest,
		Spec:           string(markdown),
		data:           data,
	}, pending, nil
}

func pendingPackageMatches(
	control *cloud.Client,
	pkg *specPackage,
	scope string,
	refs map[string]string,
	proposed *spec.ApplyPackageManifest,
	existingDigest string,
) (bool, error) {
	data, err := control.DownloadPackageVersionBundle(scope, pkg.name, pkg.version)
	if err != nil {
		return false, err
	}
	_, existing, err := verifiedPackageContents(&pulledPackage{
		reference: packageReference{ref: fmt.Sprintf("@%s/%s:%s", scope, pkg.name, pkg.version)},
		digest:    existingDigest,
		data:      data,
	})
	if err != nil {
		return false, err
	}
	if existing.SchemaVersion != spec.ApplyPackageSchemaVersionExactRefs ||
		len(existing.Skills) != len(proposed.Skills) {
		return false, nil
	}
	comparisonRefs := make(map[string]string, len(refs))
	for name, lock := range proposed.Skills {
		previous, ok := existing.Skills[name]
		if !ok || previous.Digest != lock.Digest || previous.Starred != lock.Starred {
			return false, nil
		}
		ref := refs[name]
		if ref == "" {
			parsed, ok := spec.ParseRegistrySkillRef(previous.Ref)
			if !ok || parsed.Scope != scope {
				return false, nil
			}
			ref = previous.Ref
		}
		comparisonRefs[name] = ref
	}
	// Normalize imports with the existing locks for comparison only. Publication
	// still chooses the version, so an unresolved final digest stays unresolved.
	comparison, err := spec.BuildApplyPackageWithSkillRefs(pkg.compiled, comparisonRefs)
	if err != nil {
		return false, err
	}
	return comparison.Manifest.Spec.Digest == existing.Spec.Digest, nil
}

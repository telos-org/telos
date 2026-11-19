package main

import (
	"strings"

	"github.com/telos-org/telos/internal/cloud"
)

type preparedCloudApply struct {
	name      string
	local     *specPackage
	registry  *pulledPackage
	record    *cloud.PackageVersionRecord
	skills    []packageSkillPublication
	inference *cloud.InferenceSelection
}

func prepareCloudApply(control *cloud.Client, input, goalID, model, contextOverride string) (*preparedCloudApply, error) {
	prepared := &preparedCloudApply{}
	var err error
	if strings.HasPrefix(strings.TrimSpace(input), "@") {
		reference, err := parsePackageReference(input)
		if err != nil {
			return nil, err
		}
		prepared.registry, prepared.record, err = resolvePackageReference(control, reference)
		if err != nil {
			return nil, err
		}
		if _, err := verifiedPackageSpec(prepared.registry); err != nil {
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
		if err := refuseDuplicateGoal(control, "apply", input, prepared.name, contextOverride); err != nil {
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
	}
	return prepared, nil
}

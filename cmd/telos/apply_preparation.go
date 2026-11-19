package main

import (
	"fmt"
	"strings"

	"github.com/telos-org/telos/internal/cloud"
)

type preparedCloudApply struct {
	name        string
	local       *specPackage
	record      *cloud.PackageVersionRecord
	skills      []packageSkillPublication
	inference   *cloud.InferenceSelection
	proposed    applyPackageState
	current     *applyPackageState
	pending     bool
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
		prepared.proposed, prepared.pending, err = prepareLocalApplyPackage(control, prepared.local, prepared.skills)
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

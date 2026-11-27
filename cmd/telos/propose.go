package main

import (
	"fmt"
	"strings"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/spec"
)

func cmdPropose(args []string) {
	fs := newCommandFlagSet("propose", "telos propose SPEC.md -t TITLE [-d TEXT] [flags]"+
		"\n       telos propose [SPEC.md] --onto PROPOSAL [-m MSG] [-t TITLE] [-d TEXT] [flags]")
	title := fs.String("t", "", "Proposal title")
	description := fs.String("d", "", "Proposal description")
	message := fs.String("m", "", "Update message; required with SPEC.md and --onto")
	onto := fs.String("onto", "", "ID of the proposal to update, rename, or describe")
	jsonOut := fs.Bool("json", false, "JSON output")
	contextValue := cloudContextFlag(fs)
	parseFlags(fs, args)
	contextOverride, err := cloudContextOverride(fs, *contextValue)
	if err != nil {
		exitUsage("%v", err)
	}
	*title, *message = strings.TrimSpace(*title), strings.TrimSpace(*message)
	if fs.NArg() > 1 {
		requireArgCount(fs, 1, "at most one SPEC.md")
	}
	specArg := fs.Arg(0)
	if *onto == "" {
		switch {
		case specArg == "":
			requireArgCount(fs, 1, "one SPEC.md")
		case *title == "":
			exitUsage("-t TITLE is required to open a proposal")
		case flagNameSet(fs, "m"):
			exitUsage("-m names an update; use it with --onto PROPOSAL")
		}
		proposal, err := openProposal(cloudClient(contextOverride), specArg, *title, *description, contextOverride)
		if err != nil {
			exitWithError(err)
		}
		if *jsonOut {
			printJSON(proposal)
			return
		}
		fmt.Printf("opened proposal %s for %s\n", proposal.ID, proposal.GoalName)
		fmt.Printf("View: %s\n", proposal.URL)
		fmt.Printf("To update it: telos propose %s --onto %s -m \"MSG\"\n", shellArg(specArg), proposal.ID)
		return
	}

	edit := cloud.ProposalEdit{}
	if flagNameSet(fs, "t") {
		edit.Title = title
	}
	if flagNameSet(fs, "d") {
		edit.Description = description
	}
	switch {
	case !isProposalID(*onto):
		exitUsage("--onto takes a proposal ID, such as p_3f9a2b7c")
	case specArg != "" && *message == "":
		exitUsage("-m MSG is required to add an update")
	case specArg == "" && flagNameSet(fs, "m"):
		exitUsage("-m names an update; add SPEC.md")
	case specArg == "" && edit == (cloud.ProposalEdit{}):
		exitUsage("add SPEC.md with -m, or -t or -d, to change the proposal")
	}
	proposal, err := changeProposal(cloudClient(contextOverride), *onto, specArg, *message, edit, contextOverride)
	if err != nil {
		exitWithError(err)
	}
	if *jsonOut {
		printJSON(proposal)
		return
	}
	if specArg != "" || edit.Title == nil {
		fmt.Printf("updated %s\n", proposal.ID)
	}
	if edit.Title != nil {
		fmt.Printf("renamed %s to %q\n", proposal.ID, proposal.Title)
	}
}

// openProposal opens a proposal on the Goal the spec names.
func openProposal(control *cloud.Client, specArg, title, description, contextOverride string) (*cloud.Proposal, error) {
	pkg, err := packageSpec(specArg, contextOverride)
	if err != nil {
		return nil, err
	}
	goal, err := goalNamed(control, pkg.name)
	if err != nil {
		return nil, err
	}
	if goal == nil {
		return nil, fmt.Errorf("no Goal named %s in %s\nTo create it: telos apply %s", pkg.name, control.ContextName(), shellArg(specArg))
	}
	record, err := stageSpecPackage(control, pkg)
	if err != nil {
		return nil, err
	}
	proposal, err := control.CreateProposal(cloud.ProposalCreate{
		Goal:        goal.ID,
		Title:       title,
		Description: description,
		PackageRef:  record.Ref,
	})
	return proposal, ruleError(err, "")
}

// changeProposal adds an update from specArg, which may also rename the
// proposal or change its description, or with no spec applies edit alone.
func changeProposal(
	control *cloud.Client,
	id string,
	specArg string,
	message string,
	edit cloud.ProposalEdit,
	contextOverride string,
) (*cloud.Proposal, error) {
	proposal, err := control.GetProposal(id)
	if err != nil {
		return nil, err
	}
	if specArg != "" {
		pkg, err := packageSpec(specArg, contextOverride)
		if err != nil {
			return nil, err
		}
		record, err := stageSpecPackage(control, pkg)
		if err != nil {
			return nil, err
		}
		updated, err := control.UpdateProposal(id, cloud.ProposalUpdate{
			ExpectedUpdateNumber: proposal.UpdateNumber,
			Message:              message,
			PackageRef:           record.Ref,
			Title:                edit.Title,
			Description:          edit.Description,
		})
		if err != nil {
			return nil, proposalChangeError(err, proposal)
		}
		return updated, nil
	}
	edited, err := control.EditProposal(id, edit)
	if err != nil {
		return nil, proposalChangeError(err, proposal)
	}
	return edited, nil
}

func proposalChangeError(err error, proposal *cloud.Proposal) error {
	if apiCode(err) == "proposal_stale" {
		return staleProposalError(proposal, err)
	}
	return ruleError(err, "")
}

// staleProposalError says how to redo the change on the Goal's latest version.
func staleProposalError(proposal *cloud.Proposal, cause error) error {
	return &messageError{
		message: fmt.Sprintf(
			"%s is stale: %s changed since it was opened\n"+
				"Pull the latest version, redo your change, and propose again:\n"+
				"  telos pull %s\n"+
				"  telos propose %s/%s -t %q",
			proposal.ID, proposal.GoalName, proposal.Goal, proposal.GoalName, specFile, proposal.Title),
		cause: cause,
	}
}

// stageSpecPackage stores pkg and its local skills privately for a proposal.
// Staged versions come from the content, so proposing never publishes a
// Registry version, and the same spec version can be proposed again.
func stageSpecPackage(control *cloud.Client, pkg *specPackage) (*cloud.PackageVersionRecord, error) {
	skills, err := preparePackageSkills(control, pkg.compiled)
	if err != nil {
		return nil, err
	}
	refs := make(map[string]string, len(skills))
	for _, skill := range skills {
		if skill.ref != "" {
			refs[skill.name] = skill.ref
			continue
		}
		record, err := control.StageSkill(skill.skill.name, skill.skill.files)
		if err != nil {
			return nil, err
		}
		refs[skill.name] = record.Ref
	}
	staged, err := spec.BuildApplyPackageWithSkillRefs(pkg.compiled, refs)
	if err != nil {
		return nil, err
	}
	return control.StagePackage(pkg.name, staged.Bytes)
}

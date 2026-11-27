package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/telos-org/telos/internal/cloud"
)

// commentAction is something telos comment COMMENT ACTION does.
type commentAction struct {
	name, receipt string
	needsText     bool
	run           func(control *cloud.Client, id, text string) error
}

var commentActions = []commentAction{
	{"reply", "replied to", true, func(control *cloud.Client, id, text string) error {
		return control.ReplyToComment(id, text)
	}},
	{"edit", "edited", true, func(control *cloud.Client, id, text string) error {
		return control.EditComment(id, cloud.CommentEdit{Body: &text})
	}},
	{"resolve", "resolved", false, func(control *cloud.Client, id, _ string) error {
		return control.EditComment(id, cloud.CommentEdit{Resolved: new(true)})
	}},
	{"reopen", "reopened", false, func(control *cloud.Client, id, _ string) error {
		return control.EditComment(id, cloud.CommentEdit{Resolved: new(false)})
	}},
	{"delete", "deleted", false, func(control *cloud.Client, id, _ string) error {
		return control.DeleteComment(id)
	}},
}

// commentActionNames lists the actions as "reply, edit, …, or delete".
func commentActionNames() string {
	names := make([]string, len(commentActions))
	for index, action := range commentActions {
		names[index] = action.name
	}
	return strings.Join(names[:len(names)-1], ", ") + ", or " + names[len(names)-1]
}

// commentTarget is where a new comment goes: a line, a field, or, on a
// proposal only, the whole thing.
type commentTarget struct {
	file  string
	line  int
	old   bool
	field string
}

func cmdComment(args []string) {
	fs := newCommandFlagSet("comment", "telos comment GOAL|PROPOSAL -m TEXT [--file PATH --line N [--old] | --field NAME] [flags]"+
		"\n       telos comment COMMENT reply|edit -m TEXT [flags]"+
		"\n       telos comment COMMENT resolve|reopen|delete [flags]")
	text := fs.String("m", "", "Comment text")
	file := fs.String("file", specFile, "File to comment on, with --line")
	line := fs.Int("line", 0, "Line to comment on, as numbered by telos describe")
	old := fs.Bool("old", false, "Comment on a line the proposal removes, by its old number")
	field := fs.String("field", "", "Spec frontmatter field to comment on, such as interval or network")
	contextValue := cloudContextFlag(fs)
	parseFlags(fs, args)
	contextOverride, err := cloudContextOverride(fs, *contextValue)
	if err != nil {
		exitUsage("%v", err)
	}
	if fs.NArg() == 0 {
		requireArgCount(fs, 1, "a GOAL, PROPOSAL, or COMMENT")
	}
	id := fs.Arg(0)
	anchorSet := flagNamesSet(fs, "file", "line", "old")
	fieldSet := flagNameSet(fs, "field")
	*text = strings.TrimSpace(*text)

	if isCommentID(id) {
		requireArgCount(fs, 2, "COMMENT and one of "+commentActionNames())
		index := slices.IndexFunc(commentActions, func(action commentAction) bool { return action.name == fs.Arg(1) })
		if index < 0 {
			exitUsage("unknown action %q; use %s", fs.Arg(1), commentActionNames())
		}
		action := commentActions[index]
		switch {
		case anchorSet || fieldSet:
			exitUsage("--file, --line, --old, and --field apply only to a new comment")
		case action.needsText && *text == "":
			exitUsage("%s requires -m TEXT", action.name)
		case !action.needsText && flagNameSet(fs, "m"):
			exitUsage("%s doesn't take -m", action.name)
		}
		if err := action.run(cloudClient(contextOverride), id, *text); err != nil {
			exitWithError(ruleError(err, ""))
		}
		fmt.Printf("%s %s\n", action.receipt, id)
		return
	}

	requireArgCount(fs, 1, "one GOAL or PROPOSAL")
	switch {
	case *text == "":
		exitUsage("-m TEXT is required")
	case anchorSet && fieldSet:
		exitUsage("use --file and --line, or --field, not both")
	case anchorSet && *line == 0:
		exitUsage("--file and --old require --line N")
	case !isProposalID(id) && !anchorSet && !fieldSet:
		exitUsage("a comment on a Goal requires --file and --line, or --field; only proposals take comments on the whole thing")
	case !isProposalID(id) && *old:
		exitUsage("--old applies only to a proposal")
	}
	target := commentTarget{line: *line, old: *old, field: *field}
	if anchorSet {
		target.file = *file
	}
	comment, location, err := createComment(cloudClient(contextOverride), id, *text, target)
	if err != nil {
		exitWithError(ruleError(err, ""))
	}
	fmt.Printf("commented on %s (%s)\n", location, comment.ID)
}

// createComment comments on a Goal or a proposal and returns what the comment
// is on, such as "SPEC.md line 14".
func createComment(control *cloud.Client, id, text string, target commentTarget) (*cloud.Comment, string, error) {
	body := cloud.CommentCreate{Body: text}
	var texts fileTexts
	var err error
	if isProposalID(id) {
		proposal, err := control.GetProposal(id)
		if err != nil {
			return nil, "", err
		}
		var paths []string
		if target.file != "" && !target.old {
			paths = []string{target.file}
		}
		if texts, err = proposalCommentTexts(control, proposal, paths); err != nil {
			return nil, "", err
		}
		body.Proposal = id
		body.UpdateNumber = proposal.UpdateNumber
	} else {
		body.Goal = id
		if target.file != "" {
			if texts, err = goalTexts(control, id, []string{target.file}); err != nil {
				return nil, "", err
			}
		}
	}
	location := id
	switch {
	case target.field != "":
		// Cloud quotes the field's value itself.
		body.Anchor = &cloud.CommentAnchor{Path: specFile, Field: target.field}
		location = specFile + " " + target.field
	case target.file != "":
		if body.Anchor, err = sideLineAnchor(texts, target.file, target.line, target.old); err != nil {
			return nil, "", err
		}
		location = lineLocation(target.file, target.line, body.Anchor.Side)
	}
	comment, err := control.CreateComment(body)
	return comment, location, err
}

// cloudClient returns the Cloud client for a context, or exits when signed
// out.
func cloudClient(contextOverride string) *cloud.Client {
	if err := requireCloudLogin(); err != nil {
		exitWithError(err)
	}
	control, err := cloud.ControlClientForContext(contextOverride)
	if err != nil {
		exitWithError(err)
	}
	return control
}

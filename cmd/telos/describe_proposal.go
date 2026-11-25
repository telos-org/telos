package main

import (
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	internaldiff "github.com/rogpeppe/go-internal/diff"
	"github.com/telos-org/telos/internal/cloud"
)

// describeProposal shows a proposal's status, reviews, comments, and changes.
func describeProposal(id string, jsonOut bool, contextOverride string) {
	control := cloudClient(contextOverride)
	proposal, err := control.GetProposal(id)
	if err != nil {
		exitWithError(err)
	}
	history, err := control.ProposalHistory(id)
	if err != nil {
		exitWithError(err)
	}
	reviews := currentReviews(history.Reviews, proposal.UpdateNumber)
	if jsonOut {
		printJSON(map[string]any{"proposal": proposal, "reviews": reviews})
		return
	}
	printProposal(os.Stdout, proposal, reviews)
}

// currentReviews returns each reviewer's standing verdict on the latest
// update, in the order they first reviewed it.
func currentReviews(events []cloud.ProposalReview, updateNumber int) []cloud.ProposalReview {
	var reviews []cloud.ProposalReview
	for _, event := range events {
		if event.UpdateNumber != updateNumber {
			continue
		}
		reviews = slices.DeleteFunc(reviews, func(review cloud.ProposalReview) bool {
			return review.Reviewer.Subject == event.Reviewer.Subject
		})
		if event.Verdict != nil {
			reviews = append(reviews, event)
		}
	}
	return reviews
}

func printProposal(out io.Writer, proposal *cloud.Proposal, reviews []cloud.ProposalReview) {
	printSummaryField(out, "Title", proposal.Title)
	printSummaryField(out, "Status", proposalStatus(proposal.Status))
	printSummaryField(out, "Goal", fmt.Sprintf("%s (%s)", proposal.GoalName, proposal.Goal))
	if label, ok := map[string]string{"redeploy": "Redeploys", "restore": "Restores"}[proposal.Action]; ok {
		printSummaryField(out, label, proposal.SourceRevisionID)
	}
	printSummaryField(out, "Updated", ago(proposal.UpdatedAt))
	if len(reviews) > 0 {
		fmt.Fprintln(out, "\nReviews")
		for _, review := range reviews {
			fmt.Fprintf(out, "  %s  %s\n", review.Reviewer.Label(), *review.Verdict)
		}
	}
	texts := proposalTexts(proposal)
	// Files shown without text: binary, or too large.
	omitted := map[string]string{}
	if proposal.Preview != nil {
		for _, file := range proposal.Preview.Files {
			if file.OmittedReason != "" {
				omitted[file.Path] = strings.ReplaceAll(file.OmittedReason, "_", " ")
			}
		}
	}
	paths := slices.Collect(maps.Keys(texts.new))
	for path := range texts.old {
		if _, ok := texts.new[path]; !ok {
			paths = append(paths, path)
		}
	}
	for path := range omitted {
		if !slices.Contains(paths, path) {
			paths = append(paths, path)
		}
	}
	// SPEC.md sorts before skills/…, so the spec leads, as it does in Web.
	slices.Sort(paths)
	for _, path := range paths {
		if reason, ok := omitted[path]; ok {
			fmt.Fprintf(out, "\n%s\n  Not shown: %s\n", path, reason)
			continue
		}
		printNumberedDiff(out, path, texts.old[path], texts.new[path])
	}
	if proposal.Preview != nil && len(proposal.Preview.SkillWarnings) > 0 {
		fmt.Fprintln(out, "\nSkills not compared")
		for _, warning := range proposal.Preview.SkillWarnings {
			fmt.Fprintf(out, "  %s  %s\n", warning.Name, strings.ReplaceAll(warning.Reason, "_", " "))
		}
	}
}

// diffLine is one line of a file's changes with its old and new line
// numbers, 0 on the side it isn't on, and its mark: ' ', '-', or '+'. A zero
// diffLine separates hunks.
type diffLine struct {
	old, new int
	mark     byte
	text     string
}

// diffLines returns what changed between old and new, with context.
func diffLines(old, new string) []diffLine {
	var lines []diffLine
	oldLine, newLine := 0, 0
	for _, line := range strings.Split(string(internaldiff.Diff("old", []byte(old), "new", []byte(new))), "\n") {
		if strings.HasPrefix(line, "@@") {
			if oldLine+newLine > 0 {
				lines = append(lines, diffLine{})
			}
			var oldCount, newCount int
			_, _ = fmt.Sscanf(line, "@@ -%d,%d +%d,%d @@", &oldLine, &oldCount, &newLine, &newCount)
			continue
		}
		if oldLine+newLine == 0 || line == "" || !strings.ContainsRune(" -+", rune(line[0])) {
			continue
		}
		row := diffLine{mark: line[0], text: line[1:]}
		if row.mark != '+' {
			row.old = oldLine
			oldLine++
		}
		if row.mark != '-' {
			row.new = newLine
			newLine++
		}
		lines = append(lines, row)
	}
	return lines
}

// printNumberedDiff prints what changed in one file, each line with its old
// and new line numbers, the numbers telos comment --line takes.
func printNumberedDiff(out io.Writer, path, old, new string) {
	lines := diffLines(old, new)
	if len(lines) == 0 {
		return
	}
	width := len(strconv.Itoa(max(strings.Count(old, "\n"), strings.Count(new, "\n")) + 1))
	number := func(n int) string {
		if n == 0 {
			return ""
		}
		return strconv.Itoa(n)
	}
	fmt.Fprintf(out, "\n%s\n", path)
	for _, line := range lines {
		if line.mark == 0 {
			fmt.Fprintf(out, "  %*s\n", width, "...")
			continue
		}
		fmt.Fprintf(out, "  %*s  %*s  %c %s\n", width, number(line.old), width, number(line.new), line.mark, line.text)
	}
}

// proposalStatus capitalizes an API status for people: pending is Pending.
func proposalStatus(status string) string {
	if status == "" {
		return ""
	}
	return strings.ToUpper(status[:1]) + status[1:]
}

// ago says how long ago a Cloud timestamp was, such as "2m ago".
func ago(timestamp string) string {
	at, err := time.Parse(time.RFC3339, timestamp)
	if err != nil {
		return ""
	}
	return formatRelative(time.Since(at)) + " ago"
}

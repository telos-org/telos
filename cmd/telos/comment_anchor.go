package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/spec"
)

const (
	specFile = "SPEC.md"
	// anchorContext is how many characters around a quoted line a comment
	// keeps, so Cloud and Web can find the line again after edits.
	anchorContext = 64
	// maxQuote is Cloud's limit on a comment's quote.
	maxQuote = 2000
)

// fileTexts holds each file's text by package path on both sides of a
// change. A Goal's current files are its new side.
type fileTexts struct {
	old, new map[string]string
}

func (t fileTexts) side(side string) map[string]string {
	if side == "old" {
		return t.old
	}
	return t.new
}

// proposalTexts returns the files a proposal changes, and its spec, as they
// were and as it proposes them.
func proposalTexts(proposal *cloud.Proposal) fileTexts {
	texts := fileTexts{old: map[string]string{}, new: map[string]string{}}
	preview := proposal.Preview
	if preview == nil {
		return texts
	}
	texts.new[specFile] = preview.ProposedSpec
	if preview.BaseSpec != nil {
		texts.old[specFile] = *preview.BaseSpec
	}
	for _, file := range preview.Files {
		if file.BaseText != nil {
			texts.old[file.Path] = *file.BaseText
		}
		if file.ProposedText != nil {
			texts.new[file.Path] = *file.ProposedText
		}
	}
	return texts
}

// proposalCommentTexts is proposalTexts plus the text of the files at paths
// that the proposal doesn't change, read from its bundle with only the skills
// those files are in.
func proposalCommentTexts(control *cloud.Client, proposal *cloud.Proposal, paths []string) (fileTexts, error) {
	texts := proposalTexts(proposal)
	var missing []string
	for _, path := range paths {
		if _, ok := texts.new[path]; !ok {
			missing = append(missing, path)
		}
	}
	if len(missing) == 0 {
		return texts, nil
	}
	pkg, err := proposalPackage(control, proposal.ID)
	if err != nil {
		return texts, err
	}
	files, err := packageTexts(pkg, proposalSkillFetcher(control, proposal.ID), missing)
	if err != nil {
		return texts, err
	}
	for _, path := range missing {
		if text, ok := files[path]; ok {
			texts.new[path] = text
		}
	}
	return texts, nil
}

// commentPaths returns the files that line comments quote on the new side.
func commentPaths(comments []cloud.Comment) []string {
	var paths []string
	for _, comment := range comments {
		if anchor := comment.Anchor; anchor != nil && anchor.Field == "" && anchor.Side != "old" {
			paths = append(paths, anchor.Path)
		}
	}
	return paths
}

// goalTexts returns the text of each file at paths in a Goal's current
// revision, and of its other files that need no download.
func goalTexts(control *cloud.Client, goalID string, paths []string) (fileTexts, error) {
	if len(paths) == 0 {
		return fileTexts{new: map[string]string{}}, nil
	}
	pkg, err := packageForSession(control, goalID)
	if err != nil {
		return fileTexts{}, err
	}
	files, err := packageTexts(pkg, registrySkillFetcher(control), paths)
	return fileTexts{new: files}, err
}

// packageTexts verifies pkg and returns its files' text, fetching only the
// registry skills that paths are in.
func packageTexts(pkg *pulledPackage, fetch spec.ApplyPackageSkillFetcher, paths []string) (map[string]string, error) {
	if _, err := verifiedPackageSpec(pkg); err != nil {
		return nil, err
	}
	var skills []string
	for _, path := range paths {
		if rest, ok := strings.CutPrefix(path, "skills/"); ok {
			skill, _, _ := strings.Cut(rest, "/")
			skills = append(skills, skill)
		}
	}
	files, err := spec.ApplyPackageFiles(pkg.data, fetch, skills)
	if err != nil {
		return nil, err
	}
	texts := make(map[string]string, len(files))
	for path, data := range files {
		texts[path] = string(data)
	}
	return texts, nil
}

// sideLineAnchor quotes a line of file on the new side of texts, or with old
// on the old side, where it must be a line the change removes.
func sideLineAnchor(texts fileTexts, file string, line int, old bool) (*cloud.CommentAnchor, error) {
	side := "new"
	if old {
		side = "old"
		removed := slices.ContainsFunc(diffLines(texts.old[file], texts.new[file]), func(row diffLine) bool {
			return row.mark == '-' && row.old == line
		})
		if !removed {
			return nil, fmt.Errorf("%s line %d isn't a line the proposal removes; --old takes a removed line's number from telos describe", file, line)
		}
	}
	anchor, err := lineAnchor(texts.side(side), file, line)
	if err != nil {
		return nil, err
	}
	anchor.Side = side
	return anchor, nil
}

// lineAnchor quotes line (counting from 1) of file, with the text around it.
// A line longer than Cloud's quote limit is quoted from its start.
func lineAnchor(files map[string]string, file string, line int) (*cloud.CommentAnchor, error) {
	text, ok := files[file]
	if !ok {
		return nil, fmt.Errorf("%s isn't there to comment on", file)
	}
	lines := strings.Split(text, "\n")
	if line < 1 || line > len(lines) {
		return nil, fmt.Errorf("%s has no line %d", file, line)
	}
	quote := firstRunes(strings.TrimSuffix(lines[line-1], "\r"), maxQuote)
	if strings.TrimSpace(quote) == "" {
		return nil, fmt.Errorf("%s line %d is blank; comment on a line with text", file, line)
	}
	start := 0
	for _, before := range lines[:line-1] {
		start += len(before) + 1
	}
	return &cloud.CommentAnchor{
		Path:   file,
		Quote:  quote,
		Prefix: lastRunes(text[:start], anchorContext),
		Suffix: firstRunes(text[start+len(quote):], anchorContext),
	}, nil
}

// commentLocation names what a comment is on, such as "SPEC.md line 14", by
// finding its quote in texts; "" for a comment on a whole proposal.
func commentLocation(anchor *cloud.CommentAnchor, texts fileTexts) string {
	if anchor == nil {
		return ""
	}
	if anchor.Field != "" {
		return anchor.Path + " " + anchor.Field
	}
	return lineLocation(anchor.Path, lineOf(texts.side(anchor.Side)[anchor.Path], anchor), anchor.Side)
}

func lineLocation(file string, line int, side string) string {
	if line == 0 {
		return file
	}
	location := fmt.Sprintf("%s line %d", file, line)
	if side == "old" {
		location += " (old)"
	}
	return location
}

// lineOf returns the line, counting from 1, where anchor's quote starts in
// text, preferring the place its surrounding text matches; 0 when the quote
// is gone.
func lineOf(text string, anchor *cloud.CommentAnchor) int {
	at := strings.Index(text, anchor.Prefix+anchor.Quote+anchor.Suffix)
	if at >= 0 {
		at += len(anchor.Prefix)
	} else {
		at = strings.Index(text, anchor.Quote)
	}
	if at < 0 {
		return 0
	}
	return strings.Count(text[:at], "\n") + 1
}

func firstRunes(text string, n int) string {
	runes := []rune(text)
	return string(runes[:min(n, len(runes))])
}

func lastRunes(text string, n int) string {
	runes := []rune(text)
	return string(runes[max(len(runes)-n, 0):])
}

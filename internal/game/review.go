package game

import (
	"fmt"

	"github.com/telos-org/telos/internal/protocol"
)

// ReviewCriterion is one parsed row of the verifier <review> CSV.
type ReviewCriterion = protocol.ReviewCriterion

// defaultFindingScoreThreshold is the fraction of a criterion's max score below
// which it is treated as an open finding. Review scores are emitted as `x.y/10`,
// so 0.7 flags any criterion scoring under 7/10.
const defaultFindingScoreThreshold = 0.7

// scoreFraction returns Score/Max in [0,1]; 0 when Max is non-positive.
func scoreFraction(c ReviewCriterion) float64 {
	if c.Max <= 0 {
		return 0
	}
	return c.Score / c.Max
}

// ParseReviewBlock extracts scored criteria from the last <review> block in
// text. The verifier emits the full current rubric each review turn, so the last
// block is authoritative. ok is true only when at least one well-formed
// `criteria,score` row parsed, letting callers fall back to the legacy keyword
// scan on a missing or malformed block rather than silently dropping findings.
func ParseReviewBlock(text string) ([]ReviewCriterion, bool) {
	return protocol.ParseReviewBlock(text)
}

// findingsFromReview returns one finding string per criterion scoring below the
// threshold, formatted "criteria (x.y/10)". Empty when every criterion passes.
func findingsFromReview(criteria []ReviewCriterion, threshold float64) []string {
	var findings []string
	for _, c := range criteria {
		if scoreFraction(c) < threshold {
			findings = appendUnique(findings, compactLedgerText(fmt.Sprintf("%s (%s)", c.Name, c.Raw), 400))
		}
	}
	return findings
}

// reviewFindings parses the verifier <review> CSV and returns score-driven
// findings. ok mirrors ParseReviewBlock: false when there is no parseable review
// block, so the caller falls back to the legacy keyword scan.
func reviewFindings(text string) ([]string, bool) {
	criteria, ok := ParseReviewBlock(text)
	if !ok {
		return nil, false
	}
	return findingsFromReview(criteria, defaultFindingScoreThreshold), true
}

// Finding is one entry of the pvg-mode verifier <findings> block.
type Finding = protocol.Finding

// ParseFindingsBlock extracts entries from the last <findings> block in text.
// Each non-empty line is `severity | description`; a line with no pipe is taken
// as a blocker. ok is true whenever a <findings> block is present, even if
// empty, so a verifier that concedes with an empty block is distinguished from
// one that emitted no block at all (which falls back to the review/keyword path).
func ParseFindingsBlock(text string) ([]Finding, bool) {
	return protocol.ParseFindingsBlock(text)
}

// findingsBlockFindings formats the structured <findings> block into ledger
// finding strings. ok mirrors ParseFindingsBlock: false when no block is present
// so the caller falls back to the review CSV and then the keyword scan.
func findingsBlockFindings(text string) ([]string, bool) {
	parsed, ok := ParseFindingsBlock(text)
	if !ok {
		return nil, false
	}
	var findings []string
	for _, f := range parsed {
		findings = appendUnique(findings, compactLedgerText(fmt.Sprintf("%s: %s", f.Severity, f.Text), 400))
	}
	return findings, true
}

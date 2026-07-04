// Package protocol owns the text tags exchanged between Telos agents.
package protocol

import (
	"regexp"
	"strconv"
	"strings"
)

type Role = string

const (
	RoleProver   Role = "prover"
	RoleVerifier Role = "verifier"

	ProtocolModePVG    = "pvg"
	ProtocolModeReview = "review"
)

type AgentStatus string

const (
	StatusContinue AgentStatus = "CONTINUE"
	StatusConcede  AgentStatus = "CONCEDE"
)

type Tag string

const (
	TagStatus         Tag = "status"
	TagProgressUpdate Tag = "progress_update"
	TagReview         Tag = "review"
	TagSummary        Tag = "summary"
	TagFindings       Tag = "findings"
)

var (
	statusBlockRE         = regexp.MustCompile(`(?is)<status\b[^>]*>(.*?)</status>`)
	finalStatusRE         = regexp.MustCompile(`(?is)(?:^|\n)\s*<status\b[^>]*>\s*(\w+)\s*</status>\s*$`)
	progressUpdateBlockRE = regexp.MustCompile(`(?is)<progress_update\b[^>]*>(.*?)</progress_update>`)
	finalProgressUpdateRE = regexp.MustCompile(`(?is)(?:^|\n)\s*<progress_update\b[^>]*>.*?</progress_update>\s*$`)
	reviewBlockRE         = regexp.MustCompile(`(?is)<review\b[^>]*>(.*?)</review>`)
	summaryBlockRE        = regexp.MustCompile(`(?is)<summary\b[^>]*>(.*?)</summary>`)
	findingsBlockRE       = regexp.MustCompile(`(?is)<findings\b[^>]*>(.*?)</findings>`)
	anyKnownTagRE         = regexp.MustCompile(`(?is)<(?:/?)(?:findings|review|summary|progress_update|status)\b[^>]*>`)
	reviewScoreCellRE     = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*/\s*(\d+(?:\.\d+)?)`)
)

func blockRE(tag Tag) *regexp.Regexp {
	switch tag {
	case TagStatus:
		return statusBlockRE
	case TagProgressUpdate:
		return progressUpdateBlockRE
	case TagReview:
		return reviewBlockRE
	case TagSummary:
		return summaryBlockRE
	case TagFindings:
		return findingsBlockRE
	default:
		return nil
	}
}

func CountBlocks(tag Tag, text string) int {
	re := blockRE(tag)
	if re == nil {
		return 0
	}
	return len(re.FindAllString(text, -1))
}

func LastBlock(tag Tag, text string) (string, bool) {
	re := blockRE(tag)
	if re == nil {
		return "", false
	}
	matches := re.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 || len(matches[len(matches)-1]) < 2 {
		return "", false
	}
	return strings.TrimSpace(matches[len(matches)-1][1]), true
}

func HasAnyKnownTag(text string) bool {
	return anyKnownTagRE.MatchString(text)
}

func HasFinalProgressUpdate(text string) bool {
	return finalProgressUpdateRE.MatchString(strings.TrimRight(text, " \t\n\r"))
}

func LastProgressUpdate(text string) string {
	body, ok := LastBlock(TagProgressUpdate, text)
	if !ok {
		return ""
	}
	return body
}

func LastSummaryOrReview(text string) string {
	if body, ok := LastBlock(TagSummary, text); ok {
		return body
	}
	if body, ok := LastBlock(TagReview, text); ok {
		return body
	}
	return ""
}

func StripFinalStatus(text string) string {
	return strings.TrimRight(finalStatusRE.ReplaceAllString(text, ""), " \t\n\r")
}

func ExtractStatus(text string) AgentStatus {
	status, ok := ParseFinalStatus(text)
	if !ok {
		return StatusContinue
	}
	return status
}

func ParseFinalStatus(text string) (AgentStatus, bool) {
	matches := finalStatusRE.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return StatusContinue, false
	}
	last := matches[len(matches)-1][1]
	switch strings.ToUpper(strings.TrimSpace(last)) {
	case string(StatusConcede):
		return StatusConcede, true
	case string(StatusContinue):
		return StatusContinue, true
	default:
		return StatusContinue, false
	}
}

// ReviewCriterion is one parsed row of the verifier <review> CSV.
type ReviewCriterion struct {
	Name  string
	Score float64
	Max   float64
	Raw   string
}

func ParseReviewBlock(text string) ([]ReviewCriterion, bool) {
	body, ok := LastBlock(TagReview, text)
	if !ok {
		return nil, false
	}
	var criteria []ReviewCriterion
	for _, line := range strings.Split(body, "\n") {
		line = strings.Trim(strings.TrimSpace(line), "|")
		if line == "" || strings.HasPrefix(strings.ToLower(line), "criteria,score") {
			continue
		}
		comma := strings.LastIndex(line, ",")
		if comma < 0 {
			continue
		}
		name := strings.TrimSpace(line[:comma])
		cell := strings.TrimSpace(line[comma+1:])
		m := reviewScoreCellRE.FindStringSubmatch(cell)
		if name == "" || m == nil {
			continue
		}
		score, err1 := strconv.ParseFloat(m[1], 64)
		max, err2 := strconv.ParseFloat(m[2], 64)
		if err1 != nil || err2 != nil {
			continue
		}
		criteria = append(criteria, ReviewCriterion{Name: name, Score: score, Max: max, Raw: cell})
	}
	return criteria, len(criteria) > 0
}

// Finding is one entry of the pvg-mode verifier <findings> block.
type Finding struct {
	Severity string
	Text     string
}

func ParseFindingsBlock(text string) ([]Finding, bool) {
	body, ok := LastBlock(TagFindings, text)
	if !ok {
		return nil, false
	}
	var findings []Finding
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		severity, desc := "blocker", line
		if pipe := strings.Index(line, "|"); pipe >= 0 {
			if sev := strings.ToLower(strings.TrimSpace(line[:pipe])); sev == "warn" || sev == "blocker" {
				severity = sev
			}
			desc = strings.TrimSpace(line[pipe+1:])
		}
		if desc == "" {
			continue
		}
		findings = append(findings, Finding{Severity: severity, Text: desc})
	}
	return findings, true
}

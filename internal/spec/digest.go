package spec

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/telos-org/telos/internal/platform"
)

// -- Turn context digest -----------------------------------------------------

var progressUpdateRE = regexp.MustCompile(`(?is)<progress_update\b[^>]*>(.*?)</progress_update>`)
var statusTagRE = regexp.MustCompile(`(?is)<status>(.*?)</status>`)
var reviewBlockRE = regexp.MustCompile(`(?is)<review\b[^>]*>(.*?)</review>`)
var summaryBlockRE = regexp.MustCompile(`(?is)<summary\b[^>]*>(.*?)</summary>`)

type digestContext struct {
	SpecName string
	Role     Role
}

func renderTurnContextDigest(transcriptPath string, workspace platform.WorkspaceSnapshot, contexts ...digestContext) string {
	var lines []string
	lines = append(lines, "## Current State Digest", "")
	if len(contexts) > 0 {
		ctx := contexts[0]
		if strings.TrimSpace(ctx.SpecName) != "" {
			lines = append(lines, "- Spec: `"+strings.TrimSpace(ctx.SpecName)+"`")
		}
		if strings.TrimSpace(ctx.Role) != "" {
			lines = append(lines, "- Role: `"+strings.TrimSpace(ctx.Role)+"`")
		}
	}
	ledgerPath := ""
	if transcriptPath != "" {
		ledgerPath = filepath.Join(filepath.Dir(transcriptPath), "objective-ledger.json")
		lines = append(lines, fmt.Sprintf("- Full transcript: `%s`", transcriptPath))
		lines = append(lines, fmt.Sprintf("- Evidence log: `%s`", filepath.Join(filepath.Dir(transcriptPath), "evidence.jsonl")))
		lines = append(lines, fmt.Sprintf("- Objective ledger: `%s`", ledgerPath))
	}
	lines = append(lines, renderWorkspaceDigest(workspace)...)

	ledger, ledgerOK := readDigestLedger(ledgerPath)
	if ledgerOK {
		if ledger.Objective != "" {
			lines = append(lines, "- Current objective: "+oneLine(ledger.Objective))
		}
		if ledger.State != "" {
			lines = append(lines, "- Objective state: `"+ledger.State+"`")
		}
		if ledger.LastImplementation != "" {
			lines = append(lines, "- Last implementation: "+oneLine(ledger.LastImplementation))
		}
		if ledger.LastEvaluation != "" {
			lines = append(lines, "- Last evaluation: "+oneLine(ledger.LastEvaluation))
		}
		if len(ledger.OpenFindings) > 0 {
			lines = append(lines, "- Open findings:")
			for _, finding := range limitStrings(ledger.OpenFindings, 8) {
				lines = append(lines, "  - "+oneLine(finding))
			}
		} else {
			lines = append(lines, "- Open findings: none recorded in the objective ledger.")
		}
		if recentErrors := recentLedgerErrors(ledger.Turns, 5); len(recentErrors) > 0 {
			lines = append(lines, "- Recent runtime errors:")
			for _, errText := range recentErrors {
				lines = append(lines, "  - "+oneLine(errText))
			}
		}
	}
	body := readDigestTranscript(transcriptPath)
	if body == "" {
		if !ledgerOK {
			lines = append(lines, "- Transcript state: no prior turn content found.")
		}
		return strings.Join(lines, "\n")
	}
	if !ledgerOK {
		lines = append(lines, "- Transcript state: parsed from append-only transcript.")
		return strings.Join(renderTranscriptFallbackDigest(lines, body), "\n")
	}
	if last := lastMatch(statusTagRE, body); last != "" {
		lines = append(lines, "- Last verifier status: "+strings.ToUpper(strings.TrimSpace(last)))
	}
	return strings.Join(lines, "\n")
}

type digestLedger struct {
	Objective          string             `json:"objective"`
	State              string             `json:"state"`
	LastImplementation string             `json:"last_implementation"`
	LastEvaluation     string             `json:"last_evaluation"`
	OpenFindings       []string           `json:"open_findings"`
	Turns              []digestLedgerTurn `json:"turns"`
}

type digestLedgerTurn struct {
	RoundNum int    `json:"round_num"`
	Role     string `json:"role"`
	Error    string `json:"error"`
}

func readDigestLedger(path string) (digestLedger, bool) {
	if strings.TrimSpace(path) == "" {
		return digestLedger{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return digestLedger{}, false
	}
	var ledger digestLedger
	if err := json.Unmarshal(data, &ledger); err != nil {
		return digestLedger{}, false
	}
	return ledger, true
}

func renderWorkspaceDigest(workspace platform.WorkspaceSnapshot) []string {
	if len(workspace.FileList) == 0 && len(workspace.GitStatus) == 0 && len(workspace.DiffStat) == 0 {
		return nil
	}
	lines := []string{"- Workspace summary is included below; inspect files or diffs directly before relying on it."}
	if len(workspace.GitStatus) > 0 {
		lines = append(lines, fmt.Sprintf("- Workspace status: %d changed/untracked entries.", len(workspace.GitStatus)))
		for _, line := range limitStrings(workspace.GitStatus, 6) {
			lines = append(lines, "  - "+oneLine(line))
		}
	}
	if len(workspace.DiffStat) > 0 {
		lines = append(lines, "- Diff stat:")
		for _, line := range limitStrings(workspace.DiffStat, 6) {
			lines = append(lines, "  - "+oneLine(line))
		}
	}
	return lines
}

func recentLedgerErrors(turns []digestLedgerTurn, limit int) []string {
	var out []string
	for i := len(turns) - 1; i >= 0 && len(out) < limit; i-- {
		errText := strings.TrimSpace(turns[i].Error)
		if errText == "" {
			continue
		}
		prefix := fmt.Sprintf("round %d %s", turns[i].RoundNum, turns[i].Role)
		out = append(out, prefix+": "+errText)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func limitStrings(in []string, limit int) []string {
	if limit <= 0 || len(in) <= limit {
		return in
	}
	return in[:limit]
}

func renderTranscriptFallbackDigest(lines []string, body string) []string {
	if last := lastMatch(progressUpdateRE, body); last != "" {
		lines = append(lines, "- Last implementation: "+oneLine(last))
	}
	if last := lastMatch(reviewBlockRE, body); last != "" {
		lines = append(lines, "- Last review: "+oneLine(last))
	} else if last := lastMatch(summaryBlockRE, body); last != "" {
		lines = append(lines, "- Last review summary: "+oneLine(last))
	}
	if last := lastMatch(statusTagRE, body); last != "" {
		lines = append(lines, "- Last verifier status: "+strings.ToUpper(strings.TrimSpace(last)))
	}
	findings := recentLinesContaining(body, []string{"FAIL", "finding", "blocked", "CONTINUE"}, 5)
	if len(findings) > 0 {
		lines = append(lines, "- Recent possible open findings:")
		for _, finding := range findings {
			lines = append(lines, "  - "+oneLine(finding))
		}
	}
	errors := recentLinesContaining(body, []string{"provider_", "tool_", "agent_", "runtime_budget_exhausted", "local_timeout", "local_interrupted"}, 5)
	if len(errors) > 0 {
		lines = append(lines, "- Recent runtime errors:")
		for _, errLine := range errors {
			lines = append(lines, "  - "+oneLine(errLine))
		}
	}
	return lines
}

func readDigestTranscript(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	const maxDigestBytes = 128 << 10
	if len(data) > maxDigestBytes {
		data = data[len(data)-maxDigestBytes:]
	}
	return string(data)
}

func lastMatch(re *regexp.Regexp, text string) string {
	matches := re.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 || len(matches[len(matches)-1]) < 2 {
		return ""
	}
	return strings.TrimSpace(matches[len(matches)-1][1])
}

func recentLinesContaining(text string, needles []string, limit int) []string {
	source := strings.Split(text, "\n")
	var out []string
	for i := len(source) - 1; i >= 0 && len(out) < limit; i-- {
		line := strings.TrimSpace(source[i])
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		for _, needle := range needles {
			if strings.Contains(lower, strings.ToLower(needle)) {
				out = append(out, line)
				break
			}
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func oneLine(text string) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	const max = 240
	if len(text) > max {
		return text[:max] + "..."
	}
	return text
}

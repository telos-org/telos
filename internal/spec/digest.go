package spec

import (
	"fmt"
	"path/filepath"
	"strings"

	objectiveledger "github.com/telos-org/telos/internal/ledger"
	"github.com/telos-org/telos/internal/platform"
	"github.com/telos-org/telos/internal/protocol"
)

// -- Turn context digest -----------------------------------------------------

type TurnContextDigest struct {
	TranscriptPath string
	EvidencePath   string
	LedgerPath     string
	Ledger         *objectiveledger.ObjectiveLedger
	Transcript     string
}

func (d TurnContextDigest) withTranscriptPath(transcriptPath string) TurnContextDigest {
	if strings.TrimSpace(d.TranscriptPath) == "" {
		d.TranscriptPath = transcriptPath
	}
	if d.TranscriptPath != "" {
		dir := filepath.Dir(d.TranscriptPath)
		if strings.TrimSpace(d.EvidencePath) == "" {
			d.EvidencePath = filepath.Join(dir, "evidence.jsonl")
		}
		if strings.TrimSpace(d.LedgerPath) == "" {
			d.LedgerPath = filepath.Join(dir, "objective-ledger.json")
		}
	}
	return d
}

type digestContext struct {
	SpecName string
	Role     Role
}

func renderTurnContextDigest(turnContext TurnContextDigest, workspace platform.WorkspaceSnapshot, contexts ...digestContext) string {
	var lines []string
	lines = append(lines, "## Current State Digest", "")
	turnContext = turnContext.withTranscriptPath(turnContext.TranscriptPath)
	if len(contexts) > 0 {
		ctx := contexts[0]
		if strings.TrimSpace(ctx.SpecName) != "" {
			lines = append(lines, "- Spec: `"+strings.TrimSpace(ctx.SpecName)+"`")
		}
		if strings.TrimSpace(ctx.Role) != "" {
			lines = append(lines, "- Role: `"+strings.TrimSpace(ctx.Role)+"`")
		}
	}
	if turnContext.TranscriptPath != "" {
		lines = append(lines, fmt.Sprintf("- Full transcript: `%s`", turnContext.TranscriptPath))
		if turnContext.EvidencePath != "" {
			lines = append(lines, fmt.Sprintf("- Evidence log: `%s`", turnContext.EvidencePath))
		}
		if turnContext.LedgerPath != "" {
			lines = append(lines, fmt.Sprintf("- Objective ledger: `%s`", turnContext.LedgerPath))
		}
	}
	lines = append(lines, renderWorkspaceDigest(workspace)...)

	ledgerOK := turnContext.Ledger != nil
	if ledgerOK {
		ledger := turnContext.Ledger
		if ledger.Objective != "" {
			lines = append(lines, "- Current objective: "+oneLine(ledger.Objective))
		}
		if ledger.State != "" {
			lines = append(lines, "- Objective state: `"+string(ledger.State)+"`")
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
	body := boundedDigestTranscript(turnContext.Transcript)
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
	if last := lastBlock(protocol.TagStatus, body); last != "" {
		lines = append(lines, "- Last verifier status: "+strings.ToUpper(strings.TrimSpace(last)))
	}
	return strings.Join(lines, "\n")
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

func recentLedgerErrors(turns []objectiveledger.ObjectiveTurn, limit int) []string {
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
	if last := lastBlock(protocol.TagProgressUpdate, body); last != "" {
		lines = append(lines, "- Last implementation: "+oneLine(last))
	}
	if last := lastBlock(protocol.TagReview, body); last != "" {
		lines = append(lines, "- Last review: "+oneLine(last))
	} else if last := lastBlock(protocol.TagSummary, body); last != "" {
		lines = append(lines, "- Last review summary: "+oneLine(last))
	}
	if last := lastBlock(protocol.TagStatus, body); last != "" {
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

func boundedDigestTranscript(body string) string {
	data := []byte(body)
	const maxDigestBytes = 128 << 10
	if len(data) > maxDigestBytes {
		data = data[len(data)-maxDigestBytes:]
	}
	return string(data)
}

func lastBlock(tag protocol.Tag, text string) string {
	body, ok := protocol.LastBlock(tag, text)
	if !ok {
		return ""
	}
	return body
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

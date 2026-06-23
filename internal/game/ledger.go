package game

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const objectiveLedgerSchema = "telos.objective_ledger.v1"

type ObjectiveState string

const (
	ObjectiveStatePlan      ObjectiveState = "plan"
	ObjectiveStateImplement ObjectiveState = "implement"
	ObjectiveStateVerify    ObjectiveState = "verify"
	ObjectiveStateRepair    ObjectiveState = "repair"
	ObjectiveStateFinalize  ObjectiveState = "finalize"
	ObjectiveStateBlocked   ObjectiveState = "blocked"
)

type ObjectiveLedger struct {
	Schema             string          `json:"schema"`
	SessionID          string          `json:"session_id"`
	SystemName         string          `json:"system_name"`
	Objective          string          `json:"objective,omitempty"`
	State              ObjectiveState  `json:"state"`
	LastTransition     string          `json:"last_transition,omitempty"`
	UpdatedAt          string          `json:"updated_at"`
	LastImplementation string          `json:"last_implementation,omitempty"`
	LastEvaluation     string          `json:"last_evaluation,omitempty"`
	OpenFindings       []string        `json:"open_findings,omitempty"`
	ResolvedFindings   []string        `json:"resolved_findings,omitempty"`
	Turns              []ObjectiveTurn `json:"turns,omitempty"`
}

type ObjectiveTurn struct {
	RoundNum       int            `json:"round_num"`
	Role           string         `json:"role"`
	Status         AgentStatus    `json:"status"`
	StateAfter     ObjectiveState `json:"state_after"`
	Error          string         `json:"error,omitempty"`
	ProgressUpdate string         `json:"progress_update,omitempty"`
}

var findingLineRE = regexp.MustCompile(`(?i)\b(fail|finding|blocked|blocker|continue|unresolved|must|missing|regression)\b`)

func newObjectiveLedger(state *PVGState, objective string) ObjectiveLedger {
	return ObjectiveLedger{
		Schema:     objectiveLedgerSchema,
		SessionID:  state.SessionID,
		SystemName: state.SystemName,
		Objective:  firstParagraph(objective),
		State:      ObjectiveStatePlan,
		UpdatedAt:  time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func readObjectiveLedger(path string) (ObjectiveLedger, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ObjectiveLedger{}, err
	}
	var ledger ObjectiveLedger
	if err := json.Unmarshal(data, &ledger); err != nil {
		return ObjectiveLedger{}, err
	}
	return ledger, nil
}

func writeObjectiveLedger(path string, ledger ObjectiveLedger) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	ledger.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	data, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func (p *PVG) loadLedger() ObjectiveLedger {
	ledger, err := readObjectiveLedger(p.State.LedgerPath)
	if err == nil && ledger.Schema == objectiveLedgerSchema {
		return ledger
	}
	return newObjectiveLedger(p.State, p.Compiled.SpecText)
}

func (p *PVG) transitionObjectiveState(next ObjectiveState, roundNum int, role string, reason string) {
	ledger := p.loadLedger()
	if ledger.State == next && ledger.LastTransition == reason {
		return
	}
	previous := ledger.State
	ledger.State = next
	ledger.LastTransition = reason
	if err := writeObjectiveLedger(p.State.LedgerPath, ledger); err == nil {
		p.Evidence.Log("state_transition", roundNum, role, map[string]interface{}{
			"from":   previous,
			"to":     next,
			"reason": reason,
			"ledger": p.State.LedgerPath,
		})
	}
}

// updateObjectiveLedger records the bookkeeping for a finished turn. The target
// state is decided by the task state machine and passed in as stateAfter; this
// function only derives the findings/summary bookkeeping from that state so the
// ledger can never disagree with the machine about which transition occurred.
func (p *PVG) updateObjectiveLedger(roundNum int, role string, turn TurnResult, stateAfter ObjectiveState) {
	ledger := p.loadLedger()
	progress := lastProgressUpdate(turn.Logs)
	switch stateAfter {
	case ObjectiveStateVerify:
		// The prover handed off to verification; capture its implementation summary.
		if progress != "" {
			ledger.LastImplementation = progress
		}
	case ObjectiveStateFinalize:
		// The verifier conceded (or review cycles completed); open findings are resolved.
		if len(ledger.OpenFindings) > 0 {
			ledger.ResolvedFindings = appendUnique(ledger.ResolvedFindings, ledger.OpenFindings...)
		}
		ledger.OpenFindings = nil
	case ObjectiveStateRepair:
		// The verifier wants changes; record the findings to repair. Prefer the
		// structured pvg <findings> block, then the review CSV (score-driven), and
		// fall back to the legacy keyword scan only when neither structured block
		// is present.
		findings, ok := findingsBlockFindings(turn.Logs)
		if !ok {
			findings, ok = reviewFindings(turn.Logs)
		}
		if !ok {
			findings = extractFindings(turn.Logs)
		}
		if len(findings) > 0 {
			ledger.OpenFindings = findings
		}
	}
	if role == RoleVerifier {
		if summary := lastSummaryOrReview(turn.Logs); summary != "" {
			ledger.LastEvaluation = summary
		} else if progress != "" {
			ledger.LastEvaluation = progress
		}
	}
	ledger.State = stateAfter
	ledger.Turns = append(ledger.Turns, ObjectiveTurn{
		RoundNum:       roundNum,
		Role:           role,
		Status:         turn.Status,
		StateAfter:     stateAfter,
		Error:          turn.Error,
		ProgressUpdate: progress,
	})
	if len(ledger.Turns) > 200 {
		ledger.Turns = ledger.Turns[len(ledger.Turns)-200:]
	}
	_ = writeObjectiveLedger(p.State.LedgerPath, ledger)
}

func lastProgressUpdate(text string) string {
	matches := progressUpdateRE.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 || len(matches[len(matches)-1]) < 2 {
		return ""
	}
	return compactLedgerText(matches[len(matches)-1][1], 500)
}

func lastSummaryOrReview(text string) string {
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`(?is)<summary\b[^>]*>(.*?)</summary>`),
		regexp.MustCompile(`(?is)<review\b[^>]*>(.*?)</review>`),
	} {
		matches := re.FindAllStringSubmatch(text, -1)
		if len(matches) > 0 && len(matches[len(matches)-1]) >= 2 {
			return compactLedgerText(matches[len(matches)-1][1], 700)
		}
	}
	return ""
}

func extractFindings(text string) []string {
	var findings []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.Trim(strings.TrimSpace(line), "|")
		if line == "" || strings.HasPrefix(strings.ToLower(line), "criteria,score") {
			continue
		}
		if findingLineRE.MatchString(line) {
			findings = appendUnique(findings, compactLedgerText(line, 400))
		}
		if len(findings) >= 20 {
			break
		}
	}
	if len(findings) == 0 && ExtractStatus(text) == StatusContinue {
		if summary := lastSummaryOrReview(text); summary != "" {
			findings = append(findings, summary)
		}
	}
	return findings
}

func appendUnique(items []string, values ...string) []string {
	seen := map[string]bool{}
	for _, item := range items {
		seen[item] = true
	}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		items = append(items, value)
		seen[value] = true
	}
	return items
}

func firstParagraph(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	for _, part := range strings.Split(text, "\n\n") {
		part = strings.TrimSpace(part)
		if part != "" && !strings.HasPrefix(part, "---") {
			return compactLedgerText(part, 500)
		}
	}
	return compactLedgerText(text, 500)
}

func compactLedgerText(text string, max int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if len(text) <= max {
		return text
	}
	return text[:max] + "..."
}

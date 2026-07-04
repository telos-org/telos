package game

import (
	"regexp"
	"strings"

	objectiveledger "github.com/telos-org/telos/internal/ledger"
	"github.com/telos-org/telos/internal/protocol"
)

const objectiveLedgerSchema = objectiveledger.Schema

type ObjectiveState = objectiveledger.ObjectiveState

const (
	ObjectiveStatePlan      = objectiveledger.ObjectiveStatePlan
	ObjectiveStateImplement = objectiveledger.ObjectiveStateImplement
	ObjectiveStateVerify    = objectiveledger.ObjectiveStateVerify
	ObjectiveStateRepair    = objectiveledger.ObjectiveStateRepair
	ObjectiveStateFinalize  = objectiveledger.ObjectiveStateFinalize
	ObjectiveStateBlocked   = objectiveledger.ObjectiveStateBlocked
)

type ObjectiveLedger = objectiveledger.ObjectiveLedger
type ObjectiveTurn = objectiveledger.ObjectiveTurn

var findingLineRE = regexp.MustCompile(`(?i)\b(fail|finding|blocked|blocker|continue|unresolved|must|missing|regression)\b`)

func newObjectiveLedger(state *PVGState, objective string) ObjectiveLedger {
	return objectiveledger.New(state.SessionID, state.SystemName, objective)
}

func readObjectiveLedger(path string) (ObjectiveLedger, error) {
	return objectiveledger.Read(path)
}

// InitializeObjectiveLedger creates the objective ledger when it is missing or
// empty, preserving existing ledger state across worker restarts.
func InitializeObjectiveLedger(path string, state *PVGState, objective string) error {
	return objectiveledger.Initialize(path, newObjectiveLedger(state, objective))
}

func writeObjectiveLedger(path string, ledger ObjectiveLedger) error {
	return objectiveledger.Write(path, ledger)
}

func (p *PVG) loadLedger() ObjectiveLedger {
	if p != nil && p.ledger.Schema == objectiveLedgerSchema {
		return p.ledger
	}
	if p == nil {
		return ObjectiveLedger{}
	}
	p.ledger = newObjectiveLedger(p.State, p.Compiled.SpecText)
	return p.ledger
}

func (p *PVG) transitionObjectiveState(next ObjectiveState, roundNum int, role string, reason string) {
	ledger := p.loadLedger()
	if ledger.State == next {
		return
	}
	previous := ledger.State
	ledger.State = next
	ledger.LastTransition = reason
	p.ledger = ledger
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
	previousState := ledger.State
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
		// structured pvg <findings> block when it contains at least one finding,
		// then the review CSV (score-driven), and finally the legacy keyword scan.
		// The final fallback keeps older/replayed CONTINUE logs with an empty
		// findings block from entering repair with no actionable ledger item.
		findings, ok := findingsBlockFindings(turn.Logs)
		if !ok || len(findings) == 0 {
			findings, ok = reviewFindings(turn.Logs)
		}
		if !ok || len(findings) == 0 {
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
	if previousState != stateAfter {
		ledger.LastTransition = "turn_completed"
	}
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
	p.ledger = ledger
	if err := writeObjectiveLedger(p.State.LedgerPath, ledger); err == nil && previousState != stateAfter {
		p.Evidence.Log("state_transition", roundNum, role, map[string]interface{}{
			"from":   previousState,
			"to":     stateAfter,
			"reason": ledger.LastTransition,
			"ledger": p.State.LedgerPath,
		})
	}
}

func lastProgressUpdate(text string) string {
	progress := protocol.LastProgressUpdate(text)
	if progress == "" {
		return ""
	}
	return compactLedgerText(progress, 500)
}

func lastSummaryOrReview(text string) string {
	body := protocol.LastSummaryOrReview(text)
	if body == "" {
		return ""
	}
	return compactLedgerText(body, 700)
}

func extractFindings(text string) []string {
	var findings []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.Trim(strings.TrimSpace(line), "|")
		if line == "" || strings.HasPrefix(strings.ToLower(line), "criteria,score") || isProtocolTagLine(line) {
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

func isProtocolTagLine(line string) bool {
	lower := strings.ToLower(strings.TrimSpace(line))
	for _, prefix := range []string{
		"<findings",
		"</findings",
		"<progress_update",
		"</progress_update",
		"<review",
		"</review",
		"<summary",
		"</summary",
		"<status",
		"</status",
	} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
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

func compactLedgerText(text string, max int) string {
	return objectiveledger.CompactText(text, max)
}

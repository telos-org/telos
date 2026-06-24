package executor

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/openai/openai-go/responses"
	"github.com/telos-org/telos/internal/agentsession"
)

const (
	defaultCompactionContextWindow     = 128000
	defaultCompactionTriggerRatio      = 0.7
	defaultCompactionKeepRecentTokens  = 20000
	compactionStrategyLLM              = "llm"
	compactionStrategyTruncate         = "truncate"
	compactionSummaryMessagePrefix     = "Compacted prior session state:\n"
	compactionCommand                  = "COMPACT_SESSION_STATE"
	compactionHeadingSchemaDescription = `Produce only a compacted state summary. Do not call tools. Do not answer the original task directly.

Return terse Markdown with exactly these headings, in this order:
## Goal
## Constraints & Preferences
## Progress
## Key Decisions
## Files Inspected
## Files Changed
## Commands Run
## Test Results
## Open Issues
## Next Action

Capture durable facts needed for the coding agent to continue: user requirements, repository findings, decisions, file paths, commands, test results, failures, and next steps. Be specific and do not omit unresolved blockers. Do not copy filler, repeated logs, or large raw outputs. Prefer short bullets; for "Files Inspected" and "Files Changed", list paths only.`
)

var compactionRequiredHeadings = []string{
	"## Goal",
	"## Constraints & Preferences",
	"## Progress",
	"## Key Decisions",
	"## Files Inspected",
	"## Files Changed",
	"## Commands Run",
	"## Test Results",
	"## Open Issues",
	"## Next Action",
}

type compactionConfig struct {
	contextWindow    int
	triggerRatio     float64
	keepRecentTokens int
	reserveOutput    int
	strategy         string
}

// compactionConfigFromEnv resolves the compaction config. contextWindow starts
// from the global default (overridable by TELOS_AUTOCOMPACT_CONTEXT_WINDOW) and
// is then floored to the model's actual context window when known, so the
// trigger that exists to prevent context overflow can never exceed what the
// model can hold. A modelContextWindow of 0 means "unknown" and leaves the
// configured default in place.
func compactionConfigFromEnv(reserveOutput, modelContextWindow int) compactionConfig {
	cfg := compactionConfig{
		contextWindow:    defaultCompactionContextWindow,
		triggerRatio:     defaultCompactionTriggerRatio,
		keepRecentTokens: defaultCompactionKeepRecentTokens,
		reserveOutput:    reserveOutput,
		strategy:         compactionStrategyLLM,
	}
	if raw := strings.TrimSpace(os.Getenv("TELOS_AUTOCOMPACT_CONTEXT_WINDOW")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
			cfg.contextWindow = n
		}
	}
	if raw := strings.TrimSpace(os.Getenv("TELOS_AUTOCOMPACT_TRIGGER_RATIO")); raw != "" {
		if f, err := strconv.ParseFloat(raw, 64); err == nil && f > 0 && f <= 1 {
			cfg.triggerRatio = f
		}
	}
	if raw := strings.TrimSpace(os.Getenv("TELOS_AUTOCOMPACT_KEEP_RECENT_TOKENS")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			cfg.keepRecentTokens = n
		}
	}
	if raw := strings.TrimSpace(os.Getenv("TELOS_AUTOCOMPACT_STRATEGY")); raw != "" {
		switch strings.ToLower(raw) {
		case compactionStrategyLLM:
			cfg.strategy = compactionStrategyLLM
		case "naive", compactionStrategyTruncate:
			cfg.strategy = compactionStrategyTruncate
		}
	}
	if modelContextWindow > 0 && (cfg.contextWindow <= 0 || modelContextWindow < cfg.contextWindow) {
		cfg.contextWindow = modelContextWindow
	}
	return cfg
}

func (cfg compactionConfig) budgetTokens() int {
	if cfg.contextWindow <= 0 {
		return 0
	}
	return int(cfg.triggerRatio*float64(cfg.contextWindow)) - cfg.reserveOutput
}

type compactor struct {
	cfg compactionConfig
}

func newCompactor(cfg compactionConfig) *compactor {
	return &compactor{cfg: cfg}
}

type compactionPlan struct {
	reason             string
	firstKeptIndex     int
	oldFirstKeptIndex  int
	tokensBefore       int
	projectedTokensRaw int
	itemsSummarized    int
	itemsKept          int
}

func (c *compactor) plan(s *conversationState) (compactionPlan, bool, error) {
	if c == nil || s == nil || s.mode != conversationStateStatelessHistory {
		return compactionPlan{}, false, nil
	}
	budget := c.cfg.budgetTokens()
	if budget <= 0 {
		return compactionPlan{}, false, nil
	}
	current := s.requestInput()
	tokensBefore := estimateHistoryTokens(current)
	if tokensBefore <= budget {
		return compactionPlan{}, false, nil
	}
	if len(s.history) <= 1 {
		return compactionPlan{}, false, fmt.Errorf("autocompaction cannot reduce a history with no prior assistant/tool context")
	}
	firstKept := chooseFirstKeptIndex(s.history, s.firstKeptIndex, c.cfg.keepRecentTokens)
	firstKept = ensureSummaryRoom(s, firstKept, budget)
	if firstKept >= len(s.history) {
		// ensureSummaryRoom advanced past the final message to reserve summary
		// headroom. Dropping all recent context is correct when the tail is
		// oversized or orphaned (the summary still captures it), but not when a
		// coherent recent group would itself fit the budget — there is no point
		// reserving room for a summary by discarding the only raw turn it would
		// sit beside. Back off to that tail when one exists and fits.
		if tail := lastTailBoundary(s.history, s.firstKeptIndex+1); tail > s.firstKeptIndex &&
			estimateHistoryTokens(s.inputWithSummary("", tail)) <= budget {
			firstKept = tail
		}
	}
	if firstKept <= s.firstKeptIndex || firstKept > len(s.history) {
		return compactionPlan{}, false, fmt.Errorf("autocompaction cannot find a new safe cut point")
	}
	return compactionPlan{
		reason:             "token_budget",
		firstKeptIndex:     firstKept,
		oldFirstKeptIndex:  s.firstKeptIndex,
		tokensBefore:       tokensBefore,
		projectedTokensRaw: estimateHistoryTokens(s.inputWithSummary("", firstKept)),
		itemsSummarized:    firstKept - s.firstKeptIndex,
		itemsKept:          len(s.history) - firstKept,
	}, true, nil
}

func ensureSummaryRoom(s *conversationState, firstKept, budget int) int {
	if s == nil || budget <= 0 {
		return firstKept
	}
	minSummaryTokens := budget / 4
	if minSummaryTokens < 500 {
		minSummaryTokens = 500
	}
	if minSummaryTokens > 1500 {
		minSummaryTokens = 1500
	}
	targetBase := budget - minSummaryTokens
	for firstKept < len(s.history) && estimateHistoryTokens(s.inputWithSummary("", firstKept)) > targetBase {
		next := forwardFunctionOutputBoundary(s.history, firstKept+1)
		if next <= firstKept {
			next = firstKept + 1
		}
		firstKept = next
	}
	return firstKept
}

// lastTailBoundary returns the largest cut point in [min, len-1] that keeps at
// least the final message group without orphaning a function output, or 0 if no
// such non-orphan tail exists. It is used to avoid dropping all recent context
// when a coherent, in-budget tail could have been kept; reserving summary
// headroom is not worth sending the model a summary with an empty tail.
func lastTailBoundary(history responses.ResponseInputParam, min int) int {
	if min < 1 {
		min = 1
	}
	for b := len(history) - 1; b >= min; b-- {
		if !hasOrphanFunctionOutputFrom(history, b) {
			return b
		}
	}
	return 0
}

func chooseFirstKeptIndex(history responses.ResponseInputParam, currentFirstKept, keepRecentTokens int) int {
	if len(history) <= 1 {
		return len(history)
	}
	if currentFirstKept < 1 {
		currentFirstKept = 1
	}
	boundary := len(history)
	recentTokens := 0
	for i := len(history) - 1; i >= 1; i-- {
		itemTokens := estimateItemTokens(history[i])
		if recentTokens == 0 && itemTokens > keepRecentTokens {
			boundary = i + 1
			break
		}
		boundary = i
		recentTokens += itemTokens
		if recentTokens >= keepRecentTokens {
			break
		}
	}
	if boundary <= currentFirstKept {
		boundary = currentFirstKept + 1
	}
	if boundary < len(history) {
		if userBoundary := nearestUserBoundary(history, boundary, currentFirstKept+1); userBoundary > 0 {
			boundary = userBoundary
		}
	}
	return repairFunctionOutputBoundary(history, boundary, currentFirstKept+1)
}

func nearestUserBoundary(history responses.ResponseInputParam, start, min int) int {
	if start < min {
		start = min
	}
	if start >= len(history) {
		start = len(history) - 1
	}
	for i := start; i >= min; i-- {
		if isUserMessage(history[i]) {
			return i
		}
	}
	return 0
}

func repairFunctionOutputBoundary(history responses.ResponseInputParam, boundary, min int) int {
	if boundary < min {
		boundary = min
	}
	if boundary > len(history) {
		return len(history)
	}
	for distance := 0; ; distance++ {
		checked := false
		if candidate := boundary - distance; candidate >= min {
			checked = true
			if !hasOrphanFunctionOutputFrom(history, candidate) {
				return candidate
			}
		}
		if distance > 0 {
			if candidate := boundary + distance; candidate <= len(history) {
				checked = true
				if !hasOrphanFunctionOutputFrom(history, candidate) {
					return candidate
				}
			}
		}
		if !checked {
			return len(history)
		}
	}
}

func hasOrphanFunctionOutputFrom(history responses.ResponseInputParam, boundary int) bool {
	calls := map[string]bool{}
	for i := boundary; i < len(history); i++ {
		if fc := history[i].OfFunctionCall; fc != nil {
			calls[fc.CallID] = true
		}
	}
	for i := boundary; i < len(history); i++ {
		if fco := history[i].OfFunctionCallOutput; fco != nil && !calls[fco.CallID] {
			return true
		}
	}
	return false
}

func forwardFunctionOutputBoundary(history responses.ResponseInputParam, boundary int) int {
	if boundary < 1 {
		boundary = 1
	}
	for boundary <= len(history) {
		if !hasOrphanFunctionOutputFrom(history, boundary) {
			return boundary
		}
		boundary++
	}
	return len(history)
}

func isUserMessage(item responses.ResponseInputItemUnionParam) bool {
	if item.OfMessage == nil {
		return false
	}
	return string(item.OfMessage.Role) == string(responses.EasyInputMessageRoleUser)
}

func compactionSummaryMessage(summary string) responses.ResponseInputItemUnionParam {
	return responses.ResponseInputItemParamOfMessage(compactionSummaryMessagePrefix+summary, responses.EasyInputMessageRoleAssistant)
}

func compactionCommandMessage(summaryBudgetTokens int) responses.ResponseInputItemUnionParam {
	text := compactionCommand + "\n\n" + compactionHeadingSchemaDescription
	if summaryBudgetTokens > 0 {
		text += fmt.Sprintf("\n\nHard length limit: keep the entire summary under roughly %d tokens. If the raw history is huge, keep only exact facts needed to continue and omit repetitive filler.", summaryBudgetTokens)
	}
	return responses.ResponseInputItemParamOfMessage(text, responses.EasyInputMessageRoleUser)
}

func validateCompactionSummary(summary string) error {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return fmt.Errorf("empty compacted session summary")
	}
	last := -1
	for _, heading := range compactionRequiredHeadings {
		idx := strings.Index(summary, heading)
		if idx < 0 {
			return fmt.Errorf("compacted session summary missing heading %q", heading)
		}
		if idx < last {
			return fmt.Errorf("compacted session summary heading %q is out of order", heading)
		}
		last = idx
	}
	return nil
}

type compactionDetails = agentsession.CompactionDetails

var (
	markdownListPrefixRE = regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])\s*`)
	backtickPathRE       = regexp.MustCompile("`([^`]+)`")
)

func detailsFromCompactionSummary(summary string) compactionDetails {
	return compactionDetails{
		ReadFiles:     extractHeadingList(summary, "## Files Inspected"),
		ModifiedFiles: extractHeadingList(summary, "## Files Changed"),
	}
}

func extractHeadingList(summary, heading string) []string {
	section := headingSection(summary, heading)
	if section == "" {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(section, "\n") {
		line = strings.TrimSpace(markdownListPrefixRE.ReplaceAllString(line, ""))
		line = strings.TrimSpace(strings.TrimPrefix(line, "(pending)"))
		if match := backtickPathRE.FindStringSubmatch(line); len(match) == 2 {
			line = match[1]
		}
		line = strings.Trim(line, "` ")
		if line == "" || strings.EqualFold(line, "none") || strings.EqualFold(line, "n/a") {
			continue
		}
		if !seen[line] {
			out = append(out, line)
			seen[line] = true
		}
	}
	return out
}

func headingSection(summary, heading string) string {
	start := strings.Index(summary, heading)
	if start < 0 {
		return ""
	}
	rest := summary[start+len(heading):]
	if next := strings.Index(rest, "\n## "); next >= 0 {
		rest = rest[:next]
	}
	return strings.TrimSpace(rest)
}

func estimateItemTokens(item responses.ResponseInputItemUnionParam) int {
	chars := 0
	switch {
	case item.OfMessage != nil:
		chars = len(messageItemText(item.OfMessage))
	case item.OfFunctionCall != nil:
		chars = len(item.OfFunctionCall.Name) + len(item.OfFunctionCall.Arguments)
	case item.OfFunctionCallOutput != nil:
		chars = len(item.OfFunctionCallOutput.Output)
	default:
		if data, err := json.Marshal(item); err == nil {
			chars = len(data)
		}
	}
	return chars/4 + 4
}

func estimateHistoryTokens(history responses.ResponseInputParam) int {
	total := 0
	for i := range history {
		total += estimateItemTokens(history[i])
	}
	return total
}

func messageItemText(msg *responses.EasyInputMessageParam) string {
	if msg == nil {
		return ""
	}
	if msg.Content.OfString.Valid() {
		return msg.Content.OfString.Value
	}
	if data, err := json.Marshal(msg.Content); err == nil {
		return string(data)
	}
	return ""
}

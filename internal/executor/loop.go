package executor

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/telos-org/telos/internal/game"
)

const (
	defaultMaxToolLoops    = 160
	defaultMaxOutputTokens = 16384

	// maxFormattingCorrections is the per-key retry budget for protocol
	// *formatting* errors (a missing/duplicated/mis-cased response tag). These
	// are recoverable with another attempt, so they get more than one nudge;
	// semantic keys (e.g. a missing rubric read) keep a single correction.
	maxFormattingCorrections = 3
)

// Protocol-tag matchers used for response validation. These intentionally
// mirror the lenient extractors in internal/spec (case-insensitive, attribute
// tolerant) so the gate that decides pass/fail agrees with the parser that
// later consumes the blocks. Counting *balanced* matches — rather than raw tag
// occurrences — also keeps a stray literal mention (e.g. "the <review> block")
// from inflating the count.
var (
	reviewBlockRE   = regexp.MustCompile(`(?is)<review\b[^>]*>.*?</review>`)
	summaryBlockRE  = regexp.MustCompile(`(?is)<summary\b[^>]*>.*?</summary>`)
	progressBlockRE = regexp.MustCompile(`(?is)<progress_update\b[^>]*>.*?</progress_update>`)
	findingsBlockRE = regexp.MustCompile(`(?is)<findings\b[^>]*>.*?</findings>`)
)

func countBlocks(re *regexp.Regexp, text string) int {
	return len(re.FindAllString(text, -1))
}

// maxProtocolCorrections returns the per-key retry budget by looking the key up
// in the rule table for (role, protocolMode). Each rule owns its own budget, so
// the formatting keys that warrant more than one nudge (prover progress-update,
// verifier review-block) carry maxFormattingCorrections directly. Keys with no
// matching rule — including empty_final and the missing-rubric concession nudge —
// get a single correction.
func maxProtocolCorrections(role, protocolMode, key string) int {
	for _, rule := range protocolRulesFor(role, protocolMode) {
		if rule.key == key {
			return rule.retries
		}
	}
	return 1
}

// effectiveMaxToolLoops resolves the per-turn tool-loop ceiling. The manifest
// budget is the source of truth: a configured MaxToolLoops always wins. With no
// manifest value, the harness default applies. There is no env override —
// TELOS_NATIVE_MAX_TOOL_LOOPS was removed because it conflicted with the
// manifest budget and made runs non-reproducible from the manifest alone.
func effectiveMaxToolLoops(budget game.TurnBudget) int {
	if budget.MaxToolLoops > 0 {
		return budget.MaxToolLoops
	}
	return defaultMaxToolLoops
}

// effectiveMaxOutputTokens resolves the per-model-request output-token ceiling.
// Precedence (most restrictive wins): manifest budget remaining tokens, then
// the model capability profile's max, then the harness default. The manifest is
// the source of truth; TELOS_NATIVE_MAX_OUTPUT_TOKENS was removed because it
// acted as a hidden base ceiling that could only cap *down*, contradicting the
// manifest-wins precedence used for tool loops.
func effectiveMaxOutputTokens(cfg nativeProviderConfig, budget game.TurnBudget) int {
	maxOut := defaultMaxOutputTokens
	if cfg.Capability.MaxOutputTokens > 0 && cfg.Capability.MaxOutputTokens < maxOut {
		maxOut = cfg.Capability.MaxOutputTokens
	}
	if budget.RemainingOutputTokens > 0 && budget.RemainingOutputTokens < maxOut {
		maxOut = budget.RemainingOutputTokens
	}
	return maxOut
}

// agentTurn is one model response, lifted out of the openai-go types into the
// shape the agent loop drives.
type agentTurn struct {
	text       string
	calls      []nativeToolCall
	stopReason string
	stats      game.TurnStats
}

type agentLoop struct {
	client         *responsesClient
	tools          *nativeTools
	logger         *nativeSessionLogger
	task           string
	role           string
	protocolMode   string
	provider       string
	model          string
	budget         game.TurnBudget
	strictProtocol bool
	toolsAvailable bool
	keepReasoning  bool
}

// policyKey identifies a rule set by role and protocol mode.
type policyKey struct {
	role string
	mode string
}

// ruleContext carries everything a rule's check may consult. Passing a struct
// keeps the check/message signatures stable as new inputs appear.
type ruleContext struct {
	text           string // trimmed assistant text
	usedTool       bool
	strict         bool
	toolsAvailable bool
}

// protocolRule is one enforcement rule for a (role, mode). check reports whether
// the rule is violated (a correction is needed); message renders the correction
// prompt (interpolating observed counts where relevant); retries is the per-key
// correction budget.
type protocolRule struct {
	key     string
	check   func(ruleContext) bool
	message func(ruleContext) string
	retries int
}

// Per-(role, mode) rule sets. Order is significant: the first violated rule
// wins, reproducing the precedence of the prior nested if-checks (shape rules
// before the prover no-tool nudge). empty_final is handled as an unconditional
// pre-check in protocolCorrectionForStrict, so it is not listed here.
var (
	requireStatusRule = protocolRule{
		key:     "missing_status",
		check:   func(c ruleContext) bool { return !hasStatusTag(c.text) },
		message: func(ruleContext) string { return missingStatusMessage },
		retries: 1,
	}

	requireReviewBlocksRule = protocolRule{
		key: "malformed_review_blocks",
		check: func(c ruleContext) bool {
			return countBlocks(reviewBlockRE, c.text) != 1 || countBlocks(summaryBlockRE, c.text) != 1
		},
		message: func(c ruleContext) string {
			return fmt.Sprintf(malformedReviewBlocksMessage, countBlocks(reviewBlockRE, c.text), countBlocks(summaryBlockRE, c.text))
		},
		retries: maxFormattingCorrections,
	}

	// requireFindingsRule enforces the pvg-mode verifier <findings> contract so a
	// continuing verifier emits structured findings (a single block) rather than
	// prose the ledger has to keyword-scrape. Scoped to CONTINUE turns: that is
	// where the objective ledger consumes findings (the repair state), and a
	// concession has nothing to list. Ordered after the status rule so an
	// invalid/missing terminator is corrected first; once the status rule passes
	// the terminator is a valid CONTINUE or CONCEDE.
	requireFindingsRule = protocolRule{
		key:   "malformed_findings_block",
		check: func(c ruleContext) bool { return statusIsContinue(c.text) && countBlocks(findingsBlockRE, c.text) != 1 },
		message: func(c ruleContext) string {
			return fmt.Sprintf(malformedFindingsMessage, countBlocks(findingsBlockRE, c.text))
		},
		retries: maxFormattingCorrections,
	}

	// The progress-update rules are mutually exclusive on strict so at most one
	// fires: strict requires exactly one block (malformed otherwise); lenient
	// only requires at least one (missing otherwise). Split into two rules to
	// preserve the two distinct correction keys callers and metrics depend on.
	proverProgressStrictRule = protocolRule{
		key:   "malformed_progress_update",
		check: func(c ruleContext) bool { return c.strict && countBlocks(progressBlockRE, c.text) != 1 },
		message: func(c ruleContext) string {
			return fmt.Sprintf(malformedProgressMessage, countBlocks(progressBlockRE, c.text))
		},
		retries: maxFormattingCorrections,
	}

	proverProgressLenientRule = protocolRule{
		key:     "missing_progress_update",
		check:   func(c ruleContext) bool { return !c.strict && countBlocks(progressBlockRE, c.text) < 1 },
		message: func(ruleContext) string { return missingProgressMessage },
		retries: maxFormattingCorrections,
	}

	// requireToolForProverFinalRule nudges a prover final that used no tool. This
	// replaces the old artifactOriented(task) keyword heuristic: the nudge now
	// fires regardless of task wording, so a prover final with no tool use always
	// gets one recoverable correction. Only reached when the progress-update
	// rules pass (first-match-wins order), matching the prior nested placement.
	requireToolForProverFinalRule = protocolRule{
		key:     "no_tool_for_artifact_task",
		check:   func(c ruleContext) bool { return c.toolsAvailable && !c.usedTool },
		message: func(ruleContext) string { return noToolForArtifactMessage },
		retries: 1,
	}

	proverRules = []protocolRule{proverProgressStrictRule, proverProgressLenientRule, requireToolForProverFinalRule}

	protocolPolicies = map[policyKey][]protocolRule{
		{game.RoleVerifier, game.ProtocolModePVG}:    {requireStatusRule, requireFindingsRule},
		{game.RoleVerifier, game.ProtocolModeReview}: {requireReviewBlocksRule},
		{game.RoleProver, game.ProtocolModePVG}:      proverRules,
	}
)

const (
	emptyFinalMessage            = "Your previous response had no visible result. Use the available tools to carry out the assignment, then reply with a visible final answer that follows this turn's required output tags."
	missingStatusMessage         = "Your previous response was missing the required final <status>...</status> tag. Reply with a concise final verifier result and include exactly one final <status>CONTINUE</status> or <status>CONCEDE</status> tag."
	malformedProgressMessage     = "Your previous response must contain exactly one <progress_update>...</progress_update> block, but it had %d. Reply with a concise final implementation summary and exactly one progress_update block; do not write the tag name anywhere else."
	missingProgressMessage       = "Your previous response was missing the required <progress_update>...</progress_update> block. Reply with a concise final implementation summary and include exactly one <progress_update> block."
	noToolForArtifactMessage     = "This turn appears to require inspecting or changing workspace artifacts, but no tool was used. Inspect or update the workspace with tools before finalizing, then include the required <progress_update> block."
	malformedReviewBlocksMessage = "Your previous review-mode response must contain exactly one <review>...</review> block and exactly one <summary>...</summary> block, but it had %d review and %d summary block(s). Put the CSV rubric inside a single <review> tag and your notes inside a single <summary> tag, and do not write either tag name anywhere else (including examples or narration)."
	malformedFindingsMessage     = "Your previous response must contain exactly one <findings>...</findings> block, but it had %d. List blocking findings inside a single <findings> tag, one per line as `severity | description`, using an empty <findings></findings> block if you have none; do not write the tag name anywhere else."
)

// protocolRulesFor selects the rule set for a role/mode. Mode is normalized so
// unknown or empty modes preserve the legacy switch behavior: the verifier
// defaults to the pvg (status) contract, and the prover ignores mode entirely.
func protocolRulesFor(role, protocolMode string) []protocolRule {
	switch role {
	case game.RoleVerifier:
		if protocolMode != game.ProtocolModeReview {
			protocolMode = game.ProtocolModePVG
		}
	case game.RoleProver:
		protocolMode = game.ProtocolModePVG
	}
	return protocolPolicies[policyKey{role, protocolMode}]
}

func newAgentLoop(httpClient *http.Client, cfg nativeProviderConfig, thinking string, tools *nativeTools, logger *nativeSessionLogger, task, role, protocolMode string, budget game.TurnBudget, knobs envKnobs) *agentLoop {
	maxOut := effectiveMaxOutputTokens(cfg, budget)
	tr := newResponsesClient(httpClient, cfg, thinking, maxOut, task, role, logger)
	return &agentLoop{
		client:         tr,
		tools:          tools,
		logger:         logger,
		task:           task,
		role:           role,
		protocolMode:   protocolMode,
		provider:       cfg.Provider,
		model:          cfg.Model,
		budget:         budget,
		strictProtocol: cfg.Capability.StrictProtocol,
		toolsAvailable: len(tr.tools) > 0,
		keepReasoning:  knobs.KeepReasoning,
	}
}

func (l *agentLoop) run(ctx context.Context) (string, game.TurnStats, error) {
	maxLoops := effectiveMaxToolLoops(l.budget)
	stats := game.TurnStats{Model: l.model}
	corrections := map[string]int{}
	usedTool := false

	toolLoops := 0
	for {
		if toolLoops >= maxLoops {
			err := newExecutorError(errAgentIncomplete, fmt.Sprintf("tool_loop_exceeded:%d", maxLoops))
			_ = l.logger.errorEvent(l.client.sequence, err)
			return "", stats, err
		}
		if err := l.checkBudget(stats); err != nil {
			_ = l.logger.errorEvent(l.client.sequence, err)
			return "", stats, err
		}
		turn, err := l.client.send(ctx)
		if err != nil {
			stats = mergeTurnStats(stats, turn.stats)
			return "", stats, err
		}
		stats = mergeTurnStats(stats, turn.stats)
		if err := l.checkBudget(stats); err != nil {
			_ = l.logger.errorEvent(l.client.sequence, err)
			return "", stats, err
		}
		if sanitized, removed := sanitizeVisibleText(turn.text, l.keepReasoning); removed != "" {
			_ = l.logger.reasoningLeak(removed)
			turn.text = sanitized
		}
		_ = l.logger.assistant(turn.text, l.provider, l.model, turn.stopReason, turn.stats)

		if len(turn.calls) == 0 {
			prompt, key := l.protocolCorrection(turn.text, usedTool)
			if prompt == "" {
				return turn.text, stats, nil
			}
			if corrections[key] < maxProtocolCorrections(l.role, l.protocolMode, key) {
				corrections[key]++
				_ = l.logger.protocolCorrection(key, prompt)
				l.client.recordCorrection(prompt)
				continue
			}
			err := newExecutorError(errAgentProtocol, key)
			_ = l.logger.errorEvent(l.client.sequence, err)
			return "", stats, err
		}

		usedTool = true
		toolLoops++
		for _, call := range turn.calls {
			_ = l.logger.toolCall(call)
		}
		results := l.tools.executeAll(ctx, turn.calls)
		stats.NumTurns += len(results)
		for _, result := range results {
			_ = l.logger.tool(result)
		}
		l.client.recordToolResults(results)
	}
}

func (l *agentLoop) checkBudget(stats game.TurnStats) error {
	if l.budget.RemainingCostUSD != nil && l.budget.CostHardLimit && stats.CostUnavailable {
		return newExecutorError(errRuntimeBudgetExhausted, "max_cost_usd_cost_unavailable")
	}
	if l.budget.RemainingCostUSD != nil && stats.CostUSD >= *l.budget.RemainingCostUSD {
		return newExecutorError(errRuntimeBudgetExhausted, "max_cost_usd")
	}
	if l.budget.RemainingInputTokens > 0 && stats.InputTokens >= l.budget.RemainingInputTokens {
		return newExecutorError(errRuntimeBudgetExhausted, "max_input_tokens")
	}
	if l.budget.RemainingOutputTokens > 0 && stats.OutputTokens >= l.budget.RemainingOutputTokens {
		return newExecutorError(errRuntimeBudgetExhausted, "max_output_tokens")
	}
	return nil
}

func (l *agentLoop) protocolCorrection(text string, usedTool bool) (string, string) {
	if prompt, key := protocolCorrectionForStrict(l.role, l.protocolMode, l.task, text, usedTool, l.strictProtocol, l.toolsAvailable); prompt != "" {
		return prompt, key
	}
	if l.role == game.RoleVerifier && verifierConcedes(text) {
		if missing := l.tools.missingRequiredSkills(); len(missing) > 0 {
			return fmt.Sprintf("Before conceding, read every required evaluation rubric with the skill tool. Missing required rubric skill(s): %s. Use skill action='read' for each, inspect the workspace against the rubric, then reply with a final verifier result and <status>CONCEDE</status> only if every required rubric passes.", strings.Join(missing, ", ")), "missing_required_skill_rubric"
		}
	}
	return "", ""
}

// protocolCorrectionFor is the lenient (non-strict) entry point used by offline
// session replay, where the model and capability profile are unavailable. The
// caller supplies the recorded protocol mode so review-mode verifier turns are
// validated against the review contract rather than the default pvg one.
func protocolCorrectionFor(role, protocolMode, task, text string, usedTool bool) (string, string) {
	return protocolCorrectionForStrict(role, protocolMode, task, text, usedTool, false, true)
}

// protocolCorrectionForStrict validates a final assistant turn against the rule
// table for (role, protocolMode). Empty output is always a correction; otherwise
// the first violated rule for the role/mode wins. task is retained in the
// signature for callers and future task-aware rules, though no current rule
// reads it. The returned (message, key) drive the correction prompt and the
// per-key retry budget.
func protocolCorrectionForStrict(role, protocolMode, task, text string, usedTool bool, strict bool, toolsAvailable bool) (string, string) {
	_ = task
	ctx := ruleContext{
		text:           strings.TrimSpace(text),
		usedTool:       usedTool,
		strict:         strict,
		toolsAvailable: toolsAvailable,
	}
	if ctx.text == "" {
		return emptyFinalMessage, "empty_final"
	}
	for _, rule := range protocolRulesFor(role, protocolMode) {
		if rule.check(ctx) {
			return rule.message(ctx), rule.key
		}
	}
	return "", ""
}

func hasStatusTag(text string) bool {
	if strings.Count(text, "<status>") != 1 || strings.Count(text, "</status>") != 1 {
		return false
	}
	_, ok := game.ParseFinalStatus(text)
	return ok
}

func statusIsContinue(text string) bool {
	status, ok := game.ParseFinalStatus(text)
	return ok && status == game.StatusContinue
}

func verifierConcedes(text string) bool {
	status, ok := game.ParseFinalStatus(text)
	return ok && status == game.StatusConcede
}

func nativeSystemPrompt(role string) string {
	return strings.Join([]string{
		"You are Telos' built-in coding agent working in the current workspace.",
		"The user message is the assignment for this turn. Act on it directly using the available tools; do not ask the operator what to do next or wait for confirmation before reading or editing files the task needs.",
		"If the latest user message is exactly COMPACT_SESSION_STATE or starts with COMPACT_SESSION_STATE, switch modes: produce only the requested compacted session state summary, do not call tools, and do not continue implementing the assignment.",
		"If the assignment names files to create or change, make those changes in the workspace before summarizing.",
		"Keep your answer in visible assistant text rather than only in hidden reasoning. End with a concise summary of what you changed and any checks you ran, plus any response-format tags the assignment asks for.",
		fmt.Sprintf("Your role for this turn is %s.", role),
	}, "\n")
}

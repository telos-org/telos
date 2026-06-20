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
)

func countBlocks(re *regexp.Regexp, text string) int {
	return len(re.FindAllString(text, -1))
}

// formattingCorrectionKeys are recoverable response-shape errors worth more
// than one retry; the model usually fixes them once it sees a diagnostic count.
// Scoped to the prover progress-update and verifier review-block paths, which
// are the tag shapes weaker models most often get wrong on the first reply.
var formattingCorrectionKeys = map[string]bool{
	"missing_progress_update":   true,
	"malformed_progress_update": true,
	"malformed_review_blocks":   true,
}

func maxProtocolCorrections(key string) int {
	if formattingCorrectionKeys[key] {
		return maxFormattingCorrections
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
	transport      *openaiTransport
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

type roleLoopPolicy struct {
	requireStatus          bool
	requireProgressUpdate  bool
	requireReviewBlocks    bool
	requireToolForArtifact bool
}

func loopPolicy(role, protocolMode, task string) roleLoopPolicy {
	switch role {
	case "verifier":
		reviewMode := protocolMode == "review"
		if protocolMode == "" {
			reviewMode = strings.Contains(task, "Do not emit <status> tags")
		}
		return roleLoopPolicy{
			requireStatus:       !reviewMode,
			requireReviewBlocks: reviewMode,
		}
	case "prover":
		return roleLoopPolicy{
			requireProgressUpdate:  true,
			requireToolForArtifact: true,
		}
	default:
		return roleLoopPolicy{}
	}
}

func newAgentLoop(httpClient *http.Client, cfg nativeProviderConfig, thinking string, tools *nativeTools, logger *nativeSessionLogger, task, role, protocolMode string, budget game.TurnBudget, knobs envKnobs) *agentLoop {
	maxOut := effectiveMaxOutputTokens(cfg, budget)
	tr := newOpenAITransport(httpClient, cfg, thinking, maxOut, task, role, logger)
	return &agentLoop{
		transport:      tr,
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

	for i := 0; i < maxLoops; i++ {
		if err := l.checkBudget(stats); err != nil {
			_ = l.logger.errorEvent(l.transport.sequence, err)
			return "", stats, err
		}
		turn, err := l.transport.send(ctx)
		if err != nil {
			stats = mergeTurnStats(stats, turn.stats)
			return "", stats, err
		}
		stats = mergeTurnStats(stats, turn.stats)
		if err := l.checkBudget(stats); err != nil {
			_ = l.logger.errorEvent(l.transport.sequence, err)
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
			if corrections[key] < maxProtocolCorrections(key) && i+1 < maxLoops {
				corrections[key]++
				_ = l.logger.protocolCorrection(key, prompt)
				l.transport.recordCorrection(prompt)
				continue
			}
			err := newExecutorError(errAgentProtocol, key)
			_ = l.logger.errorEvent(l.transport.sequence, err)
			return "", stats, err
		}

		usedTool = true
		for _, call := range turn.calls {
			_ = l.logger.toolCall(call)
		}
		results := l.tools.executeAll(ctx, turn.calls)
		stats.NumTurns += len(results)
		for _, result := range results {
			_ = l.logger.tool(result)
		}
		l.transport.recordToolResults(results)
	}
	err := newExecutorError(errAgentIncomplete, fmt.Sprintf("tool_loop_exceeded:%d", maxLoops))
	_ = l.logger.errorEvent(l.transport.sequence, err)
	return "", stats, err
}

func (l *agentLoop) checkBudget(stats game.TurnStats) error {
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
	if l.role == "verifier" && verifierConcedes(text) {
		if missing := l.tools.missingRequiredSkills(); len(missing) > 0 {
			return fmt.Sprintf("Before conceding, read every required evaluation rubric with the skill tool. Missing required rubric skill(s): %s. Use skill action='read' for each, inspect the workspace against the rubric, then reply with a final verifier result and <status>CONCEDE</status> only if every required rubric passes.", strings.Join(missing, ", ")), "missing_required_skill_rubric"
		}
	}
	return "", ""
}

func protocolCorrectionFor(role, task, text string, usedTool bool) (string, string) {
	return protocolCorrectionForStrict(role, "", task, text, usedTool, false, true)
}

func protocolCorrectionForStrict(role, protocolMode, task, text string, usedTool bool, strict bool, toolsAvailable bool) (string, string) {
	trimmed := strings.TrimSpace(text)
	policy := loopPolicy(role, protocolMode, task)
	if trimmed == "" {
		return "Your previous response had no visible result. Use the available tools to carry out the assignment, then reply with a visible final answer that follows this turn's required output tags.", "empty_final"
	}
	if policy.requireStatus {
		if !hasStatusTag(trimmed) {
			return "Your previous response was missing the required final <status>...</status> tag. Reply with a concise final verifier result and include exactly one final <status>CONTINUE</status> or <status>CONCEDE</status> tag.", "missing_status"
		}
	}
	if policy.requireProgressUpdate {
		progress := countBlocks(progressBlockRE, trimmed)
		if strict && progress != 1 {
			return fmt.Sprintf("Your previous response must contain exactly one <progress_update>...</progress_update> block, but it had %d. Reply with a concise final implementation summary and exactly one progress_update block; do not write the tag name anywhere else.", progress), "malformed_progress_update"
		}
		if !strict && progress < 1 {
			return "Your previous response was missing the required <progress_update>...</progress_update> block. Reply with a concise final implementation summary and include exactly one <progress_update> block.", "missing_progress_update"
		}
		if toolsAvailable && policy.requireToolForArtifact && !usedTool && artifactOriented(task) {
			return "This turn appears to require inspecting or changing workspace artifacts, but no tool was used. Inspect or update the workspace with tools before finalizing, then include the required <progress_update> block.", "no_tool_for_artifact_task"
		}
	}
	if policy.requireReviewBlocks {
		reviews := countBlocks(reviewBlockRE, trimmed)
		summaries := countBlocks(summaryBlockRE, trimmed)
		if reviews != 1 || summaries != 1 {
			return fmt.Sprintf("Your previous review-mode response must contain exactly one <review>...</review> block and exactly one <summary>...</summary> block, but it had %d review and %d summary block(s). Put the CSV rubric inside a single <review> tag and your notes inside a single <summary> tag, and do not write either tag name anywhere else (including examples or narration).", reviews, summaries), "malformed_review_blocks"
		}
	}
	return "", ""
}

func hasStatusTag(text string) bool {
	if !hasExactTag(text, "status") {
		return false
	}
	value, ok := tagValue(text, "status")
	if !ok {
		return false
	}
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case string(game.StatusContinue), string(game.StatusConcede):
		return true
	default:
		return false
	}
}

func hasExactTag(text, tag string) bool {
	return strings.Count(text, "<"+tag+">") == 1 && strings.Count(text, "</"+tag+">") == 1
}

func verifierConcedes(text string) bool {
	if !hasStatusTag(text) {
		return false
	}
	value, ok := tagValue(text, "status")
	return ok && strings.EqualFold(strings.TrimSpace(value), string(game.StatusConcede))
}

func tagValue(text, tag string) (string, bool) {
	open := "<" + tag + ">"
	close := "</" + tag + ">"
	start := strings.Index(text, open)
	if start < 0 {
		return "", false
	}
	end := strings.Index(text[start+len(open):], close)
	if end < 0 {
		return "", false
	}
	return text[start+len(open) : start+len(open)+end], true
}

func artifactOriented(task string) bool {
	lower := strings.ToLower(task)
	for _, word := range []string{"file", "workspace", "code", "edit", "write", "create", "change", "fix", "test", "implement", "patch", "diff"} {
		if strings.Contains(lower, word) {
			return true
		}
	}
	return false
}

func nativeSystemPrompt(role string) string {
	return strings.Join([]string{
		"You are Telos' built-in coding agent working in the current workspace.",
		"The user message is the assignment for this turn. Act on it directly using the available tools; do not ask the operator what to do next or wait for confirmation before reading or editing files the task needs.",
		"If the assignment names files to create or change, make those changes in the workspace before summarizing.",
		"Keep your answer in visible assistant text rather than only in hidden reasoning. End with a concise summary of what you changed and any checks you ran, plus any response-format tags the assignment asks for.",
		fmt.Sprintf("Your role for this turn is %s.", role),
	}, "\n")
}

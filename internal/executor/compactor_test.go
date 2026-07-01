package executor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/openai/openai-go/responses"
)

func largeFunctionOutputItem(callID string, size int) responses.ResponseInputItemUnionParam {
	return responses.ResponseInputItemParamOfFunctionCallOutput(callID, fillerText(size))
}

// fillerText returns approximately n characters of realistic, varied prose that
// tokenizes at roughly 4 chars/token. Token-budget fixtures must use this rather
// than a run of identical bytes, which the BPE tokenizer compresses heavily and
// would make a nominally "large" item count as only a handful of tokens.
func fillerText(n int) string {
	const lorem = "lorem ipsum dolor sit amet consectetur adipiscing elit sed do eiusmod "
	var b strings.Builder
	for b.Len() < n {
		b.WriteString(lorem)
	}
	return b.String()[:n]
}

func tightBudgetConfig() compactionConfig {
	return compactionConfig{contextWindow: 400, triggerRatio: 0.5, keepRecentTokens: 80}
}

func TestCompactionConfigFromEnvDefaultsAreOn(t *testing.T) {
	cfg := compactionConfigFromEnv(4096, 0)
	if cfg.contextWindow != defaultCompactionContextWindow || cfg.triggerRatio != defaultCompactionTriggerRatio {
		t.Fatalf("defaults: got window=%d ratio=%v", cfg.contextWindow, cfg.triggerRatio)
	}
	if cfg.keepRecentTokens != defaultCompactionKeepRecentTokens {
		t.Fatalf("keepRecentTokens: got %d", cfg.keepRecentTokens)
	}
	if cfg.reserveOutput != 4096 {
		t.Fatalf("reserveOutput: got %d", cfg.reserveOutput)
	}
	if cfg.budgetTokens() <= 0 {
		t.Fatal("default budget should be positive")
	}
}

func TestCompactionConfigFromEnvOverrides(t *testing.T) {
	t.Setenv("TELOS_AUTOCOMPACT_CONTEXT_WINDOW", "32000")
	t.Setenv("TELOS_AUTOCOMPACT_TRIGGER_RATIO", "0.5")
	t.Setenv("TELOS_AUTOCOMPACT_KEEP_RECENT_TOKENS", "4096")
	t.Setenv("TELOS_AUTOCOMPACT_STRATEGY", "truncate")

	cfg := compactionConfigFromEnv(2000, 0)
	if cfg.contextWindow != 32000 || cfg.triggerRatio != 0.5 || cfg.keepRecentTokens != 4096 || cfg.strategy != compactionStrategyTruncate {
		t.Fatalf("overrides: %#v", cfg)
	}
	if want := int(0.5*32000) - 2000; cfg.budgetTokens() != want {
		t.Fatalf("budget: got %d want %d", cfg.budgetTokens(), want)
	}
}

func TestCompactorUnderBudgetHasNoPlan(t *testing.T) {
	s := newConversationState(responses.ResponseInputParam{messageItem("task")}, conversationStateStatelessHistory)
	s.history = append(s.history, messageItem("small"))

	_, ok, err := newCompactor(compactionConfigFromEnv(4096, 0)).plan(s)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("under-budget history should not compact")
	}
}

func TestCompactorPlansTokenBoundaryAndPreservesTask(t *testing.T) {
	s := newConversationState(responses.ResponseInputParam{messageItem("task")}, conversationStateStatelessHistory)
	s.history = responses.ResponseInputParam{messageItem("task")}
	for i := 0; i < 10; i++ {
		s.history = append(s.history, messageItem(fmt.Sprintf("old-%d %s", i, strings.Repeat("x", 300))))
	}
	s.history = append(s.history, messageItem("recent user turn"), messageItem(strings.Repeat("r", 300)))

	plan, ok, err := newCompactor(tightBudgetConfig()).plan(s)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("over-budget history should compact")
	}
	if plan.firstKeptIndex <= 1 || plan.firstKeptIndex > len(s.history) {
		t.Fatalf("firstKeptIndex should summarize old history at a safe boundary: %#v len=%d", plan, len(s.history))
	}
	if plan.itemsSummarized != plan.firstKeptIndex-1 || plan.itemsKept != len(s.history)-plan.firstKeptIndex {
		t.Fatalf("plan counts: %#v", plan)
	}
	req := s.compactionRequestInput(plan.firstKeptIndex, 500)
	if req[0].OfMessage.Content.OfString.Value != "task" {
		t.Fatal("compaction request must preserve the original task as item 0")
	}
	if req[len(req)-1].OfMessage == nil || !strings.Contains(req[len(req)-1].OfMessage.Content.OfString.Value, compactionCommand) {
		t.Fatal("compaction request must append the late COMPACT_SESSION_STATE message")
	}
}

func TestRepeatedCompactionSummarizesOnlyNewRawSpan(t *testing.T) {
	s := newConversationState(responses.ResponseInputParam{messageItem("task")}, conversationStateStatelessHistory)
	s.history = responses.ResponseInputParam{
		messageItem("task"),
		messageItem("summarized-1"),
		messageItem("summarized-2"),
		messageItem("new-1"),
		messageItem("new-2"),
		messageItem("recent"),
	}
	s.applyCompaction(validCompactionSummary("previous"), 3)

	req := s.compactionRequestInput(5, 500)
	body := requestText(req)
	for _, want := range []string{"Compacted prior session state", "previous", "new-1", "new-2", compactionCommand} {
		if !strings.Contains(body, want) {
			t.Fatalf("compaction request missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "summarized-1") || strings.Contains(body, "recent") {
		t.Fatalf("repeated compaction should include only the previous summary and new raw span:\n%s", body)
	}
}

func TestBoundaryRepairAvoidsOrphanFunctionOutput(t *testing.T) {
	history := responses.ResponseInputParam{
		messageItem("task"),
		functionCallItem("call_a"),
		largeFunctionOutputItem("call_a", 900),
		messageItem("recent"),
	}

	got := repairFunctionOutputBoundary(history, 2, 1)
	if got != 1 {
		t.Fatalf("boundary should shift earlier to include matching function_call: got %d", got)
	}
	kept := history[got:]
	if hasOrphanFunctionOutput(kept) {
		t.Fatalf("kept slice contains orphan output at boundary %d", got)
	}
}

func TestBoundaryRepairTerminatesWithParallelToolOutputs(t *testing.T) {
	history := responses.ResponseInputParam{
		messageItem("task"),
		functionCallItem("call_ledger"),
		functionCallItem("call_constraints"),
		functionCallItem("call_checker"),
		functionCallItem("call_list"),
		largeFunctionOutputItem("call_ledger", 3000),
		largeFunctionOutputItem("call_constraints", 200),
		largeFunctionOutputItem("call_checker", 200),
		largeFunctionOutputItem("call_list", 100),
	}

	got := repairFunctionOutputBoundary(history, 5, 2)
	if got != len(history) {
		t.Fatalf("boundary should move past outputs whose calls cannot be kept: got %d want %d", got, len(history))
	}
}

func TestEnsureSummaryRoomMovesForwardWithParallelToolOutputs(t *testing.T) {
	s := newConversationState(responses.ResponseInputParam{messageItem("task")}, conversationStateStatelessHistory)
	s.history = responses.ResponseInputParam{
		messageItem("task"),
		functionCallItem("call_initial"),
		largeFunctionOutputItem("call_initial", 3000),
	}
	s.applyCompaction(validCompactionSummary("initial"), len(s.history))
	s.history = append(s.history,
		functionCallItem("call_ledger"),
		functionCallItem("call_constraints"),
		functionCallItem("call_checker"),
		largeFunctionOutputItem("call_ledger", 6000),
		largeFunctionOutputItem("call_constraints", 200),
		largeFunctionOutputItem("call_checker", 200),
	)

	got := ensureSummaryRoom(s, 6, 1200)
	if got <= 6 {
		t.Fatalf("ensureSummaryRoom must make forward progress, got %d", got)
	}
	if hasOrphanFunctionOutput(s.history[got:]) {
		t.Fatalf("kept slice contains orphan output at boundary %d", got)
	}
}

func TestPlannerCanCompactLatestOversizedToolOutput(t *testing.T) {
	s := newConversationState(responses.ResponseInputParam{messageItem("task")}, conversationStateStatelessHistory)
	s.history = responses.ResponseInputParam{
		messageItem("task"),
		functionCallItem("call_big"),
		largeFunctionOutputItem("call_big", 8000),
	}

	plan, ok, err := newCompactor(compactionConfig{contextWindow: 1200, triggerRatio: 0.7, keepRecentTokens: 100}).plan(s)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("latest oversized output should be summarizable")
	}
	if plan.firstKeptIndex != len(s.history) {
		t.Fatalf("oversized latest item should be summarized with no recent items kept: %#v", plan)
	}
}

func TestPlannerKeepsInBudgetRecentTailInsteadOfDroppingAll(t *testing.T) {
	s := newConversationState(responses.ResponseInputParam{messageItem("task")}, conversationStateStatelessHistory)
	s.history = responses.ResponseInputParam{
		messageItem("task"),
		messageItem("old " + fillerText(12000)),
		messageItem("recent " + fillerText(4000)),
	}

	// budget = 0.5*2000 = 1000; reserved summary headroom target = 500. The
	// recent item (~630 tokens) exceeds the keep-recent threshold so the selector
	// points firstKept past it, but task+recent still fits the budget, so it must
	// be kept rather than summarized away to an empty tail.
	plan, ok, err := newCompactor(compactionConfig{contextWindow: 2000, triggerRatio: 0.5, keepRecentTokens: 20}).plan(s)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("over-budget history should compact")
	}
	if plan.firstKeptIndex != 2 || plan.itemsKept < 1 {
		t.Fatalf("in-budget recent tail should be preserved, not dropped: %#v", plan)
	}
}

func TestCompactionConfigFloorsToModelContextWindow(t *testing.T) {
	if got := compactionConfigFromEnv(0, 32000).contextWindow; got != 32000 {
		t.Fatalf("effective window should floor to the smaller model window, got %d", got)
	}
	if got := compactionConfigFromEnv(0, 1000000).contextWindow; got != defaultCompactionContextWindow {
		t.Fatalf("a larger model window must not raise the configured default, got %d", got)
	}
	if got := compactionConfigFromEnv(0, 0).contextWindow; got != defaultCompactionContextWindow {
		t.Fatalf("an unknown model window must leave the configured default, got %d", got)
	}
}

func TestCompactionConfigExplicitZeroDisablesEvenWithModelWindow(t *testing.T) {
	t.Setenv("TELOS_AUTOCOMPACT_CONTEXT_WINDOW", "0")
	cfg := compactionConfigFromEnv(0, 32000)
	if cfg.contextWindow != 0 || cfg.budgetTokens() != 0 {
		t.Fatalf("explicit zero should disable compaction, got %#v budget=%d", cfg, cfg.budgetTokens())
	}
}

func TestModelContextWindowResolution(t *testing.T) {
	builtin := map[string]int{
		"claude-opus-4-6":             200000,
		"anthropic/claude-sonnet-4.5": 200000,
		"openai/gpt-5.1":              400000,
		"openai/gpt-4o":               128000,
		"some/unknown-model":          0,
	}
	for model, want := range builtin {
		if got := builtinModelContextWindow(model); got != want {
			t.Fatalf("builtinModelContextWindow(%q)=%d want %d", model, got, want)
		}
	}
	// An explicit capability value wins over the built-in family default.
	if got := (modelCapabilityProfile{ContextWindow: 64000}).effectiveContextWindow("claude-opus-4-6"); got != 64000 {
		t.Fatalf("explicit context window should win, got %d", got)
	}
	if got := (modelCapabilityProfile{}).effectiveContextWindow("openai/gpt-5.1"); got != 400000 {
		t.Fatalf("empty profile should fall back to the built-in default, got %d", got)
	}
}

func TestValidateCompactionSummaryRejectsMalformed(t *testing.T) {
	if err := validateCompactionSummary(validCompactionSummary("ok")); err != nil {
		t.Fatalf("valid summary rejected: %v", err)
	}
	if err := validateCompactionSummary("## Goal\nmissing most headings"); err == nil {
		t.Fatal("expected malformed summary to fail")
	}
}

func TestDetailsFromCompactionSummaryExtractsCleanPaths(t *testing.T) {
	summary := strings.ReplaceAll(validCompactionSummary("ok"),
		"- internal/executor/responses.go",
		"- `corpus/ledger.txt` (partial, lines 1-400)\n- (pending) `corpus/checker.sh`")
	details := detailsFromCompactionSummary(summary)
	want := []string{"corpus/ledger.txt", "corpus/checker.sh"}
	if strings.Join(details.ReadFiles, ",") != strings.Join(want, ",") {
		t.Fatalf("read files: got %#v want %#v", details.ReadFiles, want)
	}
}

func requestText(items responses.ResponseInputParam) string {
	var b strings.Builder
	for _, item := range items {
		if item.OfMessage != nil {
			b.WriteString(item.OfMessage.Content.OfString.Value)
			b.WriteByte('\n')
		}
		if item.OfFunctionCall != nil {
			b.WriteString(item.OfFunctionCall.Name)
			b.WriteByte('\n')
		}
		if item.OfFunctionCallOutput != nil {
			b.WriteString(item.OfFunctionCallOutput.Output)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func validCompactionSummary(label string) string {
	return strings.Join([]string{
		"## Goal",
		"Continue " + label,
		"## Constraints & Preferences",
		"- Keep changes scoped",
		"## Progress",
		"- Implemented core pieces",
		"## Key Decisions",
		"- Use LLM summaries",
		"## Files Inspected",
		"- internal/executor/responses.go",
		"## Files Changed",
		"- internal/executor/compactor.go",
		"## Commands Run",
		"- go test ./internal/executor",
		"## Test Results",
		"- pending",
		"## Open Issues",
		"- none",
		"## Next Action",
		"- continue",
	}, "\n")
}

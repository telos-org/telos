package executor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/openai/openai-go/responses"
)

// realisticHistory builds a conversation roughly the size of a long coding-agent
// turn: alternating assistant messages and tool outputs containing code-like
// text, totaling about targetTokens tokens.
func realisticHistory(targetTokens int) responses.ResponseInputParam {
	codeBlob := strings.Repeat(
		"func (t *responsesClient) send(ctx context.Context) (agentTurn, error) {\n"+
			"\tcompStats, err := t.compactSessionState(ctx) // returns game.TurnStats\n"+
			"\tif err != nil { return agentTurn{stats: compStats}, fmt.Errorf(\"send: %w\", err) }\n"+
			"\tfor i := 0; i < maxLoops; i++ { results := l.tools.executeAll(ctx, calls) }\n}\n", 6)
	prose := "Inspected the repository layout and the failing test; the regression is in " +
		"the compaction boundary repair when a function output is orphaned. Plan: keep " +
		"the last coherent tail, summarize the rest, then re-run the verifier rubric.\n"

	var h responses.ResponseInputParam
	h = append(h, messageItem("task: fix the autocompaction boundary bug and add a regression test"))
	// Each item gets a unique prefix so content is distinct across the history —
	// otherwise intra-call memoization of identical items would hide the true
	// cold-encode cost.
	for i := 0; estimateHistoryTokens(h) < targetTokens; i++ {
		uniq := fmt.Sprintf("// step %d %d\n", i, len(h))
		h = append(h,
			messageItem(uniq+prose+codeBlob),
			responses.ResponseInputItemParamOfFunctionCallOutput(
				fmt.Sprintf("call_%d", len(h)), uniq+codeBlob+codeBlob),
		)
	}
	return h
}

// BenchmarkEstimateHistoryTokens measures the hot path: estimateHistoryTokens
// runs over the full conversation on every model request (compaction's budget
// check). "cold" re-encodes everything; "warm" is the steady-state cost once the
// older items are memoized, which is what actually recurs across a tool loop.
// Run with: go test -run x -bench EstimateHistoryTokens.
func BenchmarkEstimateHistoryTokens(b *testing.B) {
	for _, target := range []int{20000, 60000, 120000} {
		h := realisticHistory(target)
		tokens := estimateHistoryTokens(h)
		b.Run(fmt.Sprintf("cold/tokens=%d", tokens), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				resetTokenCache()
				_ = estimateHistoryTokens(h)
			}
		})
		b.Run(fmt.Sprintf("warm/tokens=%d", tokens), func(b *testing.B) {
			resetTokenCache()
			_ = estimateHistoryTokens(h) // prime the cache
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = estimateHistoryTokens(h)
			}
		})
	}
}

// TestTokenMemoizationIsTransparent guards that caching never changes the count.
func TestTokenMemoizationIsTransparent(t *testing.T) {
	h := realisticHistory(8000)
	resetTokenCache()
	cold := estimateHistoryTokens(h) // computes + populates
	warm := estimateHistoryTokens(h) // served from cache
	resetTokenCache()
	recomputed := estimateHistoryTokens(h)
	if cold != warm || cold != recomputed {
		t.Fatalf("memoization changed token counts: cold=%d warm=%d recomputed=%d", cold, warm, recomputed)
	}
}

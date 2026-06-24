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
	for estimateHistoryTokens(h) < targetTokens {
		h = append(h,
			messageItem(prose+codeBlob),
			responses.ResponseInputItemParamOfFunctionCallOutput(
				fmt.Sprintf("call_%d", len(h)), codeBlob+codeBlob),
		)
	}
	return h
}

// BenchmarkEstimateHistoryTokens measures the hot path: estimateHistoryTokens
// runs over the full conversation on every model request (compaction's
// budget check). Run with: go test -run x -bench EstimateHistoryTokens.
func BenchmarkEstimateHistoryTokens(b *testing.B) {
	for _, target := range []int{20000, 60000, 120000} {
		h := realisticHistory(target)
		tokens := estimateHistoryTokens(h)
		b.Run(fmt.Sprintf("tokens=%d", tokens), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_ = estimateHistoryTokens(h)
			}
		})
	}
}

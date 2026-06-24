package agentsession

import (
	"reflect"
	"testing"
)

func TestCompactionPayloadRoundTrip(t *testing.T) {
	want := CompactionPayload{
		Reason:          "token_budget",
		FirstKeptIndex:  42,
		TokensBefore:    50000,
		TokensAfter:     18000,
		SummaryTokens:   1200,
		ItemsSummarized: 41,
		ItemsKept:       9,
		Model:           "test/model",
		ResponseID:      "resp_compact",
		Usage:           ModelResponseUsage{Input: 1000, Output: 200, CacheRead: 500},
		Details:         CompactionDetails{ReadFiles: []string{"internal/executor/responses.go"}},
	}

	event := Event{Type: KindCompaction, Data: MarshalPayload(&want)}
	got, err := Unmarshal[CompactionPayload](&event)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("round trip: got %#v want %#v", *got, want)
	}
}

package game

import "testing"

func TestExtractStatus(t *testing.T) {
	tests := []struct {
		input    string
		expected AgentStatus
	}{
		{"some text\n<status>CONCEDE</status>\n", StatusConcede},
		{"some text\n<status>CONTINUE</status>\n", StatusContinue},
		{"some text\n<status>CONCEDE</status>", StatusConcede},
		{"no status tag", StatusContinue},
		{"", StatusContinue},
		{"<status>CONCEDE</status>\nmore text after", StatusContinue}, // not final
		{"line1\n<status>CONTINUE</status>\nline2\n<status>CONCEDE</status>\n", StatusConcede},
	}
	for _, tt := range tests {
		got := ExtractStatus(tt.input)
		if got != tt.expected {
			t.Errorf("ExtractStatus(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestPVGResultAccumulate(t *testing.T) {
	r := &PVGResult{SystemName: "test"}
	r.Accumulate(TurnStats{
		CostUSD:             1.5,
		InputTokens:         1000,
		OutputTokens:        500,
		CacheReadTokens:     200,
		CacheCreationTokens: 100,
	})
	r.Accumulate(TurnStats{
		CostUSD:      0.5,
		InputTokens:  500,
		OutputTokens: 250,
	})

	if r.TotalCostUSD != 2.0 {
		t.Errorf("cost: got %.2f", r.TotalCostUSD)
	}
	if r.TotalInputTokens != 1500 {
		t.Errorf("input: got %d", r.TotalInputTokens)
	}
	if r.TotalOutputTokens != 750 {
		t.Errorf("output: got %d", r.TotalOutputTokens)
	}
	if r.TotalCacheReadTokens != 200 {
		t.Errorf("cache read: got %d", r.TotalCacheReadTokens)
	}
}

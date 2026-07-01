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
		{"The literal tag <status>CONCEDE</status> is an example.", StatusContinue},
		{"line1\n<status>CONTINUE</status>\nline2\n<status>CONCEDE</status>\n", StatusConcede},
	}
	for _, tt := range tests {
		got := ExtractStatus(tt.input)
		if got != tt.expected {
			t.Errorf("ExtractStatus(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestParseFinalStatus(t *testing.T) {
	status, ok := ParseFinalStatus("done\n<status>CONTINUE</status>\n")
	if !ok || status != StatusContinue {
		t.Fatalf("ParseFinalStatus CONTINUE: got %q ok=%v", status, ok)
	}
	if status, ok := ParseFinalStatus("done\n<status>MAYBE</status>\n"); ok || status != StatusContinue {
		t.Fatalf("invalid status: got %q ok=%v", status, ok)
	}
	if status, ok := ParseFinalStatus("<status>CONCEDE</status>\ntrailing prose"); ok || status != StatusContinue {
		t.Fatalf("trailing prose should not parse: got %q ok=%v", status, ok)
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

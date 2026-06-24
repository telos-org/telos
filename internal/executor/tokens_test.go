package executor

import (
	"strings"
	"testing"
)

func TestCountTextTokensUsesRealTokenizer(t *testing.T) {
	if textTokenizer() == nil {
		t.Fatal("offline BPE tokenizer should be available; estimator would silently fall back to bytes")
	}
	if got := countTextTokens(""); got != 0 {
		t.Fatalf("empty text should be 0 tokens, got %d", got)
	}

	// A run of identical bytes compresses far below chars/4 under real BPE — this
	// is exactly why the old byte heuristic over/under-estimated. 4000 bytes is
	// 1000 chars/4 but tokenizes to ~500.
	if got := countTextTokens(strings.Repeat("x", 4000)); got >= 1000 {
		t.Fatalf("real BPE should compress repeated bytes below chars/4, got %d", got)
	}

	// Symbol-dense content (the F4 under-count case) fragments into *more* tokens
	// than chars/4: this 400-char JSON-ish string is 100 by chars/4 but ~200 by BPE.
	dense := strings.Repeat(`{"k":1},`, 50)
	if got, naive := countTextTokens(dense), len(dense)/4; got <= naive {
		t.Fatalf("BPE count (%d) should exceed the chars/4 estimate (%d) for symbol-dense text", got, naive)
	}
}

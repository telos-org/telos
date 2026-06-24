package executor

import (
	"sync"

	"github.com/pkoukk/tiktoken-go"
	tiktoken_loader "github.com/pkoukk/tiktoken-go-loader"
)

// compactionEncoding is the BPE encoding used to estimate conversation token
// counts for autocompaction. o200k_base is the modern OpenAI encoding (GPT-4o /
// GPT-5): exact for those models and a far better approximation than byte
// counting for the Claude/Gemini models Telos also routes — it does real
// subword tokenization instead of chars/4, which undercounts code by 20%+. The
// estimate drives a trigger with a safety margin and is floored to the model's
// real context window, so cross-tokenizer approximation is acceptable here.
const compactionEncoding = "o200k_base"

// perItemTokenOverhead approximates the per-message framing tokens (role,
// delimiters) each conversation item contributes on top of its content tokens.
const perItemTokenOverhead = 4

var (
	tokenizerOnce sync.Once
	tokenizer     *tiktoken.Tiktoken
)

// textTokenizer lazily builds the BPE tokenizer from the embedded (offline)
// vocabulary so it works inside a network-isolated sandbox — the default loader
// would fetch the encoding over HTTP. It is built once and shared; the
// resulting *tiktoken.Tiktoken is read-only and safe for concurrent Encode. It
// returns nil if the tokenizer cannot be built, in which case callers fall back
// to a byte heuristic — token estimation must never break compaction.
func textTokenizer() *tiktoken.Tiktoken {
	tokenizerOnce.Do(func() {
		tiktoken.SetBpeLoader(tiktoken_loader.NewOfflineLoader())
		if enc, err := tiktoken.GetEncoding(compactionEncoding); err == nil {
			tokenizer = enc
		}
	})
	return tokenizer
}

// countTextTokens returns the BPE token count of text, falling back to a
// conservative byte estimate when the tokenizer is unavailable.
func countTextTokens(text string) int {
	if text == "" {
		return 0
	}
	if enc := textTokenizer(); enc != nil {
		return len(enc.Encode(text, nil, nil))
	}
	return fallbackTextTokens(text)
}

// fallbackTextTokens is the byte heuristic used only when the BPE tokenizer is
// unavailable. It is deliberately conservative (chars/3, vs the ~4 chars/token
// of prose) so the trigger errs toward compacting early rather than overflowing.
func fallbackTextTokens(text string) int {
	return len(text)/3 + 1
}

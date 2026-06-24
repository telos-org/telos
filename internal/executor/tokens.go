package executor

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"

	_ "embed"

	"github.com/pkoukk/tiktoken-go"
)

// compactionEncoding is the BPE encoding used to estimate conversation token
// counts for autocompaction. cl100k_base is the OpenAI GPT-3.5/4 encoding: real
// subword tokenization (far better than chars/4, which mis-estimates code and
// symbol-dense content) at roughly half the vocabulary size of o200k_base. The
// estimate only drives a trigger with a safety margin, floored to the model's
// real context window, so cl100k is an adequate cross-model approximation for
// the Claude/Gemini/GPT models Telos routes and keeps the embedded vocab small.
const compactionEncoding = "cl100k_base"

// perItemTokenOverhead approximates the per-message framing tokens (role,
// delimiters) each conversation item contributes on top of its content tokens.
const perItemTokenOverhead = 4

// cl100kBaseGz is the gzip-compressed cl100k_base vocabulary, vendored so the
// tokenizer works inside the network-isolated executor sandbox without the
// default HTTP loader. Source: OpenAI's cl100k_base.tiktoken (the same file
// shipped by github.com/pkoukk/tiktoken-go-loader). It is gzip'd because the
// raw base64 vocab is ~1.6 MB; compressed it is ~0.74 MB and is inflated once
// at startup.
//
//go:embed assets/cl100k_base.tiktoken.gz
var cl100kBaseGz []byte

// embeddedBpeLoader satisfies tiktoken.BpeLoader from the embedded gzip vocab
// instead of fetching it over HTTP. tiktoken passes the canonical blob URL; we
// serve the one encoding we vendor and reject anything else so a mismatch is
// loud rather than a silent wrong-vocab.
type embeddedBpeLoader struct{}

func (embeddedBpeLoader) LoadTiktokenBpe(tiktokenBpeFile string) (map[string]int, error) {
	if !strings.Contains(tiktokenBpeFile, compactionEncoding) {
		return nil, fmt.Errorf("embedded tokenizer only vendors %s, not %q", compactionEncoding, tiktokenBpeFile)
	}
	gz, err := gzip.NewReader(bytes.NewReader(cl100kBaseGz))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	contents, err := io.ReadAll(gz)
	if err != nil {
		return nil, err
	}
	ranks := make(map[string]int)
	for _, line := range strings.Split(string(contents), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("malformed vocab line %q", line)
		}
		token, err := base64.StdEncoding.DecodeString(parts[0])
		if err != nil {
			return nil, err
		}
		rank, err := strconv.Atoi(parts[1])
		if err != nil {
			return nil, err
		}
		ranks[string(token)] = rank
	}
	return ranks, nil
}

var (
	tokenizerOnce sync.Once
	tokenizer     *tiktoken.Tiktoken
)

// textTokenizer lazily builds the BPE tokenizer from the embedded (gzip'd)
// vocabulary so it works inside a network-isolated sandbox — the default loader
// would fetch the encoding over HTTP. It is built once and shared; the resulting
// *tiktoken.Tiktoken is read-only and safe for concurrent Encode. It returns nil
// if the tokenizer cannot be built, in which case callers fall back to a byte
// heuristic — token estimation must never break compaction.
func textTokenizer() *tiktoken.Tiktoken {
	tokenizerOnce.Do(func() {
		tiktoken.SetBpeLoader(embeddedBpeLoader{})
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

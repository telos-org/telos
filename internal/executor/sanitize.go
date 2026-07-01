package executor

import (
	"regexp"
	"strings"
)

var reasoningLeakRE = regexp.MustCompile(`(?is)<(?:think|thinking|reasoning)\b[^>]*>.*?</(?:think|thinking|reasoning)>`)
var reasoningOpenRE = regexp.MustCompile(`(?is)<(think|thinking|reasoning)\b[^>]*>`)
var reasoningCloseRE = regexp.MustCompile(`(?is)</(think|thinking|reasoning)>`)

// protocolBlockRE matches any required response tag the loop validates against.
// The lossy unbalanced-tail stripping below must never discard a region that
// carries one of these, or it would manufacture the very malformed_review_blocks
// / missing_progress_update failures it is meant to avoid.
var protocolBlockRE = regexp.MustCompile(`(?is)<(?:/?)(?:findings|review|summary|progress_update|status)\b[^>]*>`)

func containsProtocolBlock(text string) bool {
	return protocolBlockRE.MatchString(text)
}

// sanitizeVisibleText strips reasoning/COT tags from visible model output. When
// keepReasoning is true (TELOS_NATIVE_KEEP_REASONING=1, resolved once into
// envKnobs) the text is returned untouched.
func sanitizeVisibleText(text string, keepReasoning bool) (string, string) {
	if keepReasoning {
		return text, ""
	}
	var removed []string
	sanitized := reasoningLeakRE.ReplaceAllStringFunc(text, func(match string) string {
		removed = append(removed, match)
		return ""
	})
	// The unbalanced-tag handling below is lossy: a lone open/close reasoning tag
	// drops everything before/after it. That is acceptable for a genuine
	// reasoning leak, but it must never eat a required protocol block — doing so
	// would synthesize the malformed_review_blocks / missing_progress_update
	// failures the validator then reports. So skip the strip whenever the region
	// it would discard carries a protocol tag, and keep the answer instead.
	// (TELOS_NATIVE_KEEP_REASONING=1 disables this stripping entirely.)
	if locs := reasoningCloseRE.FindAllStringIndex(sanitized, -1); len(locs) > 0 {
		last := locs[len(locs)-1]
		if !containsProtocolBlock(sanitized[:last[1]]) {
			removed = append(removed, sanitized[:last[1]])
			sanitized = sanitized[last[1]:]
		}
	}
	if loc := reasoningOpenRE.FindStringIndex(sanitized); loc != nil {
		if !containsProtocolBlock(sanitized[loc[0]:]) {
			removed = append(removed, sanitized[loc[0]:])
			sanitized = sanitized[:loc[0]]
		}
	}
	if len(removed) == 0 {
		return text, ""
	}
	return strings.TrimSpace(sanitized), strings.Join(removed, "\n")
}

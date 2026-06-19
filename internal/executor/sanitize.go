package executor

import (
	"regexp"
	"strings"
)

var reasoningLeakRE = regexp.MustCompile(`(?is)<(?:think|thinking|reasoning)\b[^>]*>.*?</(?:think|thinking|reasoning)>`)
var reasoningOpenRE = regexp.MustCompile(`(?is)<(think|thinking|reasoning)\b[^>]*>`)
var reasoningCloseRE = regexp.MustCompile(`(?is)</(think|thinking|reasoning)>`)

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
	// TODO: the unbalanced-tag handling below is lossy. A final answer that
	// legitimately contains a lone "antml:think" or "antml:think" substring (e.g. a
	// coding task about parsing these tags) loses everything before/after it.
	// Only strip unbalanced tails when there is corroborating evidence of a real
	// reasoning leak; until then TELOS_NATIVE_KEEP_REASONING=1 disables this.
	if locs := reasoningCloseRE.FindAllStringIndex(sanitized, -1); len(locs) > 0 {
		last := locs[len(locs)-1]
		removed = append(removed, sanitized[:last[1]])
		sanitized = sanitized[last[1]:]
	}
	if loc := reasoningOpenRE.FindStringIndex(sanitized); loc != nil {
		removed = append(removed, sanitized[loc[0]:])
		sanitized = sanitized[:loc[0]]
	}
	if len(removed) == 0 {
		return text, ""
	}
	return strings.TrimSpace(sanitized), strings.Join(removed, "\n")
}

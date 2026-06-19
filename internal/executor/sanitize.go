package executor

import (
	"os"
	"regexp"
	"strings"
)

var reasoningLeakRE = regexp.MustCompile(`(?is)<(?:think|thinking|reasoning)\b[^>]*>.*?</(?:think|thinking|reasoning)>`)
var reasoningOpenRE = regexp.MustCompile(`(?is)<(think|thinking|reasoning)\b[^>]*>`)
var reasoningCloseRE = regexp.MustCompile(`(?is)</(think|thinking|reasoning)>`)

func sanitizeVisibleText(text string) (string, string) {
	if strings.TrimSpace(os.Getenv("TELOS_NATIVE_KEEP_REASONING")) == "1" {
		return text, ""
	}
	var removed []string
	sanitized := reasoningLeakRE.ReplaceAllStringFunc(text, func(match string) string {
		removed = append(removed, match)
		return ""
	})
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

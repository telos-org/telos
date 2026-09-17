package game

import (
	"regexp"
	"strings"
)

var userUpdateTagRE = regexp.MustCompile(`</?[A-Za-z][A-Za-z0-9:_-]*(?:\s+[A-Za-z_:][A-Za-z0-9:_.-]*\s*=\s*(?:"[^"]*"|'[^']*'|[^\s"'=<>]+))*\s*/?>`)

// SplitUserUpdates separates presentation from the technical response. Fenced
// and indented examples stay technical. An unclosed presentation block is
// quarantined through the end of the message; its contents cannot set status.
// The complete original message remains in the raw Pi session artifact.
// Each complete text block is a presentation-line boundary, not a streaming
// delta. Fence and quarantine state span blocks; retained technical pieces
// keep their original concatenation without added separators.
func SplitUserUpdates(blocks ...string) (string, []string) {
	var technical, pending strings.Builder
	var updates []string
	var fence byte
	fenceSize := 0
	inUpdate := false
	depth := 0
	for _, text := range blocks {
		for _, line := range strings.SplitAfter(text, "\n") {
			trimmed := strings.TrimSpace(line)
			if inUpdate {
				pending.WriteString(line)
			} else {
				indented := strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t")
				if !indented && len(trimmed) >= 3 && (trimmed[0] == '`' || trimmed[0] == '~') {
					size := 0
					for size < len(trimmed) && trimmed[size] == trimmed[0] {
						size++
					}
					if fence == 0 && size >= 3 && (trimmed[0] != '`' || !strings.ContainsRune(trimmed[size:], '`')) {
						fence, fenceSize = trimmed[0], size
					} else if trimmed[0] == fence && size >= fenceSize && strings.TrimSpace(trimmed[size:]) == "" {
						fence, fenceSize = 0, 0
					}
					technical.WriteString(line)
					continue
				}
				if fence != 0 || !strings.HasPrefix(line, "<user_update>") {
					technical.WriteString(line)
					continue
				}
				inUpdate = true
				pending.WriteString(strings.TrimPrefix(trimmed, "<user_update>"))
				pending.WriteByte('\n')
			}
			// Malformed nested regions still belong to presentation. Their status
			// tags must not escape into the technical response after an inner close.
			depth += strings.Count(line, "<user_update>") - strings.Count(line, "</user_update>")
			if depth > 0 {
				continue
			}
			body, suffix, closed := strings.Cut(pending.String(), "</user_update>")
			if !closed {
				continue
			}
			body = strings.Join(strings.Fields(body), " ")
			if strings.TrimSpace(suffix) == "" && body != "" && len(body) <= 1200 && !userUpdateTagRE.MatchString(body) {
				updates = append(updates, body)
			}
			pending.Reset()
			inUpdate = false
			depth = 0
		}
	}
	return technical.String(), updates
}

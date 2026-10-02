package main

import (
	"fmt"
	"strings"
)

// Cloud's merge inputs keep their original meaning: current is deployed and
// proposed is the request. Only the editable text uses the web/editor ordering.
func requestConflictText(value string) (string, error) {
	var result strings.Builder
	start, currentStart, currentEnd, proposedStart := -1, 0, -1, -1
	offset, copied := 0, 0
	newline := "\n"
	invalid := func() (string, error) {
		return "", fmt.Errorf("invalid text conflict markers; local files have not been changed")
	}
	for _, line := range strings.SplitAfter(value, "\n") {
		switch mergeMarker(line) {
		case '<':
			if start >= 0 || strings.TrimRight(line, "\r\n") != "<<<<<<< Current deployment" {
				return invalid()
			}
			start, currentStart = offset, offset+len(line)
			currentEnd, proposedStart = -1, -1
			newline = "\n"
			if strings.HasSuffix(line, "\r\n") {
				newline = "\r\n"
			}
		case '|':
			if start < 0 || currentEnd >= 0 || proposedStart >= 0 {
				return invalid()
			}
			currentEnd = offset
		case '=':
			if start >= 0 {
				if proposedStart >= 0 {
					return invalid()
				}
				if currentEnd < 0 {
					currentEnd = offset
				}
				proposedStart = offset + len(line)
			}
		case '>':
			if start < 0 || proposedStart < 0 || strings.TrimRight(line, "\r\n") != ">>>>>>> Your proposed changes" {
				return invalid()
			}
			result.WriteString(value[copied:start])
			result.WriteString("<<<<<<< Proposed version (current change)" + newline)
			result.WriteString(value[proposedStart:offset])
			result.WriteString("=======" + newline)
			result.WriteString(value[currentStart:currentEnd])
			result.WriteString(">>>>>>> Deployed version (incoming change)")
			// Preserve the closing marker's line ending, including no final newline.
			result.WriteString(line[len(strings.TrimRight(line, "\r\n")):])
			copied, start = offset+len(line), -1
		}
		offset += len(line)
	}
	if start >= 0 {
		return invalid()
	}
	result.WriteString(value[copied:])
	return result.String(), nil
}

func mergeMarker(line string) byte {
	line = strings.TrimRight(line, "\r\n")
	for _, marker := range []string{"<<<<<<<", "|||||||", "=======", ">>>>>>>"} {
		if !strings.HasPrefix(line, marker) {
			continue
		}
		if marker[0] == '=' && line != marker {
			continue
		}
		rest := strings.TrimLeft(line, marker[:1])
		if rest == "" || (marker[0] != '=' && (rest[0] == ' ' || rest[0] == '\t')) {
			return marker[0]
		}
	}
	return 0
}

func containsMergeMarkers(value string) bool {
	for _, line := range strings.Split(value, "\n") {
		switch mergeMarker(line) {
		case '<', '|', '>':
			return true
		}
	}
	// A line of equals signs alone is also a valid Markdown heading underline.
	return false
}

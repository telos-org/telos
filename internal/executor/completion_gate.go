package executor

import (
	"os"
	"regexp"
	"strings"
)

// The completion gate decides whether a model's tool-less "final" answer should
// be accepted or rejected and re-prompted. It runs on observable *signals* —
// empty output, asking the operator for direction, or a named deliverable that
// still does not exist and was not worked on this turn — rather than on
// open-ended prose pattern matching.

type gateMode string

const (
	gateSignal gateMode = "signal"
	gateOff    gateMode = "off"
)

type completionGate struct {
	mode gateMode
}

// newCompletionGate reads TELOS_NATIVE_COMPLETION_GATE: "signal" (default) or
// "off" (never re-prompt; a tool-less final is always accepted).
func newCompletionGate() completionGate {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("TELOS_NATIVE_COMPLETION_GATE"))) {
	case "off":
		return completionGate{mode: gateOff}
	default:
		return completionGate{mode: gateSignal}
	}
}

// completionSignals are the observable facts about a tool-less final turn.
type completionSignals struct {
	emptyText       bool // sanitized visible final is empty
	askedOperator   bool // final asks the human what to build / how to help
	fileDeliverable bool // task names a deliverable the agent must produce
	deliverablesMet bool // every named deliverable now exists in the workspace
	mutatedThisTurn bool // a mutating tool (write/edit/bash) ran this turn
}

// retryReason returns a short reason when the final should be rejected and
// re-prompted, or "" to accept it. The reason is recorded for telemetry.
func (g completionGate) retryReason(s completionSignals) string {
	if g.mode == gateOff {
		return ""
	}
	if s.emptyText {
		return "empty_visible_final"
	}
	if s.askedOperator {
		return "asked_operator_for_direction"
	}
	// "bad no-generation": the task requires a deliverable, it still is not on
	// disk, and nothing was changed this turn. A "good no-generation" — the
	// deliverable already exists, or the turn did real work, or the task names
	// no deliverable at all — falls through and is accepted.
	if s.fileDeliverable && !s.deliverablesMet && !s.mutatedThisTurn {
		return "named_deliverable_absent_no_change"
	}
	return ""
}

// looksLikeOperatorPrompt detects a final that punts the assignment back to the
// human. These markers are high precision: an autonomous benchmark agent should
// never ask the operator what to build.
func looksLikeOperatorPrompt(text string) bool {
	normalized := strings.ToLower(strings.TrimSpace(text))
	for _, marker := range []string{
		"what would you like me to work on",
		"what would you like me to do",
		"what would you like me to build",
		"how can i help you",
		"just describe what you need",
		"ready to help",
		"workspace is currently empty",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

// anyMutatingResult reports whether a tool batch contained a successful
// workspace-mutating call.
func anyMutatingResult(results []nativeToolResult) bool {
	for _, result := range results {
		if result.IsError {
			continue
		}
		switch result.Name {
		case "write", "edit", "bash":
			return true
		}
	}
	return false
}

// assignmentFileAnchors extracts backtick-quoted file paths named in the task,
// used to identify a file-producing assignment and to check deliverables.
func assignmentFileAnchors(task string) []string {
	re := regexp.MustCompile("`([^`]+\\.(?:py|js|ts|tsx|jsx|go|rs|java|rb|php|sh|bash|md|json|yaml|yml|toml|xml|html|css|sql|txt))`")
	seen := map[string]bool{}
	var anchors []string
	for _, match := range re.FindAllStringSubmatch(task, -1) {
		anchor := strings.TrimSpace(match[1])
		if anchor == "" || seen[anchor] {
			continue
		}
		seen[anchor] = true
		anchors = append(anchors, anchor)
	}
	return anchors
}

func nativeCorrectionPrompt(task string) string {
	return strings.Join([]string{
		"The assignment is already fully specified above. Do not ask what to build or what to do next.",
		"Implement the deliverable named in the assignment now. If the workspace is empty, create the required files directly.",
		"",
		"# Assignment",
		"",
		task,
	}, "\n")
}

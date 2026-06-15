package executor

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/telos-org/telos/internal/game"
)

const (
	defaultMaxToolLoops    = 80
	defaultMaxOutputTokens = 4096
)

func nativeMaxToolLoops() int {
	raw := strings.TrimSpace(os.Getenv("TELOS_NATIVE_MAX_TOOL_LOOPS"))
	if raw == "" {
		return defaultMaxToolLoops
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return defaultMaxToolLoops
	}
	return n
}

func nativeMaxOutputTokens() int {
	raw := strings.TrimSpace(os.Getenv("TELOS_NATIVE_MAX_OUTPUT_TOKENS"))
	if raw == "" {
		return defaultMaxOutputTokens
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 256 {
		return defaultMaxOutputTokens
	}
	return n
}

// agentTurn is one model response, normalized across providers.
type agentTurn struct {
	text       string
	calls      []nativeToolCall
	stopReason string
	stats      game.TurnStats
}

// transport owns the provider-specific wire format and conversation state. The
// agent loop drives it without knowing which provider is underneath.
type transport interface {
	// send issues one model call against the accumulated conversation state.
	send(ctx context.Context) (agentTurn, error)
	// recordToolResults threads tool output back into the conversation.
	recordToolResults(results []nativeToolResult)
	// recordCorrection appends a retry prompt for an unproductive final.
	recordCorrection(prompt string)
}

type agentLoop struct {
	transport transport
	tools     *nativeTools
	logger    *nativeSessionLogger
	task      string
	provider  string
	model     string
}

func newAgentLoop(poster httpPoster, cfg nativeProviderConfig, thinking string, tools *nativeTools, logger *nativeSessionLogger, task, role string) *agentLoop {
	maxOut := nativeMaxOutputTokens()
	var tr transport
	switch cfg.Style {
	case providerResponses:
		tr = newResponsesTransport(poster, cfg.Model, thinking, maxOut, task, role)
	case providerAnthropic:
		tr = newAnthropicTransport(poster, cfg.Model, maxOut, task, role)
	default:
		tr = newChatTransport(poster, cfg.Model, maxOut, task, role)
	}
	return &agentLoop{
		transport: tr,
		tools:     tools,
		logger:    logger,
		task:      task,
		provider:  cfg.Provider,
		model:     cfg.Model,
	}
}

func (l *agentLoop) run(ctx context.Context) (string, game.TurnStats, error) {
	maxLoops := nativeMaxToolLoops()
	stats := game.TurnStats{Model: l.model}
	usedTools := false
	for i := 0; i < maxLoops; i++ {
		turn, err := l.transport.send(ctx)
		if err != nil {
			return "", stats, err
		}
		stats = mergeTurnStats(stats, turn.stats)
		_ = l.logger.assistant(turn.text, l.provider, l.model, turn.stopReason, turn.stats)

		if len(turn.calls) == 0 {
			if i+1 < maxLoops && shouldRetryNativeFinal(turn.text, l.task, turn.stopReason, usedTools) {
				l.transport.recordCorrection(nativeCorrectionPrompt(l.task))
				continue
			}
			return turn.text, stats, nil
		}

		results := l.tools.executeAll(ctx, turn.calls)
		usedTools = true
		stats.NumTurns += len(results)
		for _, result := range results {
			_ = l.logger.tool(result)
		}
		l.transport.recordToolResults(results)
	}
	return "", stats, fmt.Errorf("agent_tool_loop_exceeded:%d", maxLoops)
}

// -- Prompts -----------------------------------------------------------------

func nativeSystemPrompt(role string) string {
	return strings.Join([]string{
		"You are Telos' built-in coding harness running inside the benchmark workspace.",
		"The user message is the complete assignment for this turn. Do not ask the operator what to build or what to do next.",
		"If the spec names a deliverable, implement that deliverable exactly. An empty or minimal workspace means you should create the required files, not switch to a generic sample task.",
		"Keep your work anchored to the current spec text and live files. Ignore unrelated task ideas, default assistant personas, and prior benchmark examples that are not present in the current spec.",
		"Use the available tools directly. Do not ask for permission before inspecting or changing files required by the task.",
		"Prefer the first useful concrete workspace mutation over long planning. For file-producing tasks, create or edit the required files before summarizing.",
		fmt.Sprintf("Available tool names are %s. Do not call unavailable tool names such as write_file, ReadFile, Edit, apply_patch, or shell.", oxfordList(nativeToolNames())),
		"Keep all actionable content in visible assistant text. Do not put the final answer only in hidden reasoning.",
		"After tool work, end with a concise visible final response listing changed files and checks run. Include any XML tags required by the Telos turn instructions.",
		fmt.Sprintf("Current Telos role: %s.", role),
	}, "\n")
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

func shouldRetryNativeFinal(text, task, stopReason string, usedTools bool) bool {
	if shouldRetryUnproductiveFinal(text, task, usedTools) {
		return true
	}
	if !usedTools && isLengthStop(stopReason) && len(assignmentFileAnchors(task)) > 0 {
		return true
	}
	return false
}

func isLengthStop(stopReason string) bool {
	switch strings.ToLower(strings.TrimSpace(stopReason)) {
	case "length", "max_tokens", "max_output_tokens":
		return true
	default:
		return false
	}
}

func shouldRetryUnproductiveFinal(text, task string, usedTools bool) bool {
	normalized := strings.ToLower(strings.TrimSpace(text))
	if normalized == "" {
		return true
	}
	if looksLikePendingWorkFinal(normalized) {
		return true
	}
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
	anchors := assignmentFileAnchors(task)
	if !usedTools && len(anchors) > 0 {
		for _, anchor := range anchors {
			if strings.Contains(normalized, strings.ToLower(anchor)) {
				return false
			}
		}
		return true
	}
	return false
}

func looksLikePendingWorkFinal(normalized string) bool {
	for _, marker := range []string{
		"will now implement",
		"will now extend",
		"will now add",
		"will now write",
		"i will now implement",
		"i will now add",
		"i will now write",
		"i will implement",
		"i will add",
		"i will write",
		"i'll now implement",
		"i'll now add",
		"i'll now write",
		"i'll implement",
		"i'll add",
		"i'll write",
		"let's implement",
		"let's extend",
		"let's add",
		"let's write",
		"let me now implement",
		"let me now add",
		"let me now write",
		"let me implement",
		"let me add",
		"let me write",
		"let me code this up",
		"now implement the",
		"need to add",
		"need to implement",
		"need to write",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

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

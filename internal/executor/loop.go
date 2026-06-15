package executor

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/telos-org/telos/internal/game"
)

const (
	defaultMaxToolLoops    = 160
	defaultMaxOutputTokens = 8192
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
	gate      completionGate
	task      string
	role      string
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
		gate:      newCompletionGate(),
		task:      task,
		role:      role,
		provider:  cfg.Provider,
		model:     cfg.Model,
	}
}

func (l *agentLoop) run(ctx context.Context) (string, game.TurnStats, error) {
	maxLoops := nativeMaxToolLoops()
	stats := game.TurnStats{Model: l.model}

	anchors := assignmentFileAnchors(l.task)
	// Deliverable enforcement applies only to the implementer (prover): a
	// verifier turn produces judgment text, not files.
	fileDeliverable := l.role == "prover" && len(anchors) > 0
	mutated := false

	for i := 0; i < maxLoops; i++ {
		turn, err := l.transport.send(ctx)
		if err != nil {
			return "", stats, err
		}
		stats = mergeTurnStats(stats, turn.stats)
		_ = l.logger.assistant(turn.text, l.provider, l.model, turn.stopReason, turn.stats)

		if len(turn.calls) == 0 {
			sig := completionSignals{
				emptyText:       strings.TrimSpace(turn.text) == "",
				askedOperator:   looksLikeOperatorPrompt(turn.text),
				fileDeliverable: fileDeliverable,
				mutatedThisTurn: mutated,
			}
			if fileDeliverable {
				sig.deliverablesMet = l.tools.deliverablesPresent(anchors)
			}
			if reason := l.gate.retryReason(sig); reason != "" && i+1 < maxLoops {
				_ = l.logger.note("completion_gate_retry", reason)
				l.transport.recordCorrection(nativeCorrectionPrompt(l.task))
				continue
			}
			return turn.text, stats, nil
		}

		results := l.tools.executeAll(ctx, turn.calls)
		if !mutated {
			mutated = anyMutatingResult(results)
		}
		stats.NumTurns += len(results)
		for _, result := range results {
			_ = l.logger.tool(result)
		}
		l.transport.recordToolResults(results)
	}
	return "", stats, fmt.Errorf("agent_tool_loop_exceeded:%d", maxLoops)
}

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

package executor

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/telos-org/telos/internal/game"
)

const (
	defaultMaxToolLoops    = 160
	defaultMaxOutputTokens = 16384
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

// agentTurn is one model response, lifted out of the openai-go types into the
// shape the agent loop drives.
type agentTurn struct {
	text       string
	calls      []nativeToolCall
	stopReason string
	stats      game.TurnStats
}

type agentLoop struct {
	transport *openaiTransport
	tools     *nativeTools
	logger    *nativeSessionLogger
	gate      completionGate
	task      string
	role      string
	provider  string
	model     string
}

func newAgentLoop(httpClient *http.Client, cfg nativeProviderConfig, thinking string, tools *nativeTools, logger *nativeSessionLogger, task, role string) *agentLoop {
	maxOut := nativeMaxOutputTokens()
	tr := newOpenAITransport(httpClient, cfg, thinking, maxOut, task, role)
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
		"You are Telos' built-in coding agent working in the current workspace.",
		"The user message is the assignment for this turn. Act on it directly using the available tools; do not ask the operator what to do next or wait for confirmation before reading or editing files the task needs.",
		"If the assignment names files to create or change, make those changes in the workspace before summarizing.",
		"Keep your answer in visible assistant text rather than only in hidden reasoning. End with a concise summary of what you changed and any checks you ran, plus any response-format tags the assignment asks for.",
		fmt.Sprintf("Your role for this turn is %s.", role),
	}, "\n")
}

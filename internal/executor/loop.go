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
		task:      task,
		role:      role,
		provider:  cfg.Provider,
		model:     cfg.Model,
	}
}

func (l *agentLoop) run(ctx context.Context) (string, game.TurnStats, error) {
	maxLoops := nativeMaxToolLoops()
	stats := game.TurnStats{Model: l.model}
	nudged := false

	for i := 0; i < maxLoops; i++ {
		turn, err := l.transport.send(ctx)
		if err != nil {
			return "", stats, err
		}
		stats = mergeTurnStats(stats, turn.stats)
		_ = l.logger.assistant(turn.text, l.provider, l.model, turn.stopReason, turn.stats)

		if len(turn.calls) == 0 {
			// A tool-less turn with no visible text is unusable. Nudge once toward
			// a visible answer before accepting it; otherwise take the final.
			if strings.TrimSpace(turn.text) == "" && !nudged && i+1 < maxLoops {
				nudged = true
				_ = l.logger.note("empty_final_retry", "model returned an empty visible final")
				l.transport.recordCorrection(nativeCorrectionPrompt())
				continue
			}
			return turn.text, stats, nil
		}

		results := l.tools.executeAll(ctx, turn.calls)
		stats.NumTurns += len(results)
		for _, result := range results {
			_ = l.logger.tool(result)
		}
		l.transport.recordToolResults(results)
	}
	return "", stats, fmt.Errorf("agent_tool_loop_exceeded:%d", maxLoops)
}

// nativeCorrectionPrompt nudges the model when a turn produced no visible answer.
// The assignment is already in context (server-side via the response chain), so
// this only asks for a usable result rather than restating the task.
func nativeCorrectionPrompt() string {
	return "Your previous response had no visible result. Use the available tools to carry out the assignment, then reply with a visible summary of what you did."
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

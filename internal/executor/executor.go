// Package executor provides Telos' built-in native coding-agent executor.
package executor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/telos-org/telos/internal/game"
	"github.com/telos-org/telos/internal/platform"
)

// NativeExecutor runs one PVG turn with Telos' built-in coding harness.
type NativeExecutor struct {
	Platform  *platform.LocalPlatform
	Model     string
	Thinking  string
	Timeout   int
	Client    *http.Client
	config    nativeConfig
	configErr error
}

// NewNativeExecutor creates a native Go coding-agent executor. The provider,
// pricing, and capability configuration is resolved once from the environment
// here and reused for every turn — no env parsing happens per turn or per
// model response. If the config is invalid the error is stored and surfaced
// as a terminal error on the first ExecuteTurn call.
func NewNativeExecutor(p *platform.LocalPlatform, model, thinking string, timeout int) *NativeExecutor {
	if thinking == "" {
		thinking = "medium"
	}
	cfg, err := resolveNativeConfig()
	return &NativeExecutor{
		Platform:  p,
		Model:     model,
		Thinking:  thinking,
		Timeout:   timeout,
		Client:    http.DefaultClient,
		config:    cfg,
		configErr: err,
	}
}

// ExecuteTurn runs one Telos-native agent turn. The role is read from
// turnState.Role so it cannot disagree with the rest of the turn state.
func (ne *NativeExecutor) ExecuteTurn(task string, turnState *game.TurnState) game.TurnResult {
	started := time.Now()
	stats := game.TurnStats{Model: ne.Model}
	role := ""
	sessionPath := ""
	var stopRequested func() bool
	var budget game.TurnBudget
	protocolMode := ""
	var skills []game.TurnSkill
	if turnState != nil {
		role = turnState.Role
		sessionPath = turnState.SessionPath()
		stopRequested = turnState.StopRequested
		budget = turnState.Budget
		protocolMode = turnState.ProtocolMode
		skills = turnState.Skills
	}

	timeout := effectiveTurnTimeout(ne.Timeout, budget)
	ctx, cancel := turnContext(timeout, stopRequested)
	defer cancel()

	logger := newNativeSessionLogger(sessionPath, ne.Platform.Workspace)
	if err := logger.start(); err != nil {
		return recoverableTurn(role, stats, newExecutorError(errToolInfra, "native_session_unavailable:"+err.Error()).Error())
	}
	_ = logger.user(task)
	_ = logger.contextPack(task)

	if ne.configErr != nil {
		execErr := newExecutorError(errConfig, ne.configErr.Error())
		_ = logger.errorEvent(0, execErr)
		return terminalTurn(role, stats, execErr.Error())
	}
	cfg, err := ne.config.providerFor(ne.Model)
	if err != nil {
		execErr := newExecutorError(errConfig, err.Error())
		_ = logger.errorEvent(0, execErr)
		return terminalTurn(role, stats, execErr.Error())
	}
	knobs := resolveEnvKnobs()
	_ = logger.providerConfig(cfg)
	_ = logger.knobs(knobs)
	_ = logger.turnPolicy(role, protocolMode)
	_ = logger.budget(effectiveMaxToolLoops(budget), effectiveMaxOutputTokens(cfg, budget), budget)

	tools := newNativeTools(ne.Platform, stopRequested, skills, logger, knobs)
	loop := newAgentLoop(ne.Client, cfg, ne.Thinking, tools, logger, task, role, protocolMode, budget, knobs)

	logs, extraStats, err := loop.run(ctx)
	stats = mergeTurnStats(stats, extraStats)
	stats.DurationMS = int(time.Since(started).Milliseconds())
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			execErr := newExecutorError(errProviderTimeout, fmt.Sprintf("turn_timeout:%d", timeout))
			if !executorErrorHasCode(err, errProviderTimeout) {
				_ = logger.errorEvent(loop.client.sequence, execErr)
			}
			return recoverableTurn(role, stats, execErr.Error())
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			execErr := newExecutorError(errStopped, "stop_requested")
			if !executorErrorHasCode(err, errStopped) {
				_ = logger.errorEvent(loop.client.sequence, execErr)
			}
			return recoverableTurn(role, stats, execErr.Error())
		}
		return recoverableTurn(role, stats, err.Error())
	}
	if strings.TrimSpace(logs) == "" {
		return recoverableTurn(role, stats, newExecutorError(errAgentProtocol, "no_output").Error())
	}
	return game.TurnResult{
		Role:   role,
		Status: game.ExtractStatus(logs),
		Logs:   logs,
		Stats:  stats,
	}
}

func executorErrorHasCode(err error, code executorErrorCode) bool {
	var execErr *executorError
	return errors.As(err, &execErr) && execErr.Code == code
}

func effectiveTurnTimeout(configured int, budget game.TurnBudget) int {
	remaining := budget.RemainingDurationSec
	if remaining <= 0 {
		return configured
	}
	if configured <= 0 || remaining < configured {
		return remaining
	}
	return configured
}

// turnContext builds the turn context, applying the optional timeout and
// wiring stop requests into the same cancellation source the tools observe.
func turnContext(timeout int, stopRequested func() bool) (context.Context, context.CancelFunc) {
	ctx := context.Background()
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	if stopRequested != nil {
		go func() {
			ticker := time.NewTicker(200 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if stopRequested() {
						cancel()
						return
					}
				}
			}
		}()
	}
	return ctx, cancel
}

// WorkspaceState returns the workspace state from the platform.
func (ne *NativeExecutor) WorkspaceState() string {
	return ne.Platform.WorkspaceState()
}

// CheckpointWorkspace creates a workspace checkpoint.
func (ne *NativeExecutor) CheckpointWorkspace(dest string) bool {
	return ne.Platform.CheckpointWorkspace(dest)
}

func recoverableTurn(role string, stats game.TurnStats, reason string) game.TurnResult {
	return game.TurnResult{
		Role:        role,
		Status:      game.StatusContinue,
		Logs:        reason,
		Stats:       stats,
		Error:       reason,
		Recoverable: true,
	}
}

func terminalTurn(role string, stats game.TurnStats, reason string) game.TurnResult {
	return game.TurnResult{
		Role:        role,
		Status:      game.StatusContinue,
		Logs:        reason,
		Stats:       stats,
		Error:       reason,
		Recoverable: false,
	}
}

func mergeTurnStats(base, extra game.TurnStats) game.TurnStats {
	base.CostUSD += extra.CostUSD
	base.DurationMS += extra.DurationMS
	base.NumTurns += extra.NumTurns
	base.InputTokens += extra.InputTokens
	base.OutputTokens += extra.OutputTokens
	base.CacheReadTokens += extra.CacheReadTokens
	base.CacheCreationTokens += extra.CacheCreationTokens
	base.CostUnavailable = base.CostUnavailable || extra.CostUnavailable
	if base.Model == "" {
		base.Model = extra.Model
	}
	return base
}

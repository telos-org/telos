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
	Platform *platform.LocalPlatform
	Model    string
	Thinking string
	Timeout  int
	Client   *http.Client
}

// NewNativeExecutor creates a native Go coding-agent executor.
func NewNativeExecutor(p *platform.LocalPlatform, model, thinking string, timeout int) *NativeExecutor {
	if thinking == "" {
		thinking = "medium"
	}
	return &NativeExecutor{
		Platform: p,
		Model:    model,
		Thinking: thinking,
		Timeout:  timeout,
		Client:   http.DefaultClient,
	}
}

// ExecuteTurn runs one Telos-native agent turn.
func (ne *NativeExecutor) ExecuteTurn(task string, role string, turnState *game.TurnState) game.TurnResult {
	started := time.Now()
	stats := game.TurnStats{Model: ne.Model}
	sessionPath := ""
	var stopRequested func() bool
	if turnState != nil {
		sessionPath = turnState.SessionPath()
		stopRequested = turnState.StopRequested
	}

	ctx, cancel := turnContext(ne.Timeout, stopRequested)
	defer cancel()

	cfg, err := resolveNativeProvider(ne.Model)
	if err != nil {
		return recoverableTurn(role, stats, "agent_config_error:"+err.Error())
	}
	logger := newNativeSessionLogger(sessionPath, ne.Platform.Workspace)
	if err := logger.start(); err != nil {
		return recoverableTurn(role, stats, "native_session_unavailable:"+err.Error())
	}
	_ = logger.user(task)

	tools := newNativeTools(ne.Platform, stopRequested)
	loop := newAgentLoop(ne.Client, cfg, ne.Thinking, tools, logger, task, role)

	logs, extraStats, err := loop.run(ctx)
	stats = mergeTurnStats(stats, extraStats)
	stats.DurationMS = int(time.Since(started).Milliseconds())
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return recoverableTurn(role, stats, fmt.Sprintf("native_timeout:%d", ne.Timeout))
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return recoverableTurn(role, stats, "native_interrupted:stop_requested")
		}
		return recoverableTurn(role, stats, err.Error())
	}
	if strings.TrimSpace(logs) == "" {
		return recoverableTurn(role, stats, "agent_no_output")
	}
	return game.TurnResult{
		Role:   role,
		Status: game.ExtractStatus(logs),
		Logs:   logs,
		Stats:  stats,
	}
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

func mergeTurnStats(base, extra game.TurnStats) game.TurnStats {
	base.CostUSD += extra.CostUSD
	base.DurationMS += extra.DurationMS
	base.NumTurns += extra.NumTurns
	base.InputTokens += extra.InputTokens
	base.OutputTokens += extra.OutputTokens
	base.CacheReadTokens += extra.CacheReadTokens
	base.CacheCreationTokens += extra.CacheCreationTokens
	if base.Model == "" {
		base.Model = extra.Model
	}
	return base
}

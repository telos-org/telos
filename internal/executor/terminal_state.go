package executor

import (
	"context"
	"errors"
)

type TerminalState string

const (
	TerminalCompleted      TerminalState = "completed"
	TerminalIncomplete     TerminalState = "incomplete"
	TerminalExhausted      TerminalState = "exhausted"
	TerminalInterrupted    TerminalState = "interrupted"
	TerminalPolicyBlocked  TerminalState = "policy_blocked"
	TerminalProviderFailed TerminalState = "provider_failed"
	TerminalToolFailed     TerminalState = "tool_failed"
)

func terminalStateForError(err error, ctx context.Context) TerminalState {
	if err == nil {
		return TerminalCompleted
	}
	if ctx != nil {
		switch {
		case errors.Is(ctx.Err(), context.Canceled):
			return TerminalInterrupted
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			return TerminalProviderFailed
		}
	}
	var execErr *executorError
	if errors.As(err, &execErr) {
		switch execErr.Code {
		case errRuntimeBudgetExhausted:
			return TerminalExhausted
		case errStopped:
			return TerminalInterrupted
		case errAgentIncomplete, errAgentProtocol:
			return TerminalIncomplete
		case errToolPolicyDenied:
			return TerminalPolicyBlocked
		case errToolInfra, errToolTimeout:
			return TerminalToolFailed
		case errConfig, errProviderRateLimited, errProviderTimeout, errProviderUnavailable, errProviderInvalidRequest, errProviderContextLimit:
			return TerminalProviderFailed
		}
	}
	return TerminalProviderFailed
}

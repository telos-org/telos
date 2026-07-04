package executor

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func (t *nativeTools) bash(ctx context.Context, command string, cwd string, env map[string]string, timeout int) (toolOutput, error) {
	if strings.TrimSpace(command) == "" {
		return toolOutput{}, fmt.Errorf("command is required")
	}
	capSeconds := t.effectiveBashTimeoutCap()
	if timeout <= 0 || timeout > capSeconds {
		timeout = capSeconds
	}
	interrupt := func() bool {
		if ctx.Err() != nil {
			return true
		}
		return t.stopRequested != nil && t.stopRequested()
	}
	runCWD := ""
	if strings.TrimSpace(cwd) != "" {
		resolved, err := t.resolvePath(cwd)
		if err != nil {
			return toolOutput{}, err
		}
		runCWD = resolved.full
	}
	result := t.platform.Run([]string{"bash", "-lc", command}, "", env, timeout, interrupt, nil, runCWD)
	stdoutText, stdoutLineTruncated := capOutputLines(strings.Join(result.RawLines, "\n"), "stdout", result.StdoutOriginalLines, t.limit.MaxLines)
	stderrText, stderrLineTruncated := capOutputLines(strings.TrimSpace(result.Stderr), "stderr", result.StderrOriginalLines, t.limit.MaxLines)
	stdoutTruncated := result.StdoutTruncated || stdoutLineTruncated
	stderrTruncated := result.StderrTruncated || stderrLineTruncated
	exitCode := result.ReturnCode
	out := toolOutput{
		fields: toolFields(
			"exit_code", exitCode,
			"signal", defaultString(result.Signal, "none"),
			"started_at", result.StartedAt.UTC().Format(time.RFC3339Nano),
			"ended_at", result.EndedAt.UTC().Format(time.RFC3339Nano),
			"duration_ms", result.DurationMS,
			"timed_out", result.TimedOut,
			"interrupted", result.Interrupted,
			"stdout_bytes", result.StdoutBytes,
			"stdout_original_bytes", result.StdoutOriginalBytes,
			"stdout_original_lines", result.StdoutOriginalLines,
			"stdout_truncated", stdoutTruncated,
			"stderr_bytes", result.StderrBytes,
			"stderr_original_bytes", result.StderrOriginalBytes,
			"stderr_original_lines", result.StderrOriginalLines,
			"stderr_truncated", stderrTruncated,
		),
		exitCode:  &exitCode,
		truncated: stdoutTruncated || stderrTruncated,
	}
	if stdoutText != "" {
		out.bodies = append(out.bodies, toolBodySection{Key: "stdout", Text: stdoutText})
	}
	if stderrText != "" {
		out.bodies = append(out.bodies, toolBodySection{Key: "stderr", Text: stderrText})
	}
	if result.InfraError != "" {
		out.bodies = append(out.bodies, toolBodySection{Key: "error", Text: result.InfraError})
		switch {
		case result.TimedOut:
			out.errorCode = errToolTimeout
		case result.Interrupted:
			out.errorCode = errStopped
		default:
			out.errorCode = errToolInfra
		}
		return out, toolFailure{code: out.errorCode, reason: result.InfraError}
	}
	if result.ReturnCode != 0 {
		return out, toolFailure{reason: fmt.Sprintf("bash_exit_code:%d", result.ReturnCode)}
	}
	return out, nil
}

func (t *nativeTools) effectiveBashTimeoutCap() int {
	capSeconds := defaultToolTimeoutSec
	if t != nil && t.budget.AgentTimeoutSec > 0 {
		capSeconds = t.budget.AgentTimeoutSec
	}
	if t != nil && t.budget.RemainingDurationSec > 0 && t.budget.RemainingDurationSec < capSeconds {
		capSeconds = t.budget.RemainingDurationSec
	}
	if capSeconds <= 0 {
		return defaultToolTimeoutSec
	}
	return capSeconds
}

func capOutputLines(text string, streamName string, originalLines int, maxLines int) (string, bool) {
	if strings.TrimSpace(text) == "" || maxLines <= 0 {
		return text, false
	}
	lines := strings.Split(text, "\n")
	if len(lines) <= maxLines {
		return text, false
	}
	displayOriginal := originalLines
	if displayOriginal < len(lines) {
		displayOriginal = len(lines)
	}
	lines = append(lines[:maxLines], fmt.Sprintf("... %s truncated at %d lines of %d ...", streamName, maxLines, displayOriginal))
	return strings.Join(lines, "\n"), true
}

// Package envutil centralizes small environment-value parsers.
package envutil

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Bool parses Telos boolean env values. The accepted true values are
// 1, true, yes, and on, case-insensitively. All other non-empty values are
// false so callers fail closed unless they explicitly opt in.
func Bool(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// EnvBool parses an environment variable with Bool.
func EnvBool(name string) bool {
	return Bool(os.Getenv(name))
}

// Int parses a base-10 integer, returning fallback when raw is empty or invalid.
func Int(raw string, fallback int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return n
}

// EnvInt parses an integer environment variable.
func EnvInt(name string, fallback int) int {
	return Int(os.Getenv(name), fallback)
}

// DurationMS parses a millisecond duration, returning fallback when raw is
// empty, invalid, or negative.
func DurationMS(raw string, fallback time.Duration) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return fallback
	}
	return time.Duration(n) * time.Millisecond
}

// EnvDurationMS parses a millisecond-duration environment variable.
func EnvDurationMS(name string, fallback time.Duration) time.Duration {
	return DurationMS(os.Getenv(name), fallback)
}

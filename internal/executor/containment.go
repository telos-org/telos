package executor

import "fmt"

type ContainmentMode string

const (
	ContainmentContainer        ContainmentMode = "container"
	ContainmentSessionWorkspace ContainmentMode = "session-workspace"
	ContainmentUncontained      ContainmentMode = "uncontained"
)

type NativeExecutorOption func(*nativeExecutorOptions)

type nativeExecutorOptions struct {
	containmentMode ContainmentMode
	sessionID       string
	sessionDir      string
}

func WithContainmentMode(mode ContainmentMode) NativeExecutorOption {
	return func(opts *nativeExecutorOptions) {
		opts.containmentMode = mode
	}
}

// WithSession identifies the session a turn belongs to. The session dir must
// hold the session.json manifest; managed Bifrost routing state is persisted
// there across turns.
func WithSession(sessionID, sessionDir string) NativeExecutorOption {
	return func(opts *nativeExecutorOptions) {
		opts.sessionID = sessionID
		opts.sessionDir = sessionDir
	}
}

func validateContainmentMode(mode ContainmentMode) error {
	switch mode {
	case ContainmentContainer, ContainmentSessionWorkspace, ContainmentUncontained:
		return nil
	case "":
		return fmt.Errorf("containment_mode is required")
	default:
		return fmt.Errorf("unknown containment_mode %q", mode)
	}
}

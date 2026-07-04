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
}

func WithContainmentMode(mode ContainmentMode) NativeExecutorOption {
	return func(opts *nativeExecutorOptions) {
		opts.containmentMode = mode
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

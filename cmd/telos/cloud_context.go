package main

import (
	"flag"
	"fmt"
	"strings"

	"github.com/telos-org/telos/internal/cloud"
)

func cloudContextFlag(fs *flag.FlagSet) *string {
	return fs.String(
		"context",
		"",
		"Cloud context for this command as @handle, organization ID, or personal",
	)
}

func cloudContextOverride(fs *flag.FlagSet, value string) (string, error) {
	value = strings.TrimSpace(value)
	if flagNameSet(fs, "context") && value == "" {
		return "", fmt.Errorf("--context requires @handle, organization ID, or personal")
	}
	return value, nil
}

// followUpContext returns the --context a later command needs to reach the
// same workspace as control, or "" when its default context already does.
func followUpContext(control *cloud.Client, contextOverride string) string {
	if strings.TrimSpace(contextOverride) == "" {
		return ""
	}
	if fallback, err := cloud.ControlClientForContext(""); err == nil && fallback.ContextName() == control.ContextName() {
		return ""
	}
	return control.ContextName()
}

func validateCloudSessionContext(sessionID, contextOverride string) error {
	if isLocalApplyID(strings.TrimSpace(sessionID)) && strings.TrimSpace(contextOverride) != "" {
		return fmt.Errorf("--context cannot be used with a local Goal")
	}
	return nil
}

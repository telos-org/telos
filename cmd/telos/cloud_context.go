package main

import (
	"flag"
	"fmt"
	"strings"
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

func validateCloudSessionContext(sessionID, contextOverride string) error {
	if isLocalApplyID(strings.TrimSpace(sessionID)) && strings.TrimSpace(contextOverride) != "" {
		return fmt.Errorf("--context cannot be used with a local session")
	}
	return nil
}

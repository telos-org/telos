package main

import (
	"errors"
	"fmt"

	"github.com/telos-org/telos/internal/cloud"
)

// apiCode returns the stable code of a Cloud error, or "".
func apiCode(err error) string {
	var apiErr *cloud.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Code
	}
	return ""
}

// ruleError shows a coded Cloud error, such as proposal_stale, in Cloud's
// words without the HTTP status, optionally followed by the CLI's next step;
// other errors pass through unchanged.
func ruleError(err error, hint string) error {
	var apiErr *cloud.APIError
	if !errors.As(err, &apiErr) || apiErr.Code == "" {
		return err
	}
	message := apiErr.Detail
	if hint != "" {
		message += "\n" + hint
	}
	return &messageError{message: message, cause: err}
}

// messageError replaces an error's text and keeps its cause for errors.As.
type messageError struct {
	message string
	cause   error
}

func (e *messageError) Error() string { return e.message }

func (e *messageError) Unwrap() error { return e.cause }

// proposeHint is the next step after a direct deploy or apply is refused.
func proposeHint(specArg string) string {
	return fmt.Sprintf("telos propose %s -t \"TITLE\"", shellArg(specArg))
}

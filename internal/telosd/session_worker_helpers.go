package telosd

import (
	"errors"
	"fmt"

	"github.com/telos-org/telos/internal/sessionapi"
)

func sessionWorkerKind(session *sessionapi.Session) (sessionapi.SessionKind, error) {
	if session == nil || session.SessionKind == nil {
		return "", errors.New("session_kind is required to launch a worker")
	}
	switch *session.SessionKind {
	case sessionapi.KindController, sessionapi.KindTask:
		return *session.SessionKind, nil
	default:
		return "", fmt.Errorf("invalid session_kind %q", *session.SessionKind)
	}
}

func ptrValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

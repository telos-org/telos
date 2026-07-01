package telosd

import (
	"fmt"

	"github.com/telos-org/telos/internal/sessionapi"
	"github.com/telos-org/telos/internal/sessionworker"
)

type localProcessSubstrate struct{}

func newLocalProcessSubstrate() localProcessSubstrate {
	return localProcessSubstrate{}
}

func newSessionSubstrate(Config) (sessionSubstrate, error) {
	return newLocalProcessSubstrate(), nil
}

func (s localProcessSubstrate) Apply(session *sessionapi.Session, wakeReason string) error {
	if _, err := sessionWorkerKind(session); err != nil {
		return err
	}
	sessionDir := ptrValue(session.SessionDir)
	if sessionDir == "" {
		return fmt.Errorf("session %s has no session_dir", session.SessionID)
	}
	if err := sessionworker.Stop(sessionDir); err != nil {
		return err
	}
	return sessionworker.StartWithOptions(sessionDir, sessionworker.StartOptions{
		Runtime:    sessionapi.RuntimeCloud,
		WakeReason: wakeReason,
	})
}

func (s localProcessSubstrate) Stop(session *sessionapi.Session) error {
	sessionDir := ptrValue(session.SessionDir)
	if sessionDir == "" {
		return nil
	}
	return sessionworker.Stop(sessionDir)
}

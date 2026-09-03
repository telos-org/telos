package telosd

import (
	"fmt"

	"github.com/telos-org/telos/internal/runtimeenv"
	"github.com/telos-org/telos/internal/sessionapi"
	"github.com/telos-org/telos/internal/sessionworker"
)

type localProcessSubstrate struct {
	runtimeCredentialEnvironmentPath string
}

func newLocalProcessSubstrate(runtimeCredentialEnvironmentPath string) localProcessSubstrate {
	return localProcessSubstrate{
		runtimeCredentialEnvironmentPath: runtimeCredentialEnvironmentPath,
	}
}

func newSessionSubstrate(cfg Config) (sessionSubstrate, error) {
	return newLocalProcessSubstrate(runtimeenv.Path(cfg.Root)), nil
}

func (s localProcessSubstrate) Apply(session *sessionapi.Session, wakeReason string) error {
	if _, err := sessionWorkerKind(session); err != nil {
		return err
	}
	sessionDir := ptrValue(session.SessionDir)
	if sessionDir == "" {
		return fmt.Errorf("session %s has no session_dir", session.SessionID)
	}
	return sessionworker.EnsureStartedWithOptions(sessionDir, sessionworker.StartOptions{
		Runtime:                          sessionapi.RuntimeCloud,
		WakeReason:                       wakeReason,
		RuntimeCredentialEnvironmentPath: s.runtimeCredentialEnvironmentPath,
	})
}

func (s localProcessSubstrate) Stop(session *sessionapi.Session) error {
	sessionDir := ptrValue(session.SessionDir)
	if sessionDir == "" {
		return nil
	}
	return sessionworker.Stop(sessionDir)
}

func (s localProcessSubstrate) Wake(session *sessionapi.Session, wakeReason string) error {
	sessionDir := ptrValue(session.SessionDir)
	if sessionDir == "" {
		return sessionworker.ErrWorkerNotRunning
	}
	return sessionworker.Wake(sessionDir)
}

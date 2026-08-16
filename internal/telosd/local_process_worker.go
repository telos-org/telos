package telosd

import (
	"fmt"
	"os"
	"strings"

	"github.com/telos-org/telos/internal/sessionapi"
	"github.com/telos-org/telos/internal/sessionworker"
)

type localProcessSubstrate struct {
	checkpointRoot      string
	checkpointSessionID string
}

func newLocalProcessSubstrate() localProcessSubstrate {
	return localProcessSubstrate{}
}

func newSessionSubstrate(cfg Config) (sessionSubstrate, error) {
	substrate := newLocalProcessSubstrate()
	if cfg.Mode == ModeCloud {
		if sessionID := strings.TrimSpace(os.Getenv("TELOS_SESSION_ID")); sessionID != "" {
			substrate.checkpointRoot = cfg.Root
			substrate.checkpointSessionID = sessionID
		}
	}
	return substrate, nil
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
		Runtime:             sessionapi.RuntimeCloud,
		WakeReason:          wakeReason,
		CheckpointRoot:      s.checkpointRoot,
		CheckpointSessionID: s.checkpointSessionID,
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

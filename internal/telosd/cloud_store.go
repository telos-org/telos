package telosd

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/telos-org/telos/internal/sessionapi"
)

type sessionSubstrate interface {
	Apply(session *sessionapi.Session, wakeReason string) error
	Stop(session *sessionapi.Session) error
}

type cloudSessionStore struct {
	*sessionapi.FileStore
	substrate sessionSubstrate
}

func newCloudSessionStore(base *sessionapi.FileStore, substrate sessionSubstrate) *cloudSessionStore {
	return &cloudSessionStore{FileStore: base, substrate: substrate}
}

func (s *cloudSessionStore) Create(req sessionapi.SessionCreateRequest) (*sessionapi.Session, error) {
	req = cloudCreateDefaults(req)
	session, err := s.FileStore.Create(req)
	if err != nil {
		return nil, err
	}
	if err := s.apply(session, startWakeReason(session)); err != nil {
		cleanupErr := s.cleanupWorker(session)
		removeSessionDir(session)
		if cleanupErr != nil {
			return nil, errors.Join(err, cleanupErr)
		}
		return nil, err
	}
	return session, nil
}

func (s *cloudSessionStore) UpdateSpec(name string, req sessionapi.SessionSpecUpdateRequest) (*sessionapi.SessionSpecUpdateResponse, error) {
	req = cloudSpecUpdateDefaults(req)
	response, err := s.FileStore.UpdateSpec(name, req)
	if err != nil {
		return nil, err
	}
	if response.Session == nil {
		return response, nil
	}
	wakeReason := "spec_updated"
	if response.Operation == "created" {
		wakeReason = startWakeReason(response.Session)
	}
	if err := s.apply(response.Session, wakeReason); err != nil {
		if response.Operation == "created" {
			cleanupErr := s.cleanupWorker(response.Session)
			removeSessionDir(response.Session)
			if cleanupErr != nil {
				return nil, errors.Join(err, cleanupErr)
			}
		}
		return nil, err
	}
	return response, nil
}

func (s *cloudSessionStore) List() ([]sessionapi.Session, error) {
	sessions, err := s.FileStore.List()
	if err != nil {
		return nil, err
	}
	return sessions, nil
}

func (s *cloudSessionStore) Get(id string) (*sessionapi.Session, error) {
	session, err := s.FileStore.Get(id)
	if err != nil {
		return nil, err
	}
	return session, nil
}

func (s *cloudSessionStore) apply(session *sessionapi.Session, wakeReason string) error {
	if s.substrate == nil {
		return nil
	}
	if err := s.substrate.Apply(session, wakeReason); err != nil {
		return fmt.Errorf("launch session %s worker: %w", session.SessionID, err)
	}
	return nil
}

func (s *cloudSessionStore) cleanupWorker(session *sessionapi.Session) error {
	if s.substrate == nil {
		return nil
	}
	if err := s.substrate.Stop(session); err != nil {
		return fmt.Errorf("clean up session %s worker: %w", session.SessionID, err)
	}
	return nil
}

func startWakeReason(session *sessionapi.Session) string {
	if session.SessionKind != nil && *session.SessionKind == sessionapi.KindController {
		return "controller_started"
	}
	return "task_started"
}

func (s *cloudSessionStore) Stop(id string) (*sessionapi.Session, error) {
	session, err := s.FileStore.Get(id)
	if err != nil {
		return nil, err
	}
	if s.substrate != nil {
		if err := s.substrate.Stop(session); err != nil {
			return nil, fmt.Errorf("stop session %s worker: %w", session.SessionID, err)
		}
	}
	session, err = s.FileStore.Stop(id)
	if err != nil {
		return nil, err
	}
	return session, nil
}

func cloudCreateDefaults(req sessionapi.SessionCreateRequest) sessionapi.SessionCreateRequest {
	if req.ParentSessionID != nil {
		return req
	}
	if req.SessionKind != nil && *req.SessionKind == sessionapi.KindTask {
		return req
	}
	if strings.TrimSpace(req.Model) == "" {
		req.Model = cloudSessionModel()
	}
	if req.AgentTimeoutSec == nil {
		req.AgentTimeoutSec = intPtr(cloudAgentTimeoutSec())
	}
	return req
}

func cloudSpecUpdateDefaults(req sessionapi.SessionSpecUpdateRequest) sessionapi.SessionSpecUpdateRequest {
	if strings.TrimSpace(req.Model) == "" {
		req.Model = cloudSessionModel()
	}
	if req.AgentTimeoutSec == nil {
		req.AgentTimeoutSec = intPtr(cloudAgentTimeoutSec())
	}
	return req
}

func removeSessionDir(session *sessionapi.Session) {
	if session == nil || session.SessionDir == nil || *session.SessionDir == "" {
		return
	}
	_ = os.RemoveAll(*session.SessionDir)
}

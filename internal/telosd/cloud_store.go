package telosd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/telos-org/telos/internal/sessionapi"
)

type sessionSubstrate interface {
	Apply(session *sessionapi.Session, wakeReason string) error
	Stop(session *sessionapi.Session) error
}

type cloudSessionStore struct {
	*sessionapi.FileStore
	handles   routeHandleResolver
	substrate sessionSubstrate
}

func newCloudSessionStore(base *sessionapi.FileStore, handles routeHandleResolver, substrate sessionSubstrate) *cloudSessionStore {
	return &cloudSessionStore{FileStore: base, handles: handles, substrate: substrate}
}

func (s *cloudSessionStore) Create(req sessionapi.SessionCreateRequest) (*sessionapi.Session, error) {
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
	s.enrich(session, s.routes())
	return session, nil
}

func (s *cloudSessionStore) UpdateSpec(id string, req sessionapi.SessionSpecUpdateRequest) (*sessionapi.Session, error) {
	session, err := s.FileStore.UpdateSpec(id, req)
	if err != nil {
		return nil, err
	}
	if err := s.apply(session, "spec_updated"); err != nil {
		return nil, err
	}
	s.enrich(session, s.routes())
	return session, nil
}

func (s *cloudSessionStore) List() ([]sessionapi.Session, error) {
	sessions, err := s.FileStore.List()
	if err != nil {
		return nil, err
	}
	routes := s.routes()
	for i := range sessions {
		s.enrich(&sessions[i], routes)
	}
	return sessions, nil
}

func (s *cloudSessionStore) Get(id string) (*sessionapi.Session, error) {
	session, err := s.FileStore.Get(id)
	if err != nil {
		return nil, err
	}
	s.enrich(session, s.routes())
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
	s.enrich(session, s.routes())
	return session, nil
}

func removeSessionDir(session *sessionapi.Session) {
	if session == nil || session.SessionDir == nil || *session.SessionDir == "" {
		return
	}
	_ = os.RemoveAll(*session.SessionDir)
}

func (s *cloudSessionStore) routes() []publicRoute {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	routes, err := s.handles.Routes(ctx)
	if err != nil {
		return nil
	}
	return routes
}

func (s *cloudSessionStore) enrich(session *sessionapi.Session, routes []publicRoute) {
	if session.SessionKind == nil || *session.SessionKind != sessionapi.KindController {
		return
	}
	if session.Status.IsTerminal() {
		return
	}
	if handle := productHandleFor(routes, *session); handle != "" {
		uri := "https://" + stripScheme(handle)
		session.ArtifactURI = &uri
	}
}

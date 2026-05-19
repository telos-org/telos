package telosd

import (
	"context"
	"time"

	"github.com/telos-org/telos-go/internal/sessionapi"
)

type cloudSessionStore struct {
	*sessionapi.FileStore
	handles routeHandleResolver
}

func newCloudSessionStore(base *sessionapi.FileStore, handles routeHandleResolver) *cloudSessionStore {
	return &cloudSessionStore{FileStore: base, handles: handles}
}

func (s *cloudSessionStore) Create(req sessionapi.SessionCreateRequest) (*sessionapi.Session, error) {
	session, err := s.FileStore.Create(req)
	if err != nil {
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

func (s *cloudSessionStore) Stop(id string) (*sessionapi.Session, error) {
	session, err := s.FileStore.Stop(id)
	if err != nil {
		return nil, err
	}
	s.enrich(session, s.routes())
	return session, nil
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

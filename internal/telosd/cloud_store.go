package telosd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/telos-org/telos/internal/sessionapi"
)

type sessionSubstrate interface {
	Apply(session *sessionapi.Session, wakeReason string, userAuthorization string) error
	Stop(session *sessionapi.Session) error
}

type sessionRuntimeStatusSubstrate interface {
	RuntimeStatus(session *sessionapi.Session) (sessionapi.SessionStatus, error)
}

type cloudSessionStore struct {
	*sessionapi.FileStore
	handles           routeHandleResolver
	substrate         sessionSubstrate
	userAuthorization *rootUserAuthorizationCache
}

func newCloudSessionStore(base *sessionapi.FileStore, handles routeHandleResolver, substrate sessionSubstrate) *cloudSessionStore {
	return &cloudSessionStore{
		FileStore:         base,
		handles:           handles,
		substrate:         substrate,
		userAuthorization: newRootUserAuthorizationCache(),
	}
}

func (s *cloudSessionStore) Create(req sessionapi.SessionCreateRequest) (*sessionapi.Session, error) {
	session, err := s.FileStore.Create(req)
	if err != nil {
		return nil, err
	}
	s.rememberUserAuthorization(session, req.UserAuthorization)
	userAuthorization := s.userAuthorizationFor(session, req.UserAuthorization)
	if err := s.apply(session, startWakeReason(session), userAuthorization); err != nil {
		s.forgetUserAuthorization(session)
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

func (s *cloudSessionStore) UpdateSpec(name string, req sessionapi.SessionSpecUpdateRequest) (*sessionapi.SessionSpecUpdateResponse, error) {
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
	s.rememberUserAuthorization(response.Session, req.UserAuthorization)
	userAuthorization := s.userAuthorizationFor(response.Session, req.UserAuthorization)
	if err := s.apply(response.Session, wakeReason, userAuthorization); err != nil {
		if response.Operation == "created" {
			s.forgetUserAuthorization(response.Session)
			cleanupErr := s.cleanupWorker(response.Session)
			removeSessionDir(response.Session)
			if cleanupErr != nil {
				return nil, errors.Join(err, cleanupErr)
			}
		}
		return nil, err
	}
	s.enrich(response.Session, s.routes())
	return response, nil
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

func (s *cloudSessionStore) apply(session *sessionapi.Session, wakeReason string, userAuthorization string) error {
	if s.substrate == nil {
		return nil
	}
	if err := s.substrate.Apply(session, wakeReason, userAuthorization); err != nil {
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
	s.forgetUserAuthorization(session)
	s.enrich(session, s.routes())
	return session, nil
}

func (s *cloudSessionStore) rememberUserAuthorization(session *sessionapi.Session, userAuthorization string) {
	if s.userAuthorization == nil {
		return
	}
	s.userAuthorization.Remember(session, userAuthorization)
}

func (s *cloudSessionStore) userAuthorizationFor(session *sessionapi.Session, provided string) string {
	if provided = strings.TrimSpace(provided); provided != "" {
		return provided
	}
	if s.userAuthorization == nil {
		return ""
	}
	return s.userAuthorization.For(session)
}

func (s *cloudSessionStore) forgetUserAuthorization(session *sessionapi.Session) {
	if s.userAuthorization == nil {
		return
	}
	s.userAuthorization.Forget(session)
}

type rootUserAuthorizationCache struct {
	mu     sync.RWMutex
	byRoot map[string]string
}

func newRootUserAuthorizationCache() *rootUserAuthorizationCache {
	return &rootUserAuthorizationCache{byRoot: map[string]string{}}
}

func (c *rootUserAuthorizationCache) Remember(session *sessionapi.Session, userAuthorization string) {
	userAuthorization = strings.TrimSpace(userAuthorization)
	if session == nil || userAuthorization == "" || !isRootSession(session) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byRoot[session.SessionID] = userAuthorization
}

func (c *rootUserAuthorizationCache) For(session *sessionapi.Session) string {
	if session == nil {
		return ""
	}
	rootID := rootSessionID(session)
	if rootID == "" {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.byRoot[rootID]
}

func (c *rootUserAuthorizationCache) Forget(session *sessionapi.Session) {
	if session == nil {
		return
	}
	rootID := rootSessionID(session)
	if rootID == "" {
		rootID = session.SessionID
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.byRoot, rootID)
}

func isRootSession(session *sessionapi.Session) bool {
	return session != nil && (session.ParentSessionID == nil || *session.ParentSessionID == "")
}

func rootSessionID(session *sessionapi.Session) string {
	if session == nil {
		return ""
	}
	if session.ParentSessionID != nil && *session.ParentSessionID != "" {
		return *session.ParentSessionID
	}
	return session.SessionID
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
	s.enrichRuntimeStatus(session)
	if session.ParentSessionID != nil && *session.ParentSessionID != "" {
		return
	}
	if handle := productHandleFor(routes, *session); handle != "" {
		uri := "https://" + stripScheme(handle)
		session.ServiceURL = &uri
	}
	if handle := dashboardHandleFor(routes, *session); handle != "" {
		url := "https://" + stripScheme(handle)
		session.DashboardURL = &url
	}
}

func (s *cloudSessionStore) enrichRuntimeStatus(session *sessionapi.Session) {
	if session == nil || s.substrate == nil || session.Status.IsTerminal() {
		return
	}
	statuser, ok := s.substrate.(sessionRuntimeStatusSubstrate)
	if !ok {
		return
	}
	status, err := statuser.RuntimeStatus(session)
	if err != nil || status == "" {
		return
	}
	session.Status = status
}

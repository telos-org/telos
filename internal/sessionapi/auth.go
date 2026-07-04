package sessionapi

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type Scope string

const (
	ScopeSessionsApply Scope = "sessions:apply"
	ScopeSessionsRun   Scope = "sessions:run"
	ScopeSessionsRead  Scope = "sessions:read"
	ScopeSessionsStop  Scope = "sessions:stop"
)

type Role string

const (
	RoleOperator   Role = "operator"
	RoleController Role = "controller"
	RoleTask       Role = "task"
)

type Caller struct {
	Role             Role
	Scopes           map[Scope]bool
	SubjectSessionID string
}

type AccessAction string

const (
	ActionHealth            AccessAction = "health"
	ActionCreateSession     AccessAction = "create_session"
	ActionUpdateSessionSpec AccessAction = "update_session_spec"
	ActionListSessions      AccessAction = "list_sessions"
	ActionReadSession       AccessAction = "read_session"
	ActionStopSession       AccessAction = "stop_session"
)

type AccessRequest struct {
	Action        AccessAction
	SessionID     string
	CreateRequest *SessionCreateRequest
}

type Authorizer interface {
	Caller(r *http.Request, req AccessRequest) (Caller, error)
	VisibleSessions(caller Caller, sessions []Session) []Session
}

type AllowAllAuthorizer struct{}

func (AllowAllAuthorizer) Caller(_ *http.Request, _ AccessRequest) (Caller, error) {
	return OperatorCaller(), nil
}

func (AllowAllAuthorizer) VisibleSessions(_ Caller, sessions []Session) []Session {
	return sessions
}

type BearerAuthorizer struct {
	store         *FileStore
	operatorToken string
}

func NewBearerAuthorizer(store *FileStore, operatorToken string) BearerAuthorizer {
	return BearerAuthorizer{
		store:         store,
		operatorToken: strings.TrimSpace(operatorToken),
	}
}

func (a BearerAuthorizer) Caller(r *http.Request, req AccessRequest) (Caller, error) {
	if req.Action == ActionHealth {
		return OperatorCaller(), nil
	}
	token := bearerToken(r.Header.Get("Authorization"))
	if token == "" {
		return Caller{}, authError{status: http.StatusUnauthorized, detail: "missing bearer token"}
	}
	if a.operatorToken != "" && constantTimeEqual(token, a.operatorToken) {
		caller := OperatorCaller()
		return caller, authorizeCaller(a.store, caller, req)
	}
	caller, ok := a.store.CallerForToken(token)
	if !ok {
		return Caller{}, authError{status: http.StatusUnauthorized, detail: "invalid bearer token"}
	}
	return caller, authorizeCaller(a.store, caller, req)
}

func (a BearerAuthorizer) VisibleSessions(caller Caller, sessions []Session) []Session {
	if caller.Role == RoleOperator {
		return sessions
	}
	if caller.SubjectSessionID == "" || !caller.HasScope(ScopeSessionsRead) {
		return []Session{}
	}
	visible := make([]Session, 0, len(sessions))
	for _, session := range sessions {
		if caller.Role == RoleTask {
			if session.SessionID == caller.SubjectSessionID {
				visible = append(visible, session)
			}
			continue
		}
		if a.store.IsSessionOrDescendant(session.SessionID, caller.SubjectSessionID) {
			visible = append(visible, session)
		}
	}
	return visible
}

type authError struct {
	status int
	detail string
}

func (e authError) Error() string {
	return e.detail
}

// AuthHTTPError returns the HTTP status/detail for authorization errors.
func AuthHTTPError(err error) (int, string, bool) {
	var e authError
	if errors.As(err, &e) {
		return e.status, e.detail, true
	}
	return 0, "", false
}

func OperatorCaller() Caller {
	return Caller{
		Role: RoleOperator,
		Scopes: map[Scope]bool{
			ScopeSessionsApply: true,
			ScopeSessionsRun:   true,
			ScopeSessionsRead:  true,
			ScopeSessionsStop:  true,
		},
	}
}

func (c Caller) HasScope(scope Scope) bool {
	return c.Scopes != nil && c.Scopes[scope]
}

func authorizeCaller(store *FileStore, caller Caller, req AccessRequest) error {
	switch req.Action {
	case ActionHealth:
		return nil
	case ActionCreateSession:
		if req.CreateRequest != nil && req.CreateRequest.ParentSessionID != nil {
			return requireRootSession(caller, *req.CreateRequest.ParentSessionID)
		}
		return requireScope(caller, ScopeSessionsApply)
	case ActionUpdateSessionSpec:
		return requireScope(caller, ScopeSessionsApply)
	case ActionListSessions:
		return requireScope(caller, ScopeSessionsRead)
	case ActionReadSession:
		return requireSessionAccess(store, caller, req.SessionID, ScopeSessionsRead)
	case ActionStopSession:
		return requireSessionAccess(store, caller, req.SessionID, ScopeSessionsStop)
	default:
		return authError{status: http.StatusForbidden, detail: "unsupported session API action"}
	}
}

func requireScope(caller Caller, scope Scope) error {
	if caller.HasScope(scope) {
		return nil
	}
	return authError{status: http.StatusForbidden, detail: fmt.Sprintf("%s access required", scope)}
}

func requireRootSession(caller Caller, sessionID string) error {
	if err := requireScope(caller, ScopeSessionsRun); err != nil {
		return err
	}
	if caller.Role != RoleController || caller.SubjectSessionID != sessionID {
		return authError{status: http.StatusForbidden, detail: "root session access required"}
	}
	return nil
}

func requireSessionAccess(store *FileStore, caller Caller, sessionID string, scope Scope) error {
	if err := requireScope(caller, scope); err != nil {
		return err
	}
	if caller.Role == RoleOperator {
		return nil
	}
	if caller.SubjectSessionID == "" {
		return authError{status: http.StatusForbidden, detail: "session scope required"}
	}
	if caller.Role == RoleTask {
		if caller.SubjectSessionID == sessionID {
			return nil
		}
		return authError{status: http.StatusForbidden, detail: "child token cannot access this session"}
	}
	if store.IsSessionOrDescendant(sessionID, caller.SubjectSessionID) {
		return nil
	}
	return authError{status: http.StatusForbidden, detail: "root token cannot access this session"}
}

func NewScopedToken(sessionID string, sessionKind SessionKind) (*ScopedToken, error) {
	token, err := randomToken()
	if err != nil {
		return nil, err
	}
	scopes := scopesForSessionKind(sessionKind)
	return &ScopedToken{
		APIToken:         token,
		SubjectSessionID: sessionID,
		Scopes:           scopeStrings(scopes),
	}, nil
}

func scopesForSessionKind(sessionKind SessionKind) []Scope {
	if sessionKind == KindController {
		return []Scope{
			ScopeSessionsRun,
			ScopeSessionsRead,
			ScopeSessionsStop,
		}
	}
	return []Scope{
		ScopeSessionsRead,
		ScopeSessionsStop,
	}
}

func scopeStrings(scopes []Scope) []string {
	values := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		values = append(values, string(scope))
	}
	return values
}

func roleForSessionKind(sessionKind SessionKind) Role {
	if sessionKind == KindController {
		return RoleController
	}
	return RoleTask
}

func scopesFromStrings(values []string) map[Scope]bool {
	scopes := map[Scope]bool{}
	for _, value := range values {
		switch scope := Scope(strings.TrimSpace(value)); scope {
		case ScopeSessionsApply, ScopeSessionsRun, ScopeSessionsRead, ScopeSessionsStop:
			scopes[scope] = true
		}
	}
	return scopes
}

func bearerToken(header string) string {
	fields := strings.Fields(header)
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(fields[1])
}

func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func randomToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate API token: %w", err)
	}
	return "tok_" + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func (fs *FileStore) CallerForToken(token string) (Caller, bool) {
	token = strings.TrimSpace(token)
	if token == "" {
		return Caller{}, false
	}

	fs.mu.Lock()
	defer fs.mu.Unlock()

	entries, err := os.ReadDir(fs.Root)
	if err != nil {
		return Caller{}, false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		m, err := ReadManifest(filepath.Join(fs.Root, entry.Name(), "session.json"))
		if err != nil || m.Access == nil {
			continue
		}
		if !constantTimeEqual(token, m.Access.APIToken) {
			continue
		}
		if m.Access.SubjectSessionID != entry.Name() {
			return Caller{}, false
		}
		scopes := scopesFromStrings(m.Access.Scopes)
		if len(scopes) == 0 {
			return Caller{}, false
		}
		return Caller{
			Role:             roleForSessionKind(m.SessionKind),
			Scopes:           scopes,
			SubjectSessionID: m.Access.SubjectSessionID,
		}, true
	}
	return Caller{}, false
}

func (fs *FileStore) IsSessionOrDescendant(sessionID, ancestorID string) bool {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.isSessionOrDescendantLocked(sessionID, ancestorID)
}

func (fs *FileStore) isSessionOrDescendantLocked(sessionID, ancestorID string) bool {
	current := sessionID
	seen := map[string]bool{}
	for current != "" && !seen[current] {
		if current == ancestorID {
			return true
		}
		seen[current] = true
		m, err := ReadManifest(filepath.Join(fs.Root, current, "session.json"))
		if err != nil || m.ParentSessionID == nil {
			return false
		}
		current = *m.ParentSessionID
	}
	return false
}

package telosd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/telos-org/telos/internal/sessionapi"
	"github.com/telos-org/telos/internal/sessionupdate"
	"github.com/telos-org/telos/internal/sessionworker"
)

type sessionSubstrate interface {
	Apply(session *sessionapi.Session, wakeReason string) error
	Stop(session *sessionapi.Session) error
	Wake(session *sessionapi.Session, wakeReason string) error
}

type packageMaterializer interface {
	Ensure(ctx context.Context, digest string) (string, error)
}

type controllerDefaults struct {
	Model           string
	Thinking        string
	AgentTimeoutSec *int
	Connection      *sessionapi.InferenceConnection
	ModelDefinition json.RawMessage
	InferenceError  error
}

type controllerReconciler struct {
	*sessionapi.FileStore
	substrate    sessionSubstrate
	materializer packageMaterializer
	defaults     controllerDefaults
}

func newControllerReconciler(
	base *sessionapi.FileStore,
	substrate sessionSubstrate,
	materializer packageMaterializer,
	defaults controllerDefaults,
) *controllerReconciler {
	if base.OnSpecUpdate == nil {
		base.OnSpecUpdate = sessionupdate.ProjectSpecUpdate
	}
	return &controllerReconciler{
		FileStore:    base,
		substrate:    substrate,
		materializer: materializer,
		defaults:     defaults,
	}
}

func (s *controllerReconciler) Create(req sessionapi.SessionCreateRequest) (*sessionapi.Session, error) {
	if s.defaults.InferenceError != nil {
		return nil, s.defaults.InferenceError
	}
	req = s.applyCreateDefaults(req)
	if err := s.materializeCreatePackage(&req); err != nil {
		return nil, err
	}
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

func (s *controllerReconciler) UpdateSpec(name string, req sessionapi.SessionSpecUpdateRequest) (*sessionapi.SessionSpecUpdateResponse, error) {
	req = s.applySpecUpdateDefaults(req)
	if err := s.materializeUpdatePackage(&req); err != nil {
		return nil, err
	}
	response, err := s.FileStore.UpdateSpec(name, req)
	if err != nil {
		return nil, err
	}
	if response.Session == nil {
		return response, nil
	}
	if response.Operation == "unchanged" {
		if err := s.wake(response.Session, "spec_unchanged"); err != nil {
			return nil, err
		}
		return response, nil
	}
	if response.Operation == "created" {
		if err := s.apply(response.Session, startWakeReason(response.Session)); err != nil {
			cleanupErr := s.cleanupWorker(response.Session)
			removeSessionDir(response.Session)
			if cleanupErr != nil {
				return nil, errors.Join(err, cleanupErr)
			}
			return nil, err
		}
		return response, nil
	}
	if err := s.wake(response.Session, "spec_updated"); err != nil {
		return nil, err
	}
	return response, nil
}

func (s *controllerReconciler) materializeCreatePackage(req *sessionapi.SessionCreateRequest) error {
	if req == nil || s.materializer == nil || strings.TrimSpace(req.PackagePath) != "" {
		return nil
	}
	digest := strings.TrimSpace(req.PackageDigest)
	if digest == "" {
		return nil
	}
	path, err := s.materializer.Ensure(context.Background(), digest)
	if err != nil {
		return err
	}
	req.PackagePath = path
	return nil
}

func (s *controllerReconciler) materializeUpdatePackage(req *sessionapi.SessionSpecUpdateRequest) error {
	if req == nil || s.materializer == nil || strings.TrimSpace(req.PackagePath) != "" {
		return nil
	}
	digest := strings.TrimSpace(req.PackageDigest)
	if digest == "" {
		return nil
	}
	path, err := s.materializer.Ensure(context.Background(), digest)
	if err != nil {
		return err
	}
	req.PackagePath = path
	req.PackageDigest = digest
	return nil
}

func (s *controllerReconciler) List() ([]sessionapi.Session, error) {
	sessions, err := s.FileStore.List()
	if err != nil {
		return nil, err
	}
	return sessions, nil
}

func (s *controllerReconciler) Get(id string) (*sessionapi.Session, error) {
	session, err := s.FileStore.Get(id)
	if err != nil {
		return nil, err
	}
	return session, nil
}

func (s *controllerReconciler) apply(session *sessionapi.Session, wakeReason string) error {
	if s.substrate == nil {
		return nil
	}
	if err := s.substrate.Apply(session, wakeReason); err != nil {
		return fmt.Errorf("launch session %s worker: %w", session.SessionID, err)
	}
	return nil
}

func (s *controllerReconciler) ensureRootWorkers(wakeReason string) error {
	sessions, listErr := s.FileStore.ListRootWorkerSessions()
	var errs []error
	if listErr != nil {
		errs = append(errs, listErr)
	}
	for i := range sessions {
		session := &sessions[i]
		if err := s.apply(session, wakeReason); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *controllerReconciler) wake(session *sessionapi.Session, wakeReason string) error {
	if s.substrate == nil {
		return nil
	}
	if err := s.substrate.Wake(session, wakeReason); err != nil {
		if errors.Is(err, sessionworker.ErrWorkerNotRunning) {
			return s.apply(session, wakeReason)
		}
		return fmt.Errorf("wake session %s worker: %w", session.SessionID, err)
	}
	return nil
}

func (s *controllerReconciler) cleanupWorker(session *sessionapi.Session) error {
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

func (s *controllerReconciler) Stop(id string) (*sessionapi.Session, error) {
	session, err := s.FileStore.Stop(id)
	if err != nil {
		return nil, err
	}
	if s.substrate != nil {
		if err := s.substrate.Stop(session); err != nil {
			return nil, fmt.Errorf("stop session %s worker: %w", session.SessionID, err)
		}
	}
	return s.FileStore.Get(id)
}

func (s *controllerReconciler) applyCreateDefaults(req sessionapi.SessionCreateRequest) sessionapi.SessionCreateRequest {
	parent := os.Getenv("TELOS_SESSION_ID")
	if req.CloudSessionID != "" {
		// A bootstrap root identifies itself in TELOS_SESSION_ID; it is not its
		// own parent and must receive the confirmed connection defaults.
		parent = ""
	}
	if req.ParentSessionID != nil {
		parent = *req.ParentSessionID
	}
	if parent != "" && filepath.Base(parent) == parent {
		m, err := sessionapi.ReadManifest(filepath.Join(s.Root, parent, "session.json"))
		if err == nil {
			if req.Model == "" {
				req.Model = m.Config.Model
			}
			if req.Thinking == "" {
				req.Thinking = m.Config.Thinking
			}
			if req.Model == m.Config.Model {
				req.ModelDefinition = m.InferenceModelDefinition
			}
			if c := m.InferenceConnection; c != nil && strings.HasPrefix(req.Model, c.Provider+"/") {
				req.InferenceConnection = c
			}
		}
	}
	if strings.TrimSpace(req.Model) == "" {
		req.Model = s.defaults.Model
	}
	if parent == "" && req.InferenceConnection == nil && s.defaults.Connection != nil && strings.HasPrefix(req.Model, s.defaults.Connection.Provider+"/") {
		req.InferenceConnection = s.defaults.Connection
		if req.Model == s.defaults.Model {
			req.ModelDefinition = s.defaults.ModelDefinition
		}
	}
	if req.ParentSessionID != nil {
		return req
	}
	if req.SessionKind != nil && *req.SessionKind == sessionapi.KindTask {
		return req
	}
	if strings.TrimSpace(req.Thinking) == "" {
		req.Thinking = s.defaults.Thinking
	}
	if req.AgentTimeoutSec == nil && s.defaults.AgentTimeoutSec != nil {
		req.AgentTimeoutSec = s.defaults.AgentTimeoutSec
	}
	return req
}

func (s *controllerReconciler) applySpecUpdateDefaults(req sessionapi.SessionSpecUpdateRequest) sessionapi.SessionSpecUpdateRequest {
	if strings.TrimSpace(req.Model) == "" {
		req.Model = s.defaults.Model
	}
	if strings.TrimSpace(req.Thinking) == "" {
		req.Thinking = s.defaults.Thinking
	}
	if req.AgentTimeoutSec == nil && s.defaults.AgentTimeoutSec != nil {
		req.AgentTimeoutSec = s.defaults.AgentTimeoutSec
	}
	return req
}

func removeSessionDir(session *sessionapi.Session) {
	if session == nil || session.SessionDir == nil || *session.SessionDir == "" {
		return
	}
	_ = os.RemoveAll(*session.SessionDir)
}

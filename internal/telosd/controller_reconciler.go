package telosd

import (
	"context"
	"errors"
	"fmt"
	"os"
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
	sessions, err := s.FileStore.List()
	if err != nil {
		return err
	}
	var errs []error
	for i := range sessions {
		session := &sessions[i]
		if session.ParentSessionID != nil {
			continue
		}
		if session.SessionKind == nil || *session.SessionKind != sessionapi.KindController {
			continue
		}
		if session.Result != nil && *session.Result == "stopped" {
			continue
		}
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

func (s *controllerReconciler) applyCreateDefaults(req sessionapi.SessionCreateRequest) sessionapi.SessionCreateRequest {
	if req.ParentSessionID != nil {
		return req
	}
	if req.SessionKind != nil && *req.SessionKind == sessionapi.KindTask {
		return req
	}
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

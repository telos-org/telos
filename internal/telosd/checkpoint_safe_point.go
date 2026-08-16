package telosd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/telos-org/telos/internal/sessionapi"
	"github.com/telos-org/telos/internal/sessionworker"
)

const (
	checkpointSafePointCapability  = sessionapi.CapabilityCheckpointSafePoint
	checkpointStateVersion         = 2
	checkpointInitializationMarker = ".checkpoint-safe-point.initialized"
	checkpointMaxMetadataBytes     = 4096
	checkpointPrepareTimeout       = 30 * time.Second
	checkpointPollInterval         = 100 * time.Millisecond
	checkpointRootEnv              = "TELOS_CHECKPOINT_ROOT"
	checkpointSessionEnv           = "TELOS_CHECKPOINT_SESSION_ID"
)

var (
	errCheckpointAdmissionClosed    = errors.New("checkpoint admission is closed")
	errCheckpointWorkersNotReady    = errors.New("checkpoint workers are not ready")
	errCheckpointOperationMismatch  = errors.New("checkpoint operation does not own admission")
	errCheckpointOperationCompleted = errors.New("checkpoint operation already completed")
	checkpointSessionIDRE           = regexp.MustCompile(`^sess_[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
	checkpointOperationIDRE         = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
)

type checkpointState struct {
	Version     int    `json:"version"`
	SessionID   string `json:"session_id"`
	OperationID string `json:"operation_id"`
	Status      string `json:"status"`
}

type checkpointInitialization struct {
	Version   int    `json:"version"`
	SessionID string `json:"session_id"`
}

type checkpointSafePoint struct {
	storageRoot    string
	root           string
	sessionID      string
	prepareTimeout time.Duration
	pollInterval   time.Duration
	prepareCheck   func() error
}

// One lease represents one admitted API create/update or one complete worker
// cycle. A worker cycle includes every agent turn and tool subprocess it starts.
type checkpointLease struct {
	path string
	file *os.File
}

func newCheckpointSafePoint(root, sessionID string) (*checkpointSafePoint, error) {
	root = strings.TrimSpace(root)
	sessionID = strings.TrimSpace(sessionID)
	if root == "" {
		return nil, errors.New("checkpoint state root is required")
	}
	if !checkpointSessionIDRE.MatchString(sessionID) {
		return nil, fmt.Errorf("invalid checkpoint session ID %q", sessionID)
	}
	manager := &checkpointSafePoint{
		storageRoot:    filepath.Clean(root),
		root:           filepath.Join(root, "checkpoint-safe-point"),
		sessionID:      sessionID,
		prepareTimeout: checkpointPrepareTimeout,
		pollInterval:   checkpointPollInterval,
	}
	if err := requireDirectoryNoFollow(manager.storageRoot); err != nil {
		return nil, fmt.Errorf("validate checkpoint storage root: %w", err)
	}
	markerExists, err := pathExistsNoFollow(manager.markerPath())
	if err != nil {
		return nil, fmt.Errorf("inspect checkpoint initialization marker: %w", err)
	}
	stateRootExists, err := pathExistsNoFollow(manager.root)
	if err != nil {
		return nil, fmt.Errorf("inspect checkpoint state root: %w", err)
	}
	switch {
	case markerExists:
		if !stateRootExists {
			return nil, errors.New("checkpoint state root is missing after initialization")
		}
		err = manager.validatePersistedStorage()
	case stateRootExists:
		err = errors.New("checkpoint state exists without a durable initialization marker")
	default:
		err = manager.initializeStorage()
	}
	if err != nil {
		return nil, err
	}
	return manager, nil
}

func checkpointSafePointForConfig(cfg Config) (*checkpointSafePoint, error) {
	if cfg.Mode != ModeCloud {
		return nil, nil
	}
	sessionID := strings.TrimSpace(os.Getenv("TELOS_SESSION_ID"))
	if sessionID == "" {
		return nil, nil
	}
	safePoint, err := newCheckpointSafePoint(cfg.Root, sessionID)
	if err != nil {
		return nil, err
	}
	safePoint.prepareCheck = func() error {
		return checkpointWorkersReady(SessionsRoot(cfg.Root))
	}
	return safePoint, nil
}

func checkpointWorkersReady(sessionsRoot string) error {
	entries, err := os.ReadDir(sessionsRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("list checkpoint workers: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		ready, err := sessionworker.ActiveWorkerSupportsCapabilities(
			filepath.Join(sessionsRoot, entry.Name()),
			sessionapi.CapabilityCheckpointSafePoint,
		)
		if err != nil {
			return fmt.Errorf("inspect checkpoint worker %s: %w", entry.Name(), err)
		}
		if !ready {
			return fmt.Errorf("%w: %s", errCheckpointWorkersNotReady, entry.Name())
		}
	}
	return nil
}

func checkpointSafePointForWorker() (*checkpointSafePoint, error) {
	root := strings.TrimSpace(os.Getenv(checkpointRootEnv))
	sessionID := strings.TrimSpace(os.Getenv(checkpointSessionEnv))
	if root == "" && sessionID == "" {
		return nil, nil
	}
	return newCheckpointSafePoint(root, sessionID)
}

func (s *checkpointSafePoint) statePath() string {
	return filepath.Join(s.root, "state.json")
}

func (s *checkpointSafePoint) markerPath() string {
	return filepath.Join(s.storageRoot, checkpointInitializationMarker)
}

func (s *checkpointSafePoint) lockPath() string {
	return filepath.Join(s.root, "state.lock")
}

func (s *checkpointSafePoint) leasesDir() string {
	return filepath.Join(s.root, "leases")
}

func (s *checkpointSafePoint) initializeStorage() error {
	if err := os.Mkdir(s.root, 0o700); err != nil {
		return fmt.Errorf("create checkpoint state root: %w", err)
	}
	if err := requireDirectoryNoFollow(s.root); err != nil {
		return err
	}
	if err := os.Mkdir(s.leasesDir(), 0o700); err != nil {
		return fmt.Errorf("create checkpoint leases: %w", err)
	}
	if err := requireDirectoryNoFollow(s.leasesDir()); err != nil {
		return err
	}
	lock, err := openRegularFileNoFollow(s.lockPath(), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("create checkpoint lock: %w", err)
	}
	if err := lock.Close(); err != nil {
		return err
	}
	if err := s.withLock(func() error { return s.writeStateLocked("open", "") }); err != nil {
		return err
	}
	return s.writeInitializationMarker()
}

func (s *checkpointSafePoint) validatePersistedStorage() error {
	markerData, err := readRegularFileNoFollow(s.markerPath(), checkpointMaxMetadataBytes)
	if err != nil {
		return fmt.Errorf("read checkpoint initialization marker: %w", err)
	}
	var marker checkpointInitialization
	if err := json.Unmarshal(markerData, &marker); err != nil {
		return fmt.Errorf("parse checkpoint initialization marker: %w", err)
	}
	if marker.Version != checkpointStateVersion || marker.SessionID != s.sessionID {
		return errors.New("checkpoint initialization marker does not match this runtime")
	}
	for _, directory := range []string{s.root, s.leasesDir()} {
		if err := requireDirectoryNoFollow(directory); err != nil {
			return err
		}
	}
	for _, file := range []string{s.lockPath(), s.statePath()} {
		if err := requireRegularFileNoFollow(file); err != nil {
			return err
		}
	}
	return s.withLock(func() error {
		_, err := s.readStateLocked()
		return err
	})
}

func (s *checkpointSafePoint) writeInitializationMarker() error {
	data, err := json.Marshal(checkpointInitialization{
		Version: checkpointStateVersion, SessionID: s.sessionID,
	})
	if err != nil {
		return err
	}
	marker, err := openRegularFileNoFollow(
		s.markerPath(),
		os.O_CREATE|os.O_EXCL|os.O_WRONLY,
		0o600,
	)
	if err != nil {
		return fmt.Errorf("create checkpoint initialization marker: %w", err)
	}
	if _, err := marker.Write(data); err != nil {
		marker.Close()
		return err
	}
	if err := marker.Sync(); err != nil {
		marker.Close()
		return err
	}
	if err := marker.Close(); err != nil {
		return err
	}
	parent, err := openDirectoryNoFollow(s.storageRoot)
	if err != nil {
		return err
	}
	defer parent.Close()
	return parent.Sync()
}

// Checkpoint files are shared only by mutually trusted telosd processes under
// one Unix UID. These checks prevent accidental path substitution; they do not
// claim to isolate malicious tools running as that same trusted UID.
func pathExistsNoFollow(path string) (bool, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func openDirectoryNoFollow(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("path is not a trusted directory: %s", path)
	}
	file, err := os.OpenFile(
		path,
		os.O_RDONLY|syscall.O_CLOEXEC|syscall.O_DIRECTORY|syscall.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if !opened.IsDir() {
		file.Close()
		return nil, fmt.Errorf("opened path is not a directory: %s", path)
	}
	return file, nil
}

func requireDirectoryNoFollow(path string) error {
	directory, err := openDirectoryNoFollow(path)
	if err != nil {
		return err
	}
	return directory.Close()
}

func openRegularFileNoFollow(path string, flags int, mode os.FileMode) (*os.File, error) {
	file, err := os.OpenFile(path, flags|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, mode)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return nil, fmt.Errorf("path is not a regular file: %s", path)
	}
	return file, nil
}

func requireRegularFileNoFollow(path string) error {
	file, err := openRegularFileNoFollow(path, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	return file.Close()
}

func readRegularFileNoFollow(path string, limit int64) ([]byte, error) {
	file, err := openRegularFileNoFollow(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("metadata file exceeds %d bytes", limit)
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("metadata file exceeds %d bytes", limit)
	}
	return data, nil
}

func (s *checkpointSafePoint) withLock(fn func() error) error {
	if err := requireDirectoryNoFollow(s.root); err != nil {
		return fmt.Errorf("validate checkpoint state root: %w", err)
	}
	lock, err := openRegularFileNoFollow(s.lockPath(), os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open checkpoint lock: %w", err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock checkpoint state: %w", err)
	}
	defer func() {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	}()
	return fn()
}

func (s *checkpointSafePoint) readStateLocked() (checkpointState, error) {
	data, err := readRegularFileNoFollow(s.statePath(), checkpointMaxMetadataBytes)
	if errors.Is(err, os.ErrNotExist) {
		return checkpointState{}, errors.New("checkpoint state is missing")
	}
	if err != nil {
		return checkpointState{}, fmt.Errorf("read checkpoint state: %w", err)
	}
	var state checkpointState
	if err := json.Unmarshal(data, &state); err != nil {
		return checkpointState{}, fmt.Errorf("parse checkpoint state: %w", err)
	}
	if state.Version != checkpointStateVersion || state.SessionID != s.sessionID {
		return checkpointState{}, errors.New("checkpoint state does not match this runtime")
	}
	if state.Status != "open" && state.Status != "preparing" && state.Status != "prepared" {
		return checkpointState{}, fmt.Errorf("invalid checkpoint state %q", state.Status)
	}
	if state.OperationID != "" && !checkpointOperationIDRE.MatchString(state.OperationID) {
		return checkpointState{}, errors.New("checkpoint state has an invalid operation ID")
	}
	if state.Status != "open" && state.OperationID == "" {
		return checkpointState{}, errors.New("closed checkpoint state has no operation owner")
	}
	return state, nil
}

func (s *checkpointSafePoint) writeStateLocked(status, operationID string) error {
	data, err := json.Marshal(checkpointState{
		Version: checkpointStateVersion, SessionID: s.sessionID, OperationID: operationID, Status: status,
	})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.root, ".state-*")
	if err != nil {
		return fmt.Errorf("create checkpoint state: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if info, err := tmp.Stat(); err != nil || !info.Mode().IsRegular() {
		tmp.Close()
		if err != nil {
			return err
		}
		return errors.New("checkpoint temporary state is not a regular file")
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	stateExists, err := pathExistsNoFollow(s.statePath())
	if err != nil {
		return err
	}
	if stateExists {
		if err := requireRegularFileNoFollow(s.statePath()); err != nil {
			return err
		}
	}
	if err := os.Rename(tmpPath, s.statePath()); err != nil {
		return fmt.Errorf("replace checkpoint state: %w", err)
	}
	dir, err := openDirectoryNoFollow(s.root)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (s *checkpointSafePoint) acquire() (*checkpointLease, error) {
	var lease *checkpointLease
	err := s.withLock(func() error {
		state, err := s.readStateLocked()
		if err != nil {
			return err
		}
		if state.Status != "open" {
			return errCheckpointAdmissionClosed
		}
		if err := requireDirectoryNoFollow(s.leasesDir()); err != nil {
			return fmt.Errorf("validate checkpoint leases directory: %w", err)
		}
		var nonce [12]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return fmt.Errorf("create checkpoint lease ID: %w", err)
		}
		path := filepath.Join(s.leasesDir(), fmt.Sprintf("%d-%s.lease", os.Getpid(), hex.EncodeToString(nonce[:])))
		file, err := openRegularFileNoFollow(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("create checkpoint lease: %w", err)
		}
		if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			file.Close()
			os.Remove(path)
			return fmt.Errorf("lock checkpoint lease: %w", err)
		}
		lease = &checkpointLease{path: path, file: file}
		return nil
	})
	return lease, err
}

func (l *checkpointLease) release() {
	if l == nil {
		return
	}
	if l.file != nil {
		_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
		_ = l.file.Close()
		l.file = nil
	}
	if l.path != "" {
		_ = os.Remove(l.path)
		l.path = ""
	}
}

func (s *checkpointSafePoint) prepare(ctx context.Context, operationID string) (string, int, error) {
	operationID = strings.TrimSpace(operationID)
	if !checkpointOperationIDRE.MatchString(operationID) {
		return "error", 0, errors.New("invalid checkpoint operation ID")
	}
	if err := s.withLock(func() error {
		state, err := s.readStateLocked()
		if err != nil {
			return err
		}
		switch state.Status {
		case "open":
			if state.OperationID == operationID {
				return errCheckpointOperationCompleted
			}
			if s.prepareCheck != nil {
				if err := s.prepareCheck(); err != nil {
					return err
				}
			}
			return s.writeStateLocked("preparing", operationID)
		case "preparing", "prepared":
			if state.OperationID != operationID {
				return errCheckpointOperationMismatch
			}
			return nil
		default:
			return fmt.Errorf("invalid checkpoint state %q", state.Status)
		}
	}); err != nil {
		return "error", 0, err
	}

	deadlineCtx, cancel := context.WithTimeout(ctx, s.prepareTimeout)
	defer cancel()
	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()
	for {
		status, count, err := s.tryFinishPrepare(operationID)
		if err != nil || status == "prepared" {
			return status, count, err
		}
		select {
		case <-deadlineCtx.Done():
			return "timeout", count, nil
		case <-ticker.C:
		}
	}
}

func (s *checkpointSafePoint) tryFinishPrepare(operationID string) (string, int, error) {
	status := "preparing"
	count := 0
	err := s.withLock(func() error {
		state, err := s.readStateLocked()
		if err != nil {
			return err
		}
		if state.OperationID != operationID {
			return errCheckpointOperationMismatch
		}
		if state.Status == "open" {
			return errCheckpointOperationCompleted
		}
		if state.Status == "prepared" {
			status = "prepared"
			return nil
		}
		count, err = s.activeLeasesLocked()
		if err != nil || count != 0 {
			return err
		}
		if s.prepareCheck != nil {
			if err := s.prepareCheck(); err != nil {
				return err
			}
		}
		if err := s.writeStateLocked("prepared", operationID); err != nil {
			return err
		}
		status = "prepared"
		return nil
	})
	return status, count, err
}

func (s *checkpointSafePoint) activeLeasesLocked() (int, error) {
	if err := requireDirectoryNoFollow(s.leasesDir()); err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(s.leasesDir())
	if err != nil {
		return 0, err
	}
	active := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".lease") {
			continue
		}
		path := filepath.Join(s.leasesDir(), entry.Name())
		file, err := openRegularFileNoFollow(path, os.O_RDWR, 0)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return 0, fmt.Errorf("open checkpoint lease: %w", err)
		}
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
			_ = file.Close()
			_ = os.Remove(path)
			continue
		}
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			active++
			continue
		}
		return 0, fmt.Errorf("inspect checkpoint lease: %w", err)
	}
	return active, nil
}

func (s *checkpointSafePoint) resume(operationID string) error {
	operationID = strings.TrimSpace(operationID)
	if !checkpointOperationIDRE.MatchString(operationID) {
		return errors.New("invalid checkpoint operation ID")
	}
	return s.withLock(func() error {
		state, err := s.readStateLocked()
		if err != nil {
			return err
		}
		if state.OperationID != operationID {
			return errCheckpointOperationMismatch
		}
		if state.Status == "open" {
			return nil
		}
		return s.writeStateLocked("open", operationID)
	})
}

func withCheckpointLease(safePoint *checkpointSafePoint, work func() error) error {
	if safePoint == nil {
		return work()
	}
	lease, err := safePoint.acquire()
	if err != nil {
		return err
	}
	defer lease.release()
	return work()
}

type checkpointStore struct {
	sessionapi.Store
	safePoint *checkpointSafePoint
}

func (s checkpointStore) Create(req sessionapi.SessionCreateRequest) (*sessionapi.Session, error) {
	lease, err := s.safePoint.acquire()
	if err != nil {
		if errors.Is(err, errCheckpointAdmissionClosed) {
			return nil, fmt.Errorf("checkpoint preparation has closed new work: %w", sessionapi.ErrConflict)
		}
		return nil, err
	}
	defer lease.release()
	return s.Store.Create(req)
}

func (s checkpointStore) UpdateSpec(name string, req sessionapi.SessionSpecUpdateRequest) (*sessionapi.SessionSpecUpdateResponse, error) {
	lease, err := s.safePoint.acquire()
	if err != nil {
		if errors.Is(err, errCheckpointAdmissionClosed) {
			return nil, fmt.Errorf("checkpoint preparation has closed new work: %w", sessionapi.ErrConflict)
		}
		return nil, err
	}
	defer lease.release()
	return s.Store.UpdateSpec(name, req)
}

func (s checkpointStore) Stop(id string) (*sessionapi.Session, error) {
	lease, err := s.safePoint.acquire()
	if err != nil {
		if errors.Is(err, errCheckpointAdmissionClosed) {
			return nil, fmt.Errorf("checkpoint preparation has closed new work: %w", sessionapi.ErrConflict)
		}
		return nil, err
	}
	defer lease.release()
	return s.Store.Stop(id)
}

type checkpointHandler struct {
	safePoint  *checkpointSafePoint
	authorizer sessionapi.Authorizer
}

type checkpointRequest struct {
	SessionID   string `json:"session_id"`
	OperationID string `json:"operation_id"`
}

type checkpointResponse struct {
	Status      string `json:"status"`
	SessionID   string `json:"session_id"`
	OperationID string `json:"operation_id"`
	InFlight    int    `json:"in_flight"`
}

func registerCheckpointRoutes(mux *http.ServeMux, safePoint *checkpointSafePoint, authorizer sessionapi.Authorizer) {
	h := checkpointHandler{safePoint: safePoint, authorizer: authorizer}
	mux.HandleFunc("POST /internal/checkpoint/prepare", h.prepare)
	mux.HandleFunc("POST /internal/checkpoint/resume", h.resume)
}

func (h checkpointHandler) prepare(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeRequest(w, r)
	if !ok {
		return
	}
	status, inFlight, err := h.safePoint.prepare(r.Context(), req.OperationID)
	if err != nil {
		switch {
		case errors.Is(err, errCheckpointWorkersNotReady):
			writeCheckpointError(w, http.StatusConflict, "checkpoint_workers_not_ready", "runtime workers are still upgrading for checkpoint safety", true)
		case errors.Is(err, errCheckpointOperationMismatch):
			writeCheckpointError(w, http.StatusConflict, "checkpoint_operation_mismatch", "another checkpoint operation owns runtime admission", false)
		case errors.Is(err, errCheckpointOperationCompleted):
			writeCheckpointError(w, http.StatusConflict, "checkpoint_operation_completed", "this checkpoint operation already completed", false)
		default:
			writeCheckpointError(w, http.StatusInternalServerError, "checkpoint_prepare_failed", "checkpoint preparation failed", true)
		}
		return
	}
	if status == "timeout" {
		writeCheckpointError(w, http.StatusConflict, "checkpoint_prepare_timeout", "runtime work did not drain before the checkpoint deadline", true)
		return
	}
	writeCheckpointJSON(w, http.StatusOK, checkpointResponse{
		Status: status, SessionID: req.SessionID, OperationID: req.OperationID, InFlight: inFlight,
	})
}

func (h checkpointHandler) resume(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeRequest(w, r)
	if !ok {
		return
	}
	if err := h.safePoint.resume(req.OperationID); err != nil {
		if errors.Is(err, errCheckpointOperationMismatch) {
			writeCheckpointError(w, http.StatusConflict, "checkpoint_operation_mismatch", "another checkpoint operation owns runtime admission", false)
			return
		}
		writeCheckpointError(w, http.StatusInternalServerError, "checkpoint_resume_failed", "checkpoint admission could not be reopened", true)
		return
	}
	writeCheckpointJSON(w, http.StatusOK, checkpointResponse{
		Status: "resumed", SessionID: req.SessionID, OperationID: req.OperationID,
	})
}

func (h checkpointHandler) authorizeRequest(w http.ResponseWriter, r *http.Request) (checkpointRequest, bool) {
	var req checkpointRequest
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil ||
		!checkpointSessionIDRE.MatchString(req.SessionID) ||
		!checkpointOperationIDRE.MatchString(req.OperationID) {
		writeCheckpointError(w, http.StatusBadRequest, "invalid_request", "invalid request body", false)
		return checkpointRequest{}, false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeCheckpointError(w, http.StatusBadRequest, "invalid_request", "invalid request body", false)
		return checkpointRequest{}, false
	}
	_, err := h.authorizer.Caller(r, sessionapi.AccessRequest{
		Action: sessionapi.ActionCheckpointSafePoint, SessionID: req.SessionID,
	})
	if err != nil {
		if status, detail, ok := sessionapi.AuthHTTPError(err); ok {
			code := "access_denied"
			if status == http.StatusUnauthorized {
				code = "unauthorized"
			} else if status == http.StatusForbidden {
				code = "forbidden"
			}
			writeCheckpointError(w, status, code, detail, false)
			return checkpointRequest{}, false
		}
		writeCheckpointError(w, http.StatusInternalServerError, "authorization_failed", "authorization failed", true)
		return checkpointRequest{}, false
	}
	if req.SessionID != h.safePoint.sessionID {
		writeCheckpointError(w, http.StatusConflict, "checkpoint_session_mismatch", "runtime session does not match", false)
		return checkpointRequest{}, false
	}
	return req, true
}

func writeCheckpointJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeCheckpointError(w http.ResponseWriter, status int, code, message string, retryable bool) {
	writeCheckpointJSON(w, status, map[string]any{
		"error": map[string]any{
			"code": code, "message": message, "retryable": retryable,
		},
	})
}

package sessionapi

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/telos-org/telos/internal/spec"
)

// ErrNotFound is returned when a session or artifact does not exist.
var ErrNotFound = errors.New("not found")

// ErrInvalidSession is returned when a request targets the wrong session kind
// or would violate session identity.
var ErrInvalidSession = errors.New("invalid session")

// ErrConflict is returned when a request would create duplicate live state.
var ErrConflict = errors.New("conflict")

var specDirNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
var cloudSessionIDRE = regexp.MustCompile(`^sess_[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

func PackagePathForDigest(root string, digest string) (string, error) {
	digest = strings.TrimSpace(digest)
	hex, ok := strings.CutPrefix(digest, "sha256:")
	if !ok || len(hex) != 64 {
		return "", fmt.Errorf("invalid package digest %q", digest)
	}
	return filepath.Join(root, "blobs", "sha256", hex, "package.tar.gz"), nil
}

// VerifyPackageDigest validates that packagePath is a readable package whose
// manifest-derived digest matches expectedDigest.
func VerifyPackageDigest(packagePath string, expectedDigest string) error {
	dir, err := os.MkdirTemp("", "telos-apply-package-verify-")
	if err != nil {
		return fmt.Errorf("create package verify dir: %w", err)
	}
	defer os.RemoveAll(dir)
	if _, err := prepareApplyPackageSpec(dir, packagePath, expectedDigest); err != nil {
		return err
	}
	return nil
}

// Store is the persistence interface for sessions. Implementations are
// expected to be safe for concurrent use.
type Store interface {
	Create(req SessionCreateRequest) (*Session, error)
	Spec(id string) (*SessionSpecResponse, error)
	UpdateSpec(name string, req SessionSpecUpdateRequest) (*SessionSpecUpdateResponse, error)
	List() ([]Session, error)
	Get(id string) (*Session, error)
	Stop(id string) (*Session, error)
	Transcript(id string) (string, error)
	Events(id string) ([]SessionEvent, error)
}

// --------- FileStore ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------

// FileStore is a local file-backed Store that writes session manifests under a
// root directory (typically .telos/sessions).
type FileStore struct {
	Root         string
	PackageRoot  string
	OnSpecUpdate func(SpecUpdateEvent)
	runtime      SessionRuntime
	launcher     string
	mu           sync.Mutex
}

// NewFileStore returns a FileStore rooted at the given directory.
func NewFileStore(root string, runtime SessionRuntime) *FileStore {
	if runtime == "" {
		runtime = RuntimeLocal
	}
	launcher := "local"
	if runtime == RuntimeCloud {
		launcher = "telosd"
	}
	return &FileStore{Root: root, runtime: runtime, launcher: launcher}
}

func (fs *FileStore) sessionDir(id string) string {
	return filepath.Join(fs.Root, id)
}

func (fs *FileStore) manifestPath(id string) string {
	return filepath.Join(fs.sessionDir(id), "session.json")
}

// Create persists a new session manifest and returns the derived Session.
func (fs *FileStore) Create(req SessionCreateRequest) (*Session, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	return fs.createLocked(req)
}

func (fs *FileStore) createLocked(req SessionCreateRequest) (*Session, error) {
	if err := validateCreateRequest(req); err != nil {
		return nil, err
	}
	if err := fs.normalizeCreatePackage(&req); err != nil {
		return nil, err
	}
	if err := fs.validateParentage(req); err != nil {
		return nil, err
	}

	sessionKind, err := fs.sessionKindForCreate(req)
	if err != nil {
		return nil, err
	}
	id, dir, err := fs.createSessionDir(req, sessionKind)
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(dir)
		}
	}()

	prepared, err := prepareRequestSpec(dir, req)
	if err != nil {
		return nil, err
	}
	specName := prepared.Name
	if sessionKind == KindController {
		ids, err := fs.liveTopLevelSessionIDsBySpecName(specName)
		if err != nil {
			return nil, err
		}
		if len(ids) > 0 {
			return nil, fmt.Errorf("root session %q already exists as %s: %w", specName, strings.Join(ids, ", "), ErrConflict)
		}
	}
	access, err := NewScopedToken(id, sessionKind)
	if err != nil {
		return nil, err
	}
	specDir := filepath.Join(dir, "specs", specName)
	if err := os.MkdirAll(specDir, 0o755); err != nil {
		return nil, fmt.Errorf("create spec dir: %w", err)
	}

	evidencePath := filepath.Join(specDir, "evidence.jsonl")
	transcriptPath := filepath.Join(specDir, fmt.Sprintf("transcript-%s.md", id))
	workspacePath := filepath.Join(specDir, "workspace.tar.gz")
	sessionSpecPath := ""
	if len(prepared.SpecData) > 0 {
		sessionSpecPath = filepath.Join(specDir, "spec.md")
		if err := writeFileAtomic(sessionSpecPath, prepared.SpecData, 0o644); err != nil {
			return nil, fmt.Errorf("write session spec: %w", err)
		}
	}
	prepared.SessionSpecPath = strPtr(sessionSpecPath)

	provenance := map[string]any{"mode": runtimeMode(fs.runtime)}
	if cloudSessionID := strings.TrimSpace(req.CloudSessionID); cloudSessionID != "" {
		provenance["cloud_session_id"] = cloudSessionID
	}
	if cloudSessionName := strings.TrimSpace(req.CloudSessionName); cloudSessionName != "" {
		provenance["cloud_session_name"] = cloudSessionName
	}
	m := ManifestFromInitial(InitialManifest{
		SessionID:        id,
		SessionKind:      sessionKind,
		Runtime:          fs.runtime,
		CreatedAt:        tsNow(),
		Launcher:         fs.launcher,
		ParentSessionID:  req.ParentSessionID,
		SourceSpecPath:   prepared.SourceSpecPath,
		SessionSpecPath:  prepared.SessionSpecPath,
		SpecName:         specName,
		Config:           buildConfig(req),
		Provenance:       provenance,
		PackageDigest:    prepared.PackageDigest,
		ApplyPackageLock: prepared.ApplyPackageLock,
		Access:           access,
		Specs: []InitialManifestSpec{{
			Index:           0,
			Name:            specName,
			DirName:         specName,
			SessionSpecPath: prepared.SessionSpecPath,
			ContentHash:     prepared.ContentHash,
			EvidencePath:    &evidencePath,
			TranscriptPath:  &transcriptPath,
			WorkspacePath:   &workspacePath,
			IntervalSeconds: prepared.IntervalSeconds,
		}},
	})
	if isTopLevelManifest(&m) && sessionSpecPath != "" {
		version := 1
		m.CurrentSpecVersion = &version
		m.SpecVersions = append(
			m.SpecVersions,
			specVersionEntry(version, sessionSpecPath, prepared.SpecData, nil, prepared.PackageDigest),
		)
	}

	if err := WriteManifest(fs.manifestPath(id), &m); err != nil {
		return nil, err
	}

	session, err := fs.deriveSession(id, &m)
	if err != nil {
		return nil, err
	}
	committed = true
	return session, nil
}

func (fs *FileStore) createSessionDir(req SessionCreateRequest, sessionKind SessionKind) (string, string, error) {
	if err := os.MkdirAll(fs.Root, 0o755); err != nil {
		return "", "", fmt.Errorf("create sessions root: %w", err)
	}
	if id := strings.TrimSpace(req.CloudSessionID); id != "" {
		if fs.runtime != RuntimeCloud || sessionKind != KindController {
			return "", "", fmt.Errorf("cloud_session_id is only valid for cloud controller sessions: %w", ErrInvalidSession)
		}
		return fs.createSessionDirWithID(id, true)
	}

	for attempt := 0; attempt < 16; attempt++ {
		id := generateSessionID(fs.runtime)
		createdID, dir, err := fs.createSessionDirWithID(id, false)
		if err != nil {
			if errors.Is(err, ErrConflict) {
				continue
			}
			return "", "", err
		}
		return createdID, dir, nil
	}
	return "", "", fmt.Errorf("create session dir: exhausted session id retries")
}

func (fs *FileStore) createSessionDirWithID(id string, validateCloudID bool) (string, string, error) {
	if validateCloudID && !cloudSessionIDRE.MatchString(id) {
		return "", "", fmt.Errorf("invalid session id %q: %w", id, ErrInvalidSession)
	}
	dir := fs.sessionDir(id)
	if err := os.Mkdir(dir, 0o755); err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", "", fmt.Errorf("session %s already exists: %w", id, ErrConflict)
		}
		return "", "", fmt.Errorf("create session dir: %w", err)
	}
	return id, dir, nil
}

func validateCreateRequest(req SessionCreateRequest) error {
	if req.Until != nil && *req.Until <= 0 {
		return fmt.Errorf("until must be positive: %w", ErrInvalidSession)
	}
	hasPackage := strings.TrimSpace(req.PackageDigest) != "" || strings.TrimSpace(req.PackagePath) != ""
	hasMarkdown := req.SpecMarkdown != nil && strings.TrimSpace(*req.SpecMarkdown) != ""
	if hasPackage && hasMarkdown {
		return fmt.Errorf("set either spec_markdown or package_digest, not both: %w", ErrInvalidSession)
	}
	if !hasPackage && !hasMarkdown {
		return fmt.Errorf("spec_markdown or package_digest is required: %w", ErrInvalidSession)
	}
	if strings.TrimSpace(req.PackagePath) != "" && strings.TrimSpace(req.PackageDigest) == "" {
		return fmt.Errorf("package_digest is required with package_path: %w", ErrInvalidSession)
	}
	return nil
}

func (fs *FileStore) normalizeCreatePackage(req *SessionCreateRequest) error {
	if req == nil || strings.TrimSpace(req.PackagePath) != "" {
		return nil
	}
	digest := strings.TrimSpace(req.PackageDigest)
	if digest == "" {
		return nil
	}
	path, err := fs.packagePathForDigest(digest)
	if err != nil {
		return err
	}
	req.PackagePath = path
	return nil
}

func (fs *FileStore) validateParentage(req SessionCreateRequest) error {
	parentID := strings.TrimSpace(ptrOr(req.ParentSessionID, ""))
	if parentID == "" {
		return nil
	}
	parent, err := ReadManifest(fs.manifestPath(parentID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if parent.ParentSessionID != nil && strings.TrimSpace(*parent.ParentSessionID) != "" {
		return fmt.Errorf("child sessions cannot spawn child sessions: %w", ErrInvalidSession)
	}
	return nil
}

func (fs *FileStore) sessionKindForCreate(req SessionCreateRequest) (SessionKind, error) {
	if req.SessionKind != nil {
		switch *req.SessionKind {
		case KindController:
			if req.ParentSessionID != nil {
				return "", fmt.Errorf("child sessions cannot use root worker kind: %w", ErrInvalidSession)
			}
			return KindController, nil
		case KindTask:
			return KindTask, nil
		default:
			return "", fmt.Errorf("invalid session_kind %q: %w", *req.SessionKind, ErrInvalidSession)
		}
	}
	if req.ParentSessionID != nil {
		return KindTask, nil
	}
	if fs.runtime == RuntimeCloud {
		return KindController, nil
	}
	return KindTask, nil
}

func isTopLevelManifest(m *Manifest) bool {
	return m.ParentSessionID == nil || *m.ParentSessionID == ""
}

func (fs *FileStore) liveTopLevelSessionIDsBySpecName(specName string) ([]string, error) {
	return fs.topLevelSessionIDsBySpecName(specName, func(id string, m *Manifest) bool {
		return !deriveStatus(fs.sessionDir(id), m).IsTerminal()
	})
}

func (fs *FileStore) updatableTopLevelSessionIDsBySpecName(specName string) ([]string, error) {
	return fs.topLevelSessionIDsBySpecName(specName, func(_ string, m *Manifest) bool {
		return !m.IsStopped()
	})
}

func (fs *FileStore) topLevelSessionIDsBySpecName(specName string, include func(id string, m *Manifest) bool) ([]string, error) {
	entries, err := os.ReadDir(fs.Root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id := entry.Name()
		m, err := ReadManifest(fs.manifestPath(id))
		if err != nil {
			continue
		}
		if !isTopLevelManifest(m) {
			continue
		}
		if m.SpecName != specName {
			continue
		}
		if include != nil && !include(id, m) {
			continue
		}
		ids = append(ids, m.SessionID)
	}
	sort.Strings(ids)
	return ids, nil
}

// Spec returns the mutable root spec currently attached to a session.
func (fs *FileStore) Spec(id string) (*SessionSpecResponse, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	m, err := ReadManifest(fs.manifestPath(id))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("session %s: %w", id, ErrNotFound)
		}
		return nil, err
	}
	if !isTopLevelManifest(m) {
		return nil, fmt.Errorf("child sessions do not have mutable specs: %w", ErrInvalidSession)
	}
	if m.SessionSpecPath == nil || *m.SessionSpecPath == "" {
		return nil, fmt.Errorf("root session has no mutable spec: %w", ErrInvalidSession)
	}
	data, err := os.ReadFile(*m.SessionSpecPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("session spec file missing on disk: %w", ErrNotFound)
		}
		return nil, err
	}
	dirName := m.SpecName
	for _, item := range m.Specs {
		if item.SessionSpecPath != nil && *item.SessionSpecPath == *m.SessionSpecPath {
			if item.DirName != "" {
				dirName = item.DirName
			}
			break
		}
	}
	return &SessionSpecResponse{
		DirName:     dirName,
		Markdown:    string(data),
		Environment: frontmatterJSON(string(data)),
		Version:     m.CurrentSpecVersion,
	}, nil
}

// UpdateSpec creates or updates the active root session named by the spec.
func (fs *FileStore) UpdateSpec(name string, req SessionSpecUpdateRequest) (*SessionSpecUpdateResponse, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	prepared, err := fs.prepareSpecForUpdate(req)
	if err != nil {
		return nil, err
	}
	if prepared.Name != name {
		return nil, fmt.Errorf("spec name %q does not match route name %q: %w", prepared.Name, name, ErrInvalidSession)
	}

	ids, err := fs.updatableTopLevelSessionIDsBySpecName(name)
	if err != nil {
		return nil, err
	}
	switch len(ids) {
	case 0:
		kind := KindController
		createReq, err := fs.createRequestForSpecUpdate(req, &kind)
		if err != nil {
			return nil, err
		}
		session, err := fs.createLocked(createReq)
		if err != nil {
			return nil, err
		}
		return &SessionSpecUpdateResponse{Operation: "created", Session: session}, nil
	case 1:
		session, changed, err := fs.updateSpecByIDLocked(ids[0], req)
		if err != nil {
			return nil, err
		}
		if !changed {
			return &SessionSpecUpdateResponse{Operation: "unchanged", Session: session}, nil
		}
		return &SessionSpecUpdateResponse{Operation: "updated", Session: session}, nil
	default:
		return nil, fmt.Errorf("multiple active root sessions named %q: %s: %w", name, strings.Join(ids, ", "), ErrConflict)
	}
}

func (fs *FileStore) UpdateSpecByID(id string, req SessionSpecUpdateRequest) (*SessionSpecUpdateResponse, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	session, changed, err := fs.updateSpecByIDLocked(id, req)
	if err != nil {
		return nil, err
	}
	if !changed {
		return &SessionSpecUpdateResponse{Operation: "unchanged", Session: session}, nil
	}
	return &SessionSpecUpdateResponse{Operation: "updated", Session: session}, nil
}

func (fs *FileStore) prepareSpecForUpdate(req SessionSpecUpdateRequest) (preparedRequestSpec, error) {
	if err := os.MkdirAll(fs.Root, 0o755); err != nil {
		return preparedRequestSpec{}, fmt.Errorf("create sessions root: %w", err)
	}
	dir, err := os.MkdirTemp(fs.Root, ".apply-")
	if err != nil {
		return preparedRequestSpec{}, fmt.Errorf("create apply compile dir: %w", err)
	}
	defer os.RemoveAll(dir)
	createReq, err := fs.createRequestForSpecUpdate(req, nil)
	if err != nil {
		return preparedRequestSpec{}, err
	}
	return prepareRequestSpec(dir, createReq)
}

func (fs *FileStore) createRequestForSpecUpdate(req SessionSpecUpdateRequest, kind *SessionKind) (SessionCreateRequest, error) {
	packageDigest := strings.TrimSpace(req.PackageDigest)
	packagePath := strings.TrimSpace(req.PackagePath)
	hasPackage := packageDigest != "" || packagePath != ""
	hasMarkdown := strings.TrimSpace(req.SpecMarkdown) != ""
	if hasPackage && hasMarkdown {
		return SessionCreateRequest{}, fmt.Errorf("set either spec_markdown or package_digest, not both: %w", ErrInvalidSession)
	}
	if !hasPackage && !hasMarkdown {
		return SessionCreateRequest{}, fmt.Errorf("spec_markdown or package_digest is required: %w", ErrInvalidSession)
	}
	if packagePath == "" && packageDigest != "" {
		resolved, err := fs.packagePathForDigest(packageDigest)
		if err != nil {
			return SessionCreateRequest{}, err
		}
		packagePath = resolved
	}
	if packagePath != "" {
		return SessionCreateRequest{
			PackagePath:     packagePath,
			PackageDigest:   packageDigest,
			SessionKind:     kind,
			Model:           req.Model,
			Thinking:        req.Thinking,
			MaxCostUSD:      req.MaxCostUSD,
			AgentTimeoutSec: req.AgentTimeoutSec,
		}, nil
	}
	markdown := strings.TrimRight(req.SpecMarkdown, "\n") + "\n"
	return SessionCreateRequest{
		SpecMarkdown:    &markdown,
		SessionKind:     kind,
		Model:           req.Model,
		Thinking:        req.Thinking,
		MaxCostUSD:      req.MaxCostUSD,
		AgentTimeoutSec: req.AgentTimeoutSec,
	}, nil
}

func (fs *FileStore) packagePathForDigest(digest string) (string, error) {
	root := strings.TrimSpace(fs.PackageRoot)
	if root == "" {
		return "", fmt.Errorf("package_root is required for package_digest updates")
	}
	return PackagePathForDigest(root, digest)
}

func (fs *FileStore) updateSpecByIDLocked(id string, req SessionSpecUpdateRequest) (*Session, bool, error) {
	m, err := ReadManifest(fs.manifestPath(id))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, fmt.Errorf("session %s: %w", id, ErrNotFound)
		}
		return nil, false, err
	}
	createReq, err := fs.createRequestForSpecUpdate(req, nil)
	if err != nil {
		return nil, false, err
	}
	prepared, err := prepareRequestSpec(fs.sessionDir(id), createReq)
	if err != nil {
		return nil, false, err
	}
	var changed bool
	var previousVersion int
	var currentVersion int
	var previousPackageDigest string
	var previousSpecSHA256 string
	var versionEntry map[string]any
	m, err = MutateManifest(fs.manifestPath(id), func(m *Manifest) error {
		if m.ParentSessionID != nil && *m.ParentSessionID != "" {
			return fmt.Errorf("child sessions do not have mutable specs: %w", ErrInvalidSession)
		}
		if m.SessionKind != KindController {
			return fmt.Errorf("only controller sessions have mutable specs: %w", ErrInvalidSession)
		}
		if m.IsStopped() {
			return fmt.Errorf("stopped controller sessions do not have mutable specs: %w", ErrInvalidSession)
		}
		if m.SessionSpecPath == nil || *m.SessionSpecPath == "" {
			return fmt.Errorf("root session has no mutable spec: %w", ErrInvalidSession)
		}
		if prepared.Name != m.SpecName {
			return fmt.Errorf("root session spec name is immutable: %w", ErrInvalidSession)
		}
		previousVersion = ptrOr(m.CurrentSpecVersion, 1)
		previousPackageDigest = strValue(m.PackageDigest)
		previousSpecSHA256 = latestSpecSHA256(m.SpecVersions)
		if previousSpecSHA256 == specSHA256(prepared.SpecData) && previousPackageDigest == strValue(prepared.PackageDigest) {
			changed = false
			return nil
		}
		if err := writeFileAtomic(*m.SessionSpecPath, prepared.SpecData, 0o644); err != nil {
			return fmt.Errorf("write session spec: %w", err)
		}
		currentVersion = previousVersion + 1
		versionEntry = specVersionEntry(currentVersion, *m.SessionSpecPath, prepared.SpecData, &previousVersion, prepared.PackageDigest)
		m.CurrentSpecVersion = &currentVersion
		m.SpecVersions = append(m.SpecVersions, versionEntry)
		m.PackageDigest = prepared.PackageDigest
		m.ApplyPackageLock = prepared.ApplyPackageLock
		m.SourceSpecPath = prepared.SourceSpecPath
		m.SessionSpecPath = strPtr(*m.SessionSpecPath)
		if len(m.Specs) > 0 {
			m.Specs[0].Name = prepared.Name
			m.Specs[0].DirName = prepared.Name
			m.Specs[0].SessionSpecPath = m.SessionSpecPath
			m.Specs[0].ContentHash = prepared.ContentHash
			m.Specs[0].IntervalSeconds = prepared.IntervalSeconds
		}
		changed = true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if !changed {
		session, err := fs.deriveSession(id, m)
		return session, false, err
	}
	fs.emitSpecUpdate(specUpdateEvent(id, m, SpecUpdateEvent{
		PreviousSpecVersion:   previousVersion,
		CurrentSpecVersion:    currentVersion,
		PreviousSpecSHA256:    previousSpecSHA256,
		CurrentSpecSHA256:     stringMapValue(versionEntry, "spec_sha256"),
		PreviousPackageDigest: previousPackageDigest,
		CurrentPackageDigest:  strValue(prepared.PackageDigest),
		SpecPath:              *m.SessionSpecPath,
	}))
	session, err := fs.deriveSession(id, m)
	return session, true, err
}

// List returns all sessions ordered by creation time descending.
func (fs *FileStore) List() ([]Session, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	entries, err := os.ReadDir(fs.Root)
	if errors.Is(err, os.ErrNotExist) {
		return []Session{}, nil
	}
	if err != nil {
		return nil, err
	}

	sessions := make([]Session, 0)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		m, err := ReadManifest(fs.manifestPath(entry.Name()))
		if err != nil {
			continue // skip unreadable entries
		}
		s, err := fs.deriveSession(entry.Name(), m)
		if err != nil {
			continue
		}
		sessions = append(sessions, *s)
	}

	sort.Slice(sessions, func(i, j int) bool {
		a := ptrOr(sessions[i].CreatedAt, "")
		b := ptrOr(sessions[j].CreatedAt, "")
		return a > b // descending
	})

	return sessions, nil
}

// Get returns a single session by ID.
func (fs *FileStore) Get(id string) (*Session, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	m, err := ReadManifest(fs.manifestPath(id))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("session %s: %w", id, ErrNotFound)
		}
		return nil, err
	}
	return fs.deriveSession(id, m)
}

// Stop transitions a session to the stopped state.
func (fs *FileStore) Stop(id string) (*Session, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	m, err := ReadManifest(fs.manifestPath(id))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("session %s: %w", id, ErrNotFound)
		}
		return nil, err
	}

	s, _ := fs.deriveSession(id, m)
	if s.Status.IsTerminal() {
		return s, nil
	}

	var runner *Runner
	if m.Runner != nil {
		copy := *m.Runner
		runner = &copy
	} else if open := m.OpenEpoch(); open != nil && open.Runner != nil {
		copy := *open.Runner
		runner = &copy
	}

	m, err = MutateManifest(fs.manifestPath(id), func(m *Manifest) error {
		now := tsNow()
		stopped := "stopped"
		stoppedErr := "stopped by operator"

		if len(m.Epochs) == 0 {
			m.Epochs = append(m.Epochs, Epoch{
				ID:         1,
				StartedAt:  now,
				FinishedAt: &now,
				Result:     &stopped,
				Error:      &stoppedErr,
			})
		} else {
			last := &m.Epochs[len(m.Epochs)-1]
			last.FinishedAt = &now
			last.Result = &stopped
			last.Error = &stoppedErr
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	terminateRunner(runner)

	return fs.deriveSession(id, m)
}

// Transcript returns the session transcript markdown for the first spec.
func (fs *FileStore) Transcript(id string) (string, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	m, err := ReadManifest(fs.manifestPath(id))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("session %s: %w", id, ErrNotFound)
		}
		return "", err
	}

	if len(m.Specs) == 0 {
		return "", fmt.Errorf("session %s transcript: %w", id, ErrNotFound)
	}

	tp := m.Specs[0].TranscriptPath
	if tp == nil || *tp == "" {
		return "", fmt.Errorf("session %s transcript: %w", id, ErrNotFound)
	}

	data, err := os.ReadFile(*tp)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("session %s transcript: %w", id, ErrNotFound)
		}
		return "", err
	}
	return string(data), nil
}

// Events reads all evidence JSONL events for the session.
func (fs *FileStore) Events(id string) ([]SessionEvent, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	m, err := ReadManifest(fs.manifestPath(id))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("session %s: %w", id, ErrNotFound)
		}
		return nil, err
	}

	var events []SessionEvent
	for _, spec := range m.Specs {
		if spec.EvidencePath == nil || *spec.EvidencePath == "" {
			continue
		}
		specEvents, err := readEvidenceFile(*spec.EvidencePath, &spec)
		if err != nil {
			continue
		}
		events = append(events, specEvents...)
	}
	return events, nil
}

func (fs *FileStore) emitSpecUpdate(event SpecUpdateEvent) {
	if fs.OnSpecUpdate == nil {
		return
	}
	fs.OnSpecUpdate(event)
}

func specUpdateEvent(id string, m *Manifest, event SpecUpdateEvent) SpecUpdateEvent {
	if m == nil {
		return event
	}
	event.SessionID = id
	event.SpecName = m.SpecName
	event.SessionCreatedAt = m.CreatedAt
	if open := m.OpenEpoch(); open != nil {
		event.EpochID = open.ID
	}
	if len(m.Specs) == 0 {
		return event
	}
	spec := m.Specs[0]
	event.TranscriptPath = ptrOr(spec.TranscriptPath, "")
	event.EvidencePath = ptrOr(spec.EvidencePath, "")
	return event
}

// --------- Derivation (manifest -> Session) ------------------------------------------------------------------------------------------------------------------------

func (fs *FileStore) deriveSession(id string, m *Manifest) (*Session, error) {
	dir := fs.sessionDir(id)

	specs := make([]SessionSpec, len(m.Specs))
	for i, ms := range m.Specs {
		specs[i] = deriveSpec(ms)
	}

	epochs := make([]map[string]any, len(m.Epochs))
	for i, e := range m.Epochs {
		epochs[i] = epochToMap(e)
	}

	status := deriveStatus(dir, m)
	activeWorkspacePath := filepath.Join(dir, "workspace")
	activeWorkspaceExists := fileExists(activeWorkspacePath)
	var activeWorkspacePathPtr *string
	var activeWorkspaceExistsPtr *bool
	if activeWorkspaceExists || !status.IsTerminal() {
		activeWorkspacePathPtr = strPtr(activeWorkspacePath)
		activeWorkspaceExistsPtr = &activeWorkspaceExists
	}

	kind := m.SessionKind
	s := Session{
		SessionID:             m.SessionID,
		SessionKind:           &kind,
		ParentSessionID:       m.ParentSessionID,
		SpecName:              strPtr(m.SpecName),
		Status:                status,
		CreatedAt:             strPtr(m.CreatedAt),
		Runtime:               manifestRuntime(m, fs.runtime),
		Launcher:              strPtr(m.Launcher),
		SessionSpecPath:       m.SessionSpecPath,
		SessionDir:            strPtr(dir),
		ActiveWorkspacePath:   activeWorkspacePathPtr,
		ActiveWorkspaceExists: activeWorkspaceExistsPtr,
		Config:                m.Config.AsMap(),
		Workspace:             m.Workspace,
		Provenance:            m.Provenance,
		Specs:                 specs,
		Epochs:                epochs,
		CurrentSpecVersion:    m.CurrentSpecVersion,
		SpecVersions:          cloneSpecVersions(m.SpecVersions),
	}

	if s.Config == nil {
		s.Config = map[string]any{}
	}
	if s.Provenance == nil {
		s.Provenance = map[string]any{}
	}

	// Derive result/error from last epoch.
	if last := m.LastEpoch(); last != nil {
		s.Result = last.Result
		s.Error = last.Error
	}
	applySessionEvidenceSummary(&s)

	return &s, nil
}

func deriveSpec(ms ManifestSpec) SessionSpec {
	ss := SessionSpec{
		Index:           ms.Index,
		Name:            strPtr(ms.Name),
		DirName:         strPtr(ms.DirName),
		SessionSpecPath: ms.SessionSpecPath,
		ContentHash:     ms.ContentHash,
		EvidencePath:    ms.EvidencePath,
		TranscriptPath:  ms.TranscriptPath,
		WorkspacePath:   ms.WorkspacePath,
		IntervalSeconds: ms.IntervalSeconds,
	}

	if ms.EvidencePath != nil {
		exists := fileExists(*ms.EvidencePath)
		ss.EvidenceExists = &exists
	}
	if ms.TranscriptPath != nil {
		exists := fileExists(*ms.TranscriptPath)
		ss.TranscriptExists = &exists
	}
	if ms.WorkspacePath != nil {
		exists := fileExists(*ms.WorkspacePath)
		ss.WorkspaceExists = &exists
	}
	if summary, err := readEvidenceSummary(ms.EvidencePath); err == nil && summary != nil {
		ss.TotalCostUSD = summary.TotalCostUSD
		ss.TotalInputTokens = summary.TotalInputTokens
		ss.TotalOutputTokens = summary.TotalOutputTokens
		ss.TotalCacheReadTokens = summary.TotalCacheReadTokens
		ss.TotalCacheCreateTokens = summary.TotalCacheCreateTokens
		ss.RoundCount = summary.RoundCount
		ss.CompletionReason = summary.CompletionReason
		ss.VerifierConceded = summary.VerifierConceded
		ss.CurrentRound = summary.CurrentRound
		ss.CurrentRole = summary.CurrentRole
	}

	return ss
}

func applySessionEvidenceSummary(session *Session) {
	var totalCost float64
	var totalInputTokens int
	var totalOutputTokens int
	var totalCacheReadTokens int
	var totalCacheCreateTokens int
	var roundCount int
	var hasCost bool
	var hasInput bool
	var hasOutput bool
	var hasCacheRead bool
	var hasCacheCreate bool
	var hasRounds bool
	var completionReason *string
	var verifierConceded *bool
	var currentSpec *CurrentSpec
	var currentRound *int
	var currentRole *string

	for _, spec := range session.Specs {
		if spec.TotalCostUSD != nil {
			totalCost += *spec.TotalCostUSD
			hasCost = true
		}
		if spec.TotalInputTokens != nil {
			totalInputTokens += *spec.TotalInputTokens
			hasInput = true
		}
		if spec.TotalOutputTokens != nil {
			totalOutputTokens += *spec.TotalOutputTokens
			hasOutput = true
		}
		if spec.TotalCacheReadTokens != nil {
			totalCacheReadTokens += *spec.TotalCacheReadTokens
			hasCacheRead = true
		}
		if spec.TotalCacheCreateTokens != nil {
			totalCacheCreateTokens += *spec.TotalCacheCreateTokens
			hasCacheCreate = true
		}
		if spec.RoundCount != nil {
			roundCount += *spec.RoundCount
			hasRounds = true
		}
		if spec.CompletionReason != nil {
			completionReason = spec.CompletionReason
		}
		if spec.VerifierConceded != nil {
			verifierConceded = spec.VerifierConceded
		}
		if spec.CurrentRound != nil && spec.CurrentRole != nil {
			currentRound = spec.CurrentRound
			currentRole = spec.CurrentRole
			currentSpec = &CurrentSpec{
				Index:   spec.Index,
				Name:    spec.Name,
				DirName: spec.DirName,
			}
		}
	}

	if hasCost {
		session.TotalCostUSD = &totalCost
	}
	if hasInput {
		session.TotalInputTokens = &totalInputTokens
	}
	if hasOutput {
		session.TotalOutputTokens = &totalOutputTokens
	}
	if hasCacheRead {
		session.TotalCacheReadTokens = &totalCacheReadTokens
	}
	if hasCacheCreate {
		session.TotalCacheCreateTokens = &totalCacheCreateTokens
	}
	if hasRounds {
		session.RoundCount = &roundCount
	}
	session.CompletionReason = completionReason
	session.VerifierConceded = verifierConceded
	if !session.Status.IsTerminal() {
		session.CurrentSpec = currentSpec
		session.CurrentRound = currentRound
		session.CurrentRole = currentRole
	}
}

func deriveStatus(sessionDir string, m *Manifest) SessionStatus {
	open := m.OpenEpoch()
	last := m.LastEpoch()

	if m.Runner != nil {
		alive, err := runnerLockHeld(sessionDir)
		if err == nil && alive {
			return StatusRunning
		}
		if last == nil || last.Result == nil {
			return StatusStale
		}
	}

	if open != nil {
		if open.Runner != nil && open.Runner.InCluster {
			return StatusRunning
		}
		if pid, ok := open.Runner.ProcessID(); ok && pid == os.Getpid() {
			return StatusRunning
		}
		return StatusStale
	}
	if last != nil {
		if last.Result != nil {
			switch *last.Result {
			case "completed":
				if m.SessionKind == KindController && manifestRuntime(m, RuntimeLocal) == RuntimeCloud {
					return StatusRunning
				}
				return StatusCompleted
			case "failed":
				return StatusFailed
			case "stopped":
				return StatusStopped
			}
		}
		return StatusCompleted
	}
	return StatusPending
}

func runnerLockHeld(sessionDir string) (bool, error) {
	lock, err := os.OpenFile(filepath.Join(sessionDir, "runner.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false, err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return true, nil
		}
		return false, err
	}
	_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return false, nil
}

func terminateRunner(runner *Runner) {
	pid, ok := runner.ProcessID()
	if !ok {
		return
	}
	if pid == os.Getpid() {
		return
	}
	group := pid
	if pgid, ok := runner.ProcessGroupID(); ok {
		group = pgid
	}
	if err := syscall.Kill(-group, syscall.SIGTERM); err == nil || errors.Is(err, syscall.ESRCH) {
		return
	}
	_ = syscall.Kill(pid, syscall.SIGTERM)
}

func epochToMap(e Epoch) map[string]any {
	data, err := json.Marshal(e)
	if err != nil {
		return map[string]any{}
	}
	m := map[string]any{}
	if err := json.Unmarshal(data, &m); err != nil {
		return map[string]any{}
	}
	return m
}

// --------- Helpers ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------

func readEvidenceFile(path string, spec *ManifestSpec) ([]SessionEvent, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var events []SessionEvent
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}

		eventName, _ := raw["event"].(string)
		if eventName == "" {
			continue
		}

		dataField, _ := raw["data"].(map[string]any)
		timestamp, _ := raw["ts"].(string)

		ev := SessionEvent{
			Event:       eventName,
			Timestamp:   strPtr(timestamp),
			SpecIndex:   spec.Index,
			SpecName:    strPtr(spec.Name),
			SpecDirName: strPtr(spec.DirName),
			Data:        dataField,
		}
		events = append(events, ev)
	}
	return events, nil
}

type evidenceSummary struct {
	TotalCostUSD           *float64
	TotalInputTokens       *int
	TotalOutputTokens      *int
	TotalCacheReadTokens   *int
	TotalCacheCreateTokens *int
	RoundCount             *int
	CompletionReason       *string
	VerifierConceded       *bool
	CurrentRound           *int
	CurrentRole            *string
}

func readEvidenceSummary(path *string) (*evidenceSummary, error) {
	if path == nil || *path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(*path)
	if err != nil {
		return nil, err
	}

	var summary *evidenceSummary
	var activeRound *int
	var activeRole *string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		eventName, _ := raw["event"].(string)
		round := numberAsInt(raw["round"])
		role := stringPtrFromAny(raw["role"])
		dataField, _ := raw["data"].(map[string]any)

		switch eventName {
		case "round_start":
			if round != nil && role != nil && *role != "" && *role != "system" {
				activeRound = round
				activeRole = role
				if summary == nil {
					summary = &evidenceSummary{}
				}
			}
		case "agent_complete":
			if activeRound != nil && activeRole != nil && round != nil && role != nil &&
				*activeRound == *round && *activeRole == *role {
				activeRound = nil
				activeRole = nil
			}
		case "game_end":
			activeRound = nil
			activeRole = nil
			summary = &evidenceSummary{
				TotalCostUSD:           numberAsFloat(dataField["total_cost_usd"]),
				TotalInputTokens:       numberAsInt(dataField["total_input_tokens"]),
				TotalOutputTokens:      numberAsInt(dataField["total_output_tokens"]),
				TotalCacheReadTokens:   numberAsInt(dataField["total_cache_read_tokens"]),
				TotalCacheCreateTokens: numberAsInt(dataField["total_cache_creation_tokens"]),
				RoundCount:             evidenceRoundCount(raw, dataField),
				CompletionReason:       stringPtrFromAny(dataField["completion_reason"]),
				VerifierConceded:       boolPtrFromAny(dataField["verifier_conceded"]),
			}
		}
	}
	if summary != nil {
		summary.CurrentRound = activeRound
		summary.CurrentRole = activeRole
	}
	return summary, nil
}

func evidenceRoundCount(raw map[string]any, data map[string]any) *int {
	proverRounds := numberAsInt(data["prover_rounds"])
	verifierRounds := numberAsInt(data["verifier_rounds"])
	if proverRounds != nil || verifierRounds != nil {
		total := ptrOr(proverRounds, 0) + ptrOr(verifierRounds, 0)
		return &total
	}
	return numberAsInt(raw["round"])
}

func numberAsFloat(value any) *float64 {
	switch v := value.(type) {
	case float64:
		return &v
	case int:
		f := float64(v)
		return &f
	case json.Number:
		if f, err := v.Float64(); err == nil {
			return &f
		}
	}
	return nil
}

func numberAsInt(value any) *int {
	switch v := value.(type) {
	case float64:
		i := int(v)
		return &i
	case int:
		return &v
	case json.Number:
		if i, err := v.Int64(); err == nil {
			asInt := int(i)
			return &asInt
		}
	}
	return nil
}

func stringPtrFromAny(value any) *string {
	v, ok := value.(string)
	if !ok || v == "" {
		return nil
	}
	return &v
}

func boolPtrFromAny(value any) *bool {
	v, ok := value.(bool)
	if !ok {
		return nil
	}
	return &v
}

var sessionSeq atomic.Uint64

func generateSessionID(runtime SessionRuntime) string {
	now := time.Now().UTC()
	if runtime == RuntimeCloud {
		var suffix [8]byte
		if _, err := rand.Read(suffix[:]); err == nil {
			return fmt.Sprintf("sess_%s_%s", now.Format("20060102_150405"), hex.EncodeToString(suffix[:]))
		}
		seq := sessionSeq.Add(1) - 1
		return fmt.Sprintf("sess_%s_%08d", now.Format("20060102_150405"), seq)
	}
	seq := sessionSeq.Add(1) - 1
	return fmt.Sprintf("local_%s_%02d", now.Format("20060102_150405"), seq)
}

func tsNow() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

func frontmatterJSON(markdown string) string {
	raw, _, ok := spec.ParseFrontmatter(markdown)
	if !ok {
		return "{}"
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func cloneSpecVersions(versions []map[string]any) []map[string]any {
	if versions == nil {
		return []map[string]any{}
	}
	cloned := make([]map[string]any, 0, len(versions))
	for _, version := range versions {
		item := make(map[string]any, len(version))
		for k, v := range version {
			item[k] = v
		}
		cloned = append(cloned, item)
	}
	return cloned
}

func specVersionEntry(version int, specPath string, data []byte, previous *int, packageDigest *string) map[string]any {
	var previousVersion any
	if previous != nil {
		previousVersion = *previous
	}
	entry := map[string]any{
		"version":          version,
		"spec_path":        specPath,
		"spec_sha256":      specSHA256(data),
		"previous_version": previousVersion,
		"provenance":       map[string]any{"type": "inline"},
		"created_at":       tsNow(),
	}
	if packageDigest != nil && *packageDigest != "" {
		entry["package_digest"] = *packageDigest
	}
	return entry
}

func specSHA256(data []byte) string {
	hash := sha256.Sum256(data)
	return fmt.Sprintf("%x", hash)
}

type preparedRequestSpec struct {
	Name             string
	SourceSpecPath   *string
	SessionSpecPath  *string
	ContentHash      *string
	IntervalSeconds  *int
	SpecData         []byte
	PackageDigest    *string
	ApplyPackageLock *spec.ApplyPackageManifest
}

func prepareRequestSpec(sessionDir string, req SessionCreateRequest) (preparedRequestSpec, error) {
	if strings.TrimSpace(req.PackagePath) != "" {
		return prepareApplyPackageSpec(sessionDir, req.PackagePath, req.PackageDigest)
	}
	if req.SpecMarkdown == nil || strings.TrimSpace(*req.SpecMarkdown) == "" {
		return preparedRequestSpec{}, fmt.Errorf("spec_markdown is required")
	}

	tmpDir := filepath.Join(sessionDir, ".compile")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return preparedRequestSpec{}, fmt.Errorf("create compile dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	data := []byte(*req.SpecMarkdown)
	tmpSpecPath := filepath.Join(tmpDir, materializedSpecDir(data), "SPEC.md")
	if err := os.MkdirAll(filepath.Dir(tmpSpecPath), 0o755); err != nil {
		return preparedRequestSpec{}, fmt.Errorf("create compile spec dir: %w", err)
	}
	if err := os.WriteFile(tmpSpecPath, data, 0o644); err != nil {
		return preparedRequestSpec{}, fmt.Errorf("write compile spec: %w", err)
	}
	compiled, err := spec.CompileEnvironment(tmpSpecPath)
	if err != nil {
		return preparedRequestSpec{}, err
	}
	applyPackage, err := spec.BuildApplyPackage(compiled)
	if err != nil {
		return preparedRequestSpec{}, fmt.Errorf("build apply package: %w", err)
	}
	prepared := preparedRequestSpec{
		Name:             compiled.Environment.Name,
		ContentHash:      strPtr(compiled.ContentHash),
		IntervalSeconds:  compiled.Environment.IntervalSeconds,
		SpecData:         data,
		PackageDigest:    strPtr(applyPackage.Digest),
		ApplyPackageLock: &applyPackage.Manifest,
	}
	return prepared, nil
}

func prepareApplyPackageSpec(sessionDir string, packagePath string, expectedDigest string) (preparedRequestSpec, error) {
	data, err := os.ReadFile(packagePath)
	if err != nil {
		return preparedRequestSpec{}, fmt.Errorf("read apply package: %w", err)
	}
	packageDir := filepath.Join(sessionDir, "package")
	if err := os.RemoveAll(packageDir); err != nil {
		return preparedRequestSpec{}, fmt.Errorf("clear package dir: %w", err)
	}
	manifest, err := spec.ExtractApplyPackage(data, packageDir)
	if err != nil {
		return preparedRequestSpec{}, err
	}
	actualDigest := digestApplyPackage(data, manifest)
	expectedDigest = strings.TrimSpace(expectedDigest)
	if expectedDigest != "" && actualDigest != expectedDigest {
		return preparedRequestSpec{}, fmt.Errorf("package digest %q does not match expected %q", actualDigest, expectedDigest)
	}
	specPath := filepath.Join(packageDir, "SPEC.md")
	specData, err := os.ReadFile(specPath)
	if err != nil {
		return preparedRequestSpec{}, fmt.Errorf("read package root spec: %w", err)
	}
	compiled, err := spec.CompileEnvironmentWithBase(specPath, packageDir)
	if err != nil {
		return preparedRequestSpec{}, err
	}
	return preparedRequestSpec{
		Name:             compiled.Environment.Name,
		SourceSpecPath:   strPtr(specPath),
		ContentHash:      strPtr(compiled.ContentHash),
		IntervalSeconds:  compiled.Environment.IntervalSeconds,
		SpecData:         specData,
		PackageDigest:    strPtr(actualDigest),
		ApplyPackageLock: manifest,
	}, nil
}

func digestApplyPackage(data []byte, manifest *spec.ApplyPackageManifest) string {
	if manifest == nil {
		sum := sha256.Sum256(data)
		return fmt.Sprintf("sha256:%x", sum)
	}
	h := sha256.New()
	writeSpecDigestPart(h, fmt.Sprintf("schema:%d", manifest.SchemaVersion))
	writeSpecDigestPart(h, "spec:"+manifest.Spec.Digest)
	names := make([]string, 0, len(manifest.Skills))
	for name := range manifest.Skills {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		writeSpecDigestPart(h, name)
		writeSpecDigestPart(h, manifest.Skills[name])
	}
	return fmt.Sprintf("sha256:%x", h.Sum(nil))
}

func writeSpecDigestPart(w io.Writer, value string) {
	_, _ = io.WriteString(w, value)
	_, _ = w.Write([]byte{0})
}

func materializedSpecDir(data []byte) string {
	raw, _, ok := spec.ParseFrontmatter(string(data))
	if !ok {
		return "spec"
	}
	name, ok := raw["name"].(string)
	if !ok || !specDirNameRE.MatchString(name) {
		return "spec"
	}
	return name
}

func buildConfig(req SessionCreateRequest) SessionConfig {
	cfg := SessionConfig{
		Model:    req.Model,
		Thinking: req.Thinking,
	}
	if req.Until != nil {
		cfg.Until = *req.Until
	}
	if req.MaxCostUSD != nil {
		cfg.MaxCostUSD = req.MaxCostUSD
	}
	if req.AgentTimeoutSec != nil {
		cfg.AgentTimeoutSec = *req.AgentTimeoutSec
	}
	return cfg
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := file.Name()
	defer os.Remove(tmpPath)
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Chmod(perm); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func strValue(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func ptrOr[T any](p *T, def T) T {
	if p != nil {
		return *p
	}
	return def
}

func latestSpecSHA256(versions []map[string]any) string {
	for i := len(versions) - 1; i >= 0; i-- {
		if value := stringMapValue(versions[i], "spec_sha256"); value != "" {
			return value
		}
	}
	return ""
}

func stringMapValue(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	value, _ := m[key].(string)
	return value
}

func runtimeMode(runtime SessionRuntime) string {
	if runtime == RuntimeCloud {
		return "cloud"
	}
	return "local"
}

func manifestRuntime(m *Manifest, fallback SessionRuntime) SessionRuntime {
	if m.Runtime == RuntimeLocal || m.Runtime == RuntimeCloud {
		return m.Runtime
	}
	mode, _ := m.Provenance["mode"].(string)
	switch mode {
	case "cloud":
		return RuntimeCloud
	case "local":
		return RuntimeLocal
	}
	if fallback == RuntimeCloud {
		return RuntimeCloud
	}
	return RuntimeLocal
}

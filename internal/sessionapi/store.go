package sessionapi

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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

// Store is the persistence interface for sessions. Implementations are
// expected to be safe for concurrent use.
type Store interface {
	Create(req SessionCreateRequest) (*Session, error)
	Spec(id string) (*SessionSpecResponse, error)
	UpdateSpec(id string, req SessionSpecUpdateRequest) (*Session, error)
	List() ([]Session, error)
	Get(id string) (*Session, error)
	Stop(id string) (*Session, error)
	Transcript(id string) (string, error)
	Events(id string) ([]SessionEvent, error)
	WorkspacePath(id string, specName string) (string, error)
}

// --------- FileStore ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------

// FileStore is a local file-backed Store that writes session manifests under a
// root directory (typically .telos/sessions).
type FileStore struct {
	Root     string
	runtime  SessionRuntime
	launcher string
	mu       sync.Mutex
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

	if err := validateCreateRequest(req); err != nil {
		return nil, err
	}

	id := generateSessionID(fs.runtime)
	dir := fs.sessionDir(id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create session dir: %w", err)
	}

	prepared, err := prepareRequestSpec(dir, req)
	if err != nil {
		return nil, err
	}
	specName := prepared.Name
	sessionKind, err := fs.sessionKindForCreate(req)
	if err != nil {
		return nil, err
	}
	if sessionKind == KindController {
		ids, err := fs.liveControllerIDsBySpecName(specName)
		if err != nil {
			return nil, err
		}
		if len(ids) > 0 {
			return nil, fmt.Errorf("controller spec %q already exists as %s: %w", specName, strings.Join(ids, ", "), ErrConflict)
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
		if err := os.WriteFile(sessionSpecPath, prepared.SpecData, 0o644); err != nil {
			return nil, fmt.Errorf("write session spec: %w", err)
		}
	}
	prepared.SessionSpecPath = strPtr(sessionSpecPath)

	m := ManifestFromInitial(InitialManifest{
		SessionID:       id,
		SessionKind:     sessionKind,
		Runtime:         fs.runtime,
		CreatedAt:       tsNow(),
		Launcher:        fs.launcher,
		ParentSessionID: req.ParentSessionID,
		SessionSpecPath: prepared.SessionSpecPath,
		SpecName:        specName,
		Config:          buildConfig(req),
		Provenance:      map[string]any{"mode": runtimeMode(fs.runtime)},
		Access:          access,
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
	if sessionKind == KindController && sessionSpecPath != "" {
		version := 1
		m.CurrentSpecVersion = &version
		m.SpecVersions = append(
			m.SpecVersions,
			specVersionEntry(version, sessionSpecPath, prepared.SpecData, nil),
		)
	}

	if err := WriteManifest(fs.manifestPath(id), &m); err != nil {
		return nil, err
	}

	return fs.deriveSession(id, &m)
}

func validateCreateRequest(req SessionCreateRequest) error {
	if req.Until != nil && *req.Until <= 0 {
		return fmt.Errorf("until must be positive: %w", ErrInvalidSession)
	}
	return nil
}

func (fs *FileStore) sessionKindForCreate(req SessionCreateRequest) (SessionKind, error) {
	if req.SessionKind != nil {
		switch *req.SessionKind {
		case KindController:
			if req.ParentSessionID != nil {
				return "", fmt.Errorf("child sessions must use session_kind %q: %w", KindTask, ErrInvalidSession)
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

func (fs *FileStore) liveControllerIDsBySpecName(specName string) ([]string, error) {
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
		m, err := ReadManifest(fs.manifestPath(entry.Name()))
		if err != nil {
			continue
		}
		if m.SessionKind != KindController || m.SpecName != specName {
			continue
		}
		if deriveStatus(m).IsTerminal() {
			continue
		}
		ids = append(ids, m.SessionID)
	}
	sort.Strings(ids)
	return ids, nil
}

// Spec returns the mutable controller spec currently attached to a session.
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
	if m.SessionKind != KindController {
		return nil, fmt.Errorf("task sessions do not have mutable specs: %w", ErrInvalidSession)
	}
	if m.SessionSpecPath == nil || *m.SessionSpecPath == "" {
		return nil, fmt.Errorf("controller session has no mutable spec: %w", ErrInvalidSession)
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

// UpdateSpec replaces a controller session's primary spec in place.
func (fs *FileStore) UpdateSpec(id string, req SessionSpecUpdateRequest) (*Session, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	m, err := ReadManifest(fs.manifestPath(id))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("session %s: %w", id, ErrNotFound)
		}
		return nil, err
	}
	if m.SessionKind != KindController {
		return nil, fmt.Errorf("task sessions do not have mutable specs: %w", ErrInvalidSession)
	}
	if m.SessionSpecPath == nil || *m.SessionSpecPath == "" {
		return nil, fmt.Errorf("controller session has no mutable spec: %w", ErrInvalidSession)
	}

	markdown := strings.TrimRight(req.SpecMarkdown, "\n") + "\n"
	prepared, err := prepareRequestSpec(fs.sessionDir(id), SessionCreateRequest{SpecMarkdown: &markdown})
	if err != nil {
		return nil, err
	}
	if prepared.Name != m.SpecName {
		return nil, fmt.Errorf("controller spec name is immutable: %w", ErrInvalidSession)
	}
	if err := os.WriteFile(*m.SessionSpecPath, prepared.SpecData, 0o644); err != nil {
		return nil, fmt.Errorf("write session spec: %w", err)
	}
	updateManifestConfig(&m.Config, req)
	previousVersion := ptrOr(m.CurrentSpecVersion, 1)
	version := previousVersion + 1
	m.CurrentSpecVersion = &version
	m.SpecVersions = append(
		m.SpecVersions,
		specVersionEntry(version, *m.SessionSpecPath, prepared.SpecData, &previousVersion),
	)
	m.SessionSpecPath = strPtr(*m.SessionSpecPath)
	if len(m.Specs) > 0 {
		m.Specs[0].Name = prepared.Name
		m.Specs[0].DirName = prepared.Name
		m.Specs[0].SessionSpecPath = m.SessionSpecPath
		m.Specs[0].ContentHash = prepared.ContentHash
		m.Specs[0].IntervalSeconds = prepared.IntervalSeconds
	}
	if err := WriteManifest(fs.manifestPath(id), m); err != nil {
		return nil, err
	}
	return fs.deriveSession(id, m)
}

func updateManifestConfig(cfg *SessionConfig, req SessionSpecUpdateRequest) {
	if req.Model != "" {
		cfg.Model = req.Model
	}
	if req.Thinking != "" {
		cfg.Thinking = req.Thinking
	}
	if req.MaxCostUSD != nil {
		cfg.MaxCostUSD = req.MaxCostUSD
	}
	if req.AgentTimeoutSec != nil {
		cfg.AgentTimeoutSec = *req.AgentTimeoutSec
	}
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

	if open := m.OpenEpoch(); open != nil {
		terminateRunner(open.Runner)
	}

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

	if err := WriteManifest(fs.manifestPath(id), m); err != nil {
		return nil, err
	}

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

// WorkspacePath returns the absolute path to the workspace archive for a spec.
func (fs *FileStore) WorkspacePath(id string, specName string) (string, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	m, err := ReadManifest(fs.manifestPath(id))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("session %s: %w", id, ErrNotFound)
		}
		return "", err
	}

	for _, spec := range m.Specs {
		if spec.Name == specName || spec.DirName == specName {
			if spec.WorkspacePath == nil || *spec.WorkspacePath == "" {
				return "", fmt.Errorf("workspace for spec %s: %w", specName, ErrNotFound)
			}
			if _, err := os.Stat(*spec.WorkspacePath); err != nil {
				return "", fmt.Errorf("workspace for spec %s: %w", specName, ErrNotFound)
			}
			return *spec.WorkspacePath, nil
		}
	}
	return "", fmt.Errorf("spec %s: %w", specName, ErrNotFound)
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

	status := deriveStatus(m)
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

func deriveStatus(m *Manifest) SessionStatus {
	open := m.OpenEpoch()
	last := m.LastEpoch()

	if open != nil {
		if open.Runner != nil && open.Runner.InCluster {
			return StatusRunning
		}
		pid, ok := open.Runner.ProcessID()
		if ok && processAlive(pid) {
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

func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
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

		ev := SessionEvent{
			Event:       eventName,
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

func specVersionEntry(version int, specPath string, data []byte, previous *int) map[string]any {
	hash := sha256.Sum256(data)
	var previousVersion any
	if previous != nil {
		previousVersion = *previous
	}
	return map[string]any{
		"version":          version,
		"spec_path":        specPath,
		"spec_sha256":      fmt.Sprintf("%x", hash),
		"previous_version": previousVersion,
		"provenance":       map[string]any{"type": "inline"},
		"created_at":       tsNow(),
	}
}

type preparedRequestSpec struct {
	Name            string
	SessionSpecPath *string
	ContentHash     *string
	IntervalSeconds *int
	SpecData        []byte
}

func prepareRequestSpec(sessionDir string, req SessionCreateRequest) (preparedRequestSpec, error) {
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
	prepared := preparedRequestSpec{
		Name:            compiled.Environment.Name,
		ContentHash:     strPtr(compiled.ContentHash),
		IntervalSeconds: compiled.Environment.IntervalSeconds,
		SpecData:        data,
	}
	return prepared, nil
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

func ptrOr[T any](p *T, def T) T {
	if p != nil {
		return *p
	}
	return def
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

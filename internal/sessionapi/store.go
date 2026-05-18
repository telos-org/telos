package sessionapi

import (
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

	"github.com/telos-org/telos-go/internal/spec"
)

// ErrNotFound is returned when a session or artifact does not exist.
var ErrNotFound = errors.New("not found")

var specDirNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// Store is the persistence interface for sessions. Implementations are
// expected to be safe for concurrent use.
type Store interface {
	Create(req SessionCreateRequest) (*Session, error)
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

	id := generateSessionID()
	dir := fs.sessionDir(id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create session dir: %w", err)
	}

	prepared, err := prepareRequestSpec(dir, req)
	if err != nil {
		return nil, err
	}
	specName := prepared.Name
	access, err := NewScopedToken(id, KindTask)
	if err != nil {
		return nil, err
	}
	specDir := filepath.Join(dir, "specs", specName)
	if err := os.MkdirAll(specDir, 0o755); err != nil {
		return nil, fmt.Errorf("create spec dir: %w", err)
	}

	evidencePath := filepath.Join(specDir, "evidence.jsonl")
	transcriptPath := filepath.Join(specDir, fmt.Sprintf("pvg-transcript-%s.md", id))
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
		SessionKind:     KindTask,
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

	if err := WriteManifest(fs.manifestPath(id), &m); err != nil {
		return nil, err
	}

	return fs.deriveSession(id, &m)
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

// Transcript returns the PVG transcript markdown for the first spec.
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

	kind := m.SessionKind
	s := Session{
		SessionID:       m.SessionID,
		SessionKind:     &kind,
		ParentSessionID: m.ParentSessionID,
		SpecName:        strPtr(m.SpecName),
		Status:          status,
		CreatedAt:       strPtr(m.CreatedAt),
		Runtime:         manifestRuntime(m, fs.runtime),
		Launcher:        strPtr(m.Launcher),
		SessionSpecPath: m.SessionSpecPath,
		SessionDir:      strPtr(dir),
		Config:          m.Config.AsMap(),
		Provenance:      m.Provenance,
		Specs:           specs,
		Epochs:          epochs,
		SpecVersions:    []map[string]any{},
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

	return ss
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

var sessionSeq atomic.Uint64

func generateSessionID() string {
	now := time.Now().UTC()
	seq := sessionSeq.Add(1) - 1
	return fmt.Sprintf("local_%s_%02d", now.Format("20060102_150405"), seq)
}

func tsNow() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
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
	if req.MaxRounds != nil {
		cfg.MaxRounds = *req.MaxRounds
	}
	if req.MaxCostUSD != nil {
		cfg.MaxCostUSD = req.MaxCostUSD
	}
	if req.AgentTimeoutSec != nil {
		cfg.AgentTimeoutSec = *req.AgentTimeoutSec
	}
	if req.Workspace != nil {
		cfg.Workspace = *req.Workspace
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

package sessionapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// --------- Manifest (on-disk shape) ------------------------------------------------------------------------------------------------------------------------------------------------

// manifest mirrors the session.json written by the Python runtime.
type manifest struct {
	SessionID       string         `json:"session_id"`
	SessionKind     SessionKind    `json:"session_kind"`
	CreatedAt       string         `json:"created_at"`
	Launcher        string         `json:"launcher"`
	ParentSessionID *string        `json:"parent_session_id"`
	SourceSpecPath  *string        `json:"source_spec_path,omitempty"`
	SessionSpecPath *string        `json:"session_spec_path,omitempty"`
	SpecName        string         `json:"spec_name"`
	Config          map[string]any `json:"config"`
	Provenance      map[string]any `json:"provenance"`
	Specs           []manifestSpec `json:"specs"`
	Epochs          []epoch        `json:"epochs"`
}

type manifestSpec struct {
	Index           *int    `json:"index,omitempty"`
	Name            string  `json:"name"`
	DirName         string  `json:"dir_name"`
	EnvironmentPath *string `json:"environment_path,omitempty"`
	SessionSpecPath *string `json:"session_spec_path,omitempty"`
	ContentHash     *string `json:"content_hash,omitempty"`
	EvidencePath    *string `json:"evidence_path,omitempty"`
	TranscriptPath  *string `json:"transcript_path,omitempty"`
	WorkspacePath   *string `json:"workspace_path,omitempty"`
	IntervalSeconds *int    `json:"interval_seconds"`
}

type epoch struct {
	ID         int            `json:"id"`
	StartedAt  string         `json:"started_at"`
	FinishedAt *string        `json:"finished_at"`
	Result     *string        `json:"result"`
	Error      *string        `json:"error"`
	Runner     map[string]any `json:"runner"`
}

// --------- FileStore ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------

// FileStore is a local file-backed Store that writes session manifests under a
// root directory (typically .telos/sessions).
type FileStore struct {
	Root string
	mu   sync.Mutex
}

// NewFileStore returns a FileStore rooted at the given directory.
func NewFileStore(root string) *FileStore {
	return &FileStore{Root: root}
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
		if prepared.EnvironmentPath == nil {
			prepared.EnvironmentPath = strPtr(sessionSpecPath)
		}
	}
	prepared.SessionSpecPath = strPtr(sessionSpecPath)

	idx := 0
	m := manifest{
		SessionID:       id,
		SessionKind:     KindTask,
		CreatedAt:       tsNow(),
		Launcher:        "local",
		ParentSessionID: req.ParentSessionID,
		SourceSpecPath:  prepared.SourceSpecPath,
		SessionSpecPath: prepared.SessionSpecPath,
		SpecName:        specName,
		Config:          buildConfig(req),
		Provenance:      map[string]any{"mode": "local"},
		Specs: []manifestSpec{{
			Index:           &idx,
			Name:            specName,
			DirName:         specName,
			EnvironmentPath: prepared.EnvironmentPath,
			SessionSpecPath: prepared.SessionSpecPath,
			ContentHash:     prepared.ContentHash,
			EvidencePath:    &evidencePath,
			TranscriptPath:  &transcriptPath,
			WorkspacePath:   &workspacePath,
			IntervalSeconds: prepared.IntervalSeconds,
		}},
		Epochs: []epoch{},
	}

	if err := writeManifest(fs.manifestPath(id), &m); err != nil {
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
		m, err := readManifest(fs.manifestPath(entry.Name()))
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

	m, err := readManifest(fs.manifestPath(id))
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

	m, err := readManifest(fs.manifestPath(id))
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

	if open := openEpoch(m); open != nil {
		terminateRunner(open.Runner)
	}

	now := tsNow()
	stopped := "stopped"
	stoppedErr := "stopped by operator"

	if len(m.Epochs) == 0 {
		m.Epochs = append(m.Epochs, epoch{
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

	if err := writeManifest(fs.manifestPath(id), m); err != nil {
		return nil, err
	}

	return fs.deriveSession(id, m)
}

// Transcript returns the PVG transcript markdown for the first spec.
func (fs *FileStore) Transcript(id string) (string, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	m, err := readManifest(fs.manifestPath(id))
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

	m, err := readManifest(fs.manifestPath(id))
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

	m, err := readManifest(fs.manifestPath(id))
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

func (fs *FileStore) deriveSession(id string, m *manifest) (*Session, error) {
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
		Runtime:         RuntimeLocal,
		Launcher:        strPtr(m.Launcher),
		SourceSpecPath:  m.SourceSpecPath,
		SessionSpecPath: m.SessionSpecPath,
		SessionDir:      strPtr(dir),
		Config:          m.Config,
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
	if last := lastEpoch(m); last != nil {
		s.Result = last.Result
		s.Error = last.Error
	}

	return &s, nil
}

func deriveSpec(ms manifestSpec) SessionSpec {
	ss := SessionSpec{
		Index:           ms.Index,
		Name:            strPtr(ms.Name),
		DirName:         strPtr(ms.DirName),
		EnvironmentPath: ms.EnvironmentPath,
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

func deriveStatus(m *manifest) SessionStatus {
	open := openEpoch(m)
	last := lastEpoch(m)

	if open != nil {
		pid, ok := runnerPID(open.Runner)
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

func openEpoch(m *manifest) *epoch {
	for i := len(m.Epochs) - 1; i >= 0; i-- {
		if m.Epochs[i].FinishedAt == nil {
			return &m.Epochs[i]
		}
	}
	return nil
}

func lastEpoch(m *manifest) *epoch {
	if len(m.Epochs) == 0 {
		return nil
	}
	return &m.Epochs[len(m.Epochs)-1]
}

func runnerPID(runner map[string]any) (int, bool) {
	raw, ok := runner["pid"]
	if !ok {
		return 0, false
	}
	switch value := raw.(type) {
	case int:
		if value > 0 {
			return value, true
		}
	case int64:
		if value > 0 {
			return int(value), true
		}
	case float64:
		if value > 0 {
			return int(value), true
		}
	case json.Number:
		parsed, err := value.Int64()
		if err == nil && parsed > 0 {
			return int(parsed), true
		}
	}
	return 0, false
}

func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func terminateRunner(runner map[string]any) {
	pid, ok := runnerPID(runner)
	if !ok {
		return
	}
	if pid == os.Getpid() {
		return
	}
	if err := syscall.Kill(-pid, syscall.SIGTERM); err == nil || errors.Is(err, syscall.ESRCH) {
		return
	}
	_ = syscall.Kill(pid, syscall.SIGTERM)
}

func epochToMap(e epoch) map[string]any {
	m := map[string]any{
		"id":          e.ID,
		"started_at":  e.StartedAt,
		"finished_at": e.FinishedAt,
		"result":      e.Result,
		"error":       e.Error,
		"runner":      e.Runner,
	}
	return m
}

// --------- Helpers ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------

func readManifest(path string) (*manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest %s: %w", path, err)
	}
	return &m, nil
}

func writeManifest(path string, m *manifest) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readEvidenceFile(path string, spec *manifestSpec) ([]SessionEvent, error) {
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
	SourceSpecPath  *string
	EnvironmentPath *string
	SessionSpecPath *string
	ContentHash     *string
	IntervalSeconds *int
	SpecData        []byte
}

func prepareRequestSpec(sessionDir string, req SessionCreateRequest) (preparedRequestSpec, error) {
	prepared := preparedRequestSpec{Name: deriveSpecName(req), SourceSpecPath: req.SpecPath}

	if req.SpecPath != nil && *req.SpecPath != "" {
		abs, err := filepath.Abs(*req.SpecPath)
		if err != nil {
			return preparedRequestSpec{}, fmt.Errorf("resolve spec path: %w", err)
		}
		data, err := os.ReadFile(abs)
		if err == nil {
			compiled, err := spec.CompileEnvironment(abs)
			if err != nil {
				return preparedRequestSpec{}, err
			}
			prepared.Name = compiled.Environment.Name
			prepared.EnvironmentPath = req.SpecPath
			prepared.ContentHash = strPtr(compiled.ContentHash)
			prepared.IntervalSeconds = compiled.Environment.IntervalSeconds
			prepared.SpecData = data
			return prepared, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return preparedRequestSpec{}, fmt.Errorf("read spec: %w", err)
		}
	}

	if req.SpecMarkdown == nil || strings.TrimSpace(*req.SpecMarkdown) == "" {
		return prepared, nil
	}

	tmpDir := filepath.Join(sessionDir, ".compile")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return preparedRequestSpec{}, fmt.Errorf("create compile dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	tmpSpecPath := filepath.Join(tmpDir, "SPEC.md")
	data := []byte(*req.SpecMarkdown)
	if err := os.WriteFile(tmpSpecPath, data, 0o644); err != nil {
		return preparedRequestSpec{}, fmt.Errorf("write compile spec: %w", err)
	}
	compiled, err := spec.CompileEnvironment(tmpSpecPath)
	if err != nil {
		return preparedRequestSpec{}, err
	}
	prepared.Name = compiled.Environment.Name
	prepared.SourceSpecPath = nil
	prepared.ContentHash = strPtr(compiled.ContentHash)
	prepared.IntervalSeconds = compiled.Environment.IntervalSeconds
	prepared.SpecData = data
	return prepared, nil
}

func deriveSpecName(req SessionCreateRequest) string {
	if req.SpecPath != nil && *req.SpecPath != "" {
		base := filepath.Base(filepath.Dir(*req.SpecPath))
		if base != "." && base != "/" {
			return base
		}
	}
	if req.SpecID != nil && *req.SpecID != "" {
		return *req.SpecID
	}
	return "default"
}

func buildConfig(req SessionCreateRequest) map[string]any {
	cfg := map[string]any{}
	if req.Model != "" {
		cfg["model"] = req.Model
	}
	if req.Thinking != "" {
		cfg["thinking"] = req.Thinking
	}
	if req.MaxRounds != nil {
		cfg["max_rounds"] = *req.MaxRounds
	}
	if req.MaxCostUSD != nil {
		cfg["max_cost_usd"] = *req.MaxCostUSD
	}
	if req.AgentTimeoutSec != nil {
		cfg["agent_timeout_sec"] = *req.AgentTimeoutSec
	}
	if req.Workspace != nil {
		cfg["workspace"] = *req.Workspace
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

package sessionapi

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/telos-org/telos/internal/spec"
)

// Manifest is the persisted session.json shape shared by the store and worker.
type Manifest struct {
	SessionID          string                 `json:"session_id"`
	SessionKind        SessionKind            `json:"session_kind"`
	Runtime            SessionRuntime         `json:"runtime,omitempty"`
	CreatedAt          string                 `json:"created_at"`
	Launcher           string                 `json:"launcher"`
	ParentSessionID    *string                `json:"parent_session_id"`
	SourceSpecPath     *string                `json:"source_spec_path,omitempty"`
	SessionSpecPath    *string                `json:"session_spec_path,omitempty"`
	SpecName           string                 `json:"spec_name"`
	CurrentSpecVersion *int                   `json:"current_spec_version,omitempty"`
	SpecVersions       []map[string]any       `json:"spec_versions,omitempty"`
	ApplyPackageDigest *string                `json:"apply_package_digest,omitempty"`
	ApplyPackageLock   *spec.ApplyPackageLock `json:"apply_package_lock,omitempty"`
	Config             SessionConfig          `json:"config"`
	Workspace          *Workspace             `json:"workspace,omitempty"`
	Provenance         map[string]any         `json:"provenance"`
	Access             *ScopedToken           `json:"access,omitempty"`
	Specs              []ManifestSpec         `json:"specs"`
	Epochs             []Epoch                `json:"epochs"`
}

type Workspace struct {
	Mode       string                    `json:"mode"`
	Source     string                    `json:"source,omitempty"`
	BaseCommit string                    `json:"base_commit,omitempty"`
	Extends    *WorkspaceArtifactBinding `json:"extends,omitempty"`
}

type WorkspaceArtifactBinding struct {
	SpecPath      string `json:"spec_path,omitempty"`
	SpecName      string `json:"spec_name,omitempty"`
	ContentHash   string `json:"content_hash,omitempty"`
	SessionID     string `json:"session_id,omitempty"`
	WorkspacePath string `json:"workspace_path,omitempty"`
}

type ManifestSpec struct {
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

type Epoch struct {
	ID         int     `json:"id"`
	StartedAt  string  `json:"started_at"`
	FinishedAt *string `json:"finished_at"`
	Result     *string `json:"result"`
	Error      *string `json:"error"`
	Runner     *Runner `json:"runner"`
}

type Runner struct {
	Kind         string  `json:"kind,omitempty"`
	PID          int     `json:"pid,omitempty"`
	PGID         int     `json:"pgid,omitempty"`
	LogPath      string  `json:"log_path,omitempty"`
	InCluster    bool    `json:"in_cluster"`
	Hostname     string  `json:"hostname,omitempty"`
	PodName      string  `json:"pod_name,omitempty"`
	PodNamespace string  `json:"pod_namespace,omitempty"`
	StartedAt    string  `json:"started_at,omitempty"`
	FinishedAt   *string `json:"finished_at,omitempty"`
	Status       string  `json:"status,omitempty"`
}

type ScopedToken struct {
	APIToken         string   `json:"api_token"`
	SubjectSessionID string   `json:"subject_session_id"`
	Scopes           []string `json:"scopes"`
}

type SessionConfig struct {
	Model           string         `json:"model,omitempty"`
	Until           int            `json:"until,omitempty"`
	MaxCostUSD      *float64       `json:"max_cost_usd,omitempty"`
	AgentTimeoutSec int            `json:"agent_timeout_sec,omitempty"`
	Thinking        string         `json:"thinking,omitempty"`
	Extra           map[string]any `json:"-"`
}

// InitialManifest is the typed input for creating a session.json before any
// worker epoch has started.
type InitialManifest struct {
	SessionID          string
	SessionKind        SessionKind
	Runtime            SessionRuntime
	Launcher           string
	CreatedAt          string
	ParentSessionID    *string
	SourceSpecPath     *string
	SessionSpecPath    *string
	SpecName           string
	Config             SessionConfig
	Workspace          *Workspace
	Provenance         map[string]any
	ApplyPackageDigest *string
	ApplyPackageLock   *spec.ApplyPackageLock
	Access             *ScopedToken
	Specs              []InitialManifestSpec
}

type InitialManifestSpec struct {
	Index           int
	Name            string
	DirName         string
	SessionSpecPath *string
	ContentHash     *string
	EvidencePath    *string
	TranscriptPath  *string
	WorkspacePath   *string
	IntervalSeconds *int
}

func WriteInitialManifest(path string, input InitialManifest) error {
	m := ManifestFromInitial(input)
	return WriteManifest(path, &m)
}

func ManifestFromInitial(input InitialManifest) Manifest {
	if input.SessionKind == "" {
		input.SessionKind = KindTask
	}
	if input.Runtime == "" {
		input.Runtime = RuntimeLocal
	}
	if input.Launcher == "" {
		input.Launcher = "local"
	}
	if input.Provenance == nil {
		input.Provenance = map[string]any{"mode": runtimeMode(input.Runtime)}
	}
	specs := make([]ManifestSpec, 0, len(input.Specs))
	for _, spec := range input.Specs {
		index := spec.Index
		specs = append(specs, ManifestSpec{
			Index:           &index,
			Name:            spec.Name,
			DirName:         spec.DirName,
			SessionSpecPath: spec.SessionSpecPath,
			ContentHash:     spec.ContentHash,
			EvidencePath:    spec.EvidencePath,
			TranscriptPath:  spec.TranscriptPath,
			WorkspacePath:   spec.WorkspacePath,
			IntervalSeconds: spec.IntervalSeconds,
		})
	}
	return Manifest{
		SessionID:          input.SessionID,
		SessionKind:        input.SessionKind,
		Runtime:            input.Runtime,
		CreatedAt:          input.CreatedAt,
		Launcher:           input.Launcher,
		ParentSessionID:    input.ParentSessionID,
		SourceSpecPath:     input.SourceSpecPath,
		SessionSpecPath:    input.SessionSpecPath,
		SpecName:           input.SpecName,
		Config:             input.Config,
		Workspace:          input.Workspace,
		Provenance:         input.Provenance,
		ApplyPackageDigest: input.ApplyPackageDigest,
		ApplyPackageLock:   input.ApplyPackageLock,
		Access:             input.Access,
		Specs:              specs,
		Epochs:             []Epoch{},
	}
}

func ReadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest %s: %w", path, err)
	}
	return &m, nil
}

func WriteManifest(path string, m *Manifest) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (m *Manifest) LastEpoch() *Epoch {
	if len(m.Epochs) == 0 {
		return nil
	}
	return &m.Epochs[len(m.Epochs)-1]
}

func (m *Manifest) OpenEpoch() *Epoch {
	for i := len(m.Epochs) - 1; i >= 0; i-- {
		if m.Epochs[i].FinishedAt == nil {
			return &m.Epochs[i]
		}
	}
	return nil
}

func (m *Manifest) IsStopped() bool {
	last := m.LastEpoch()
	return last != nil && last.Result != nil && *last.Result == "stopped"
}

func (c SessionConfig) AsMap() map[string]any {
	data, err := json.Marshal(c)
	if err != nil {
		return map[string]any{}
	}
	m := map[string]any{}
	if err := json.Unmarshal(data, &m); err != nil {
		return map[string]any{}
	}
	return m
}

func (c SessionConfig) MarshalJSON() ([]byte, error) {
	m := map[string]any{}
	for key, value := range c.Extra {
		m[key] = value
	}
	if c.Model != "" {
		m["model"] = c.Model
	}
	if c.Until > 0 {
		m["until"] = c.Until
	}
	if c.MaxCostUSD != nil {
		m["max_cost_usd"] = *c.MaxCostUSD
	}
	if c.AgentTimeoutSec > 0 {
		m["agent_timeout_sec"] = c.AgentTimeoutSec
	}
	if c.Thinking != "" {
		m["thinking"] = c.Thinking
	}
	return json.Marshal(m)
}

func (c *SessionConfig) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	c.Extra = map[string]any{}
	if value, ok := raw["model"]; ok {
		if err := json.Unmarshal(value, &c.Model); err != nil {
			return fmt.Errorf("config.model: %w", err)
		}
		delete(raw, "model")
	}
	if value, ok := raw["until"]; ok {
		if err := json.Unmarshal(value, &c.Until); err != nil {
			return fmt.Errorf("config.until: %w", err)
		}
		delete(raw, "until")
	}
	if value, ok := raw["max_cost_usd"]; ok {
		if err := json.Unmarshal(value, &c.MaxCostUSD); err != nil {
			return fmt.Errorf("config.max_cost_usd: %w", err)
		}
		delete(raw, "max_cost_usd")
	}
	if value, ok := raw["agent_timeout_sec"]; ok {
		if err := json.Unmarshal(value, &c.AgentTimeoutSec); err != nil {
			return fmt.Errorf("config.agent_timeout_sec: %w", err)
		}
		delete(raw, "agent_timeout_sec")
	}
	if value, ok := raw["thinking"]; ok {
		if err := json.Unmarshal(value, &c.Thinking); err != nil {
			return fmt.Errorf("config.thinking: %w", err)
		}
		delete(raw, "thinking")
	}
	if _, ok := raw["workspace"]; ok {
		delete(raw, "workspace")
	}
	for key, value := range raw {
		var decoded any
		if err := json.Unmarshal(value, &decoded); err != nil {
			return err
		}
		c.Extra[key] = decoded
	}
	if len(c.Extra) == 0 {
		c.Extra = nil
	}
	return nil
}

func (r *Runner) ProcessID() (int, bool) {
	if r == nil || r.PID <= 0 {
		return 0, false
	}
	return r.PID, true
}

func (r *Runner) ProcessGroupID() (int, bool) {
	if r == nil || r.PGID <= 0 {
		return 0, false
	}
	return r.PGID, true
}

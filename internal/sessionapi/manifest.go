package sessionapi

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Manifest is the typed on-disk representation of session.json. The local
// runner and the FileStore both read and write this single shape, so the
// two cannot drift from each other.
type Manifest struct {
	SessionID       string         `json:"session_id"`
	SessionKind     SessionKind    `json:"session_kind"`
	CreatedAt       string         `json:"created_at"`
	Launcher        string         `json:"launcher"`
	ParentSessionID *string        `json:"parent_session_id"`
	SourceSpecPath  *string        `json:"source_spec_path,omitempty"`
	SessionSpecPath *string        `json:"session_spec_path,omitempty"`
	SpecName        string         `json:"spec_name"`
	Config          SessionConfig  `json:"config"`
	Provenance      map[string]any `json:"provenance"`
	Specs           []ManifestSpec `json:"specs"`
	Epochs          []Epoch        `json:"epochs"`
}

// ManifestSpec is one compiled spec entry inside a session manifest.
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

// Epoch is one run attempt of a session.
type Epoch struct {
	ID         int     `json:"id"`
	StartedAt  string  `json:"started_at"`
	FinishedAt *string `json:"finished_at"`
	Result     *string `json:"result"`
	Error      *string `json:"error"`
	Runner     *Runner `json:"runner"`
}

// Runner records the process that executed an epoch.
type Runner struct {
	Kind       string  `json:"kind,omitempty"`
	PID        int     `json:"pid,omitempty"`
	PGID       int     `json:"pgid,omitempty"`
	LogPath    string  `json:"log_path,omitempty"`
	FinishedAt *string `json:"finished_at,omitempty"`
	Status     string  `json:"status,omitempty"`
}

// SessionConfig is the persisted run configuration for a session.
type SessionConfig struct {
	Model           string   `json:"model,omitempty"`
	MaxRounds       int      `json:"max_rounds,omitempty"`
	MaxCostUSD      *float64 `json:"max_cost_usd,omitempty"`
	AgentTimeoutSec int      `json:"agent_timeout_sec,omitempty"`
	Thinking        string   `json:"thinking,omitempty"`
	Workspace       string   `json:"workspace,omitempty"`
}

// AsMap renders the config as the loosely typed map carried by the
// Sessions API Session.Config field.
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

// LastEpoch returns a pointer to the most recent epoch, or nil if there are
// none. The pointer aliases the slice element, so callers may mutate it.
func (m *Manifest) LastEpoch() *Epoch {
	if len(m.Epochs) == 0 {
		return nil
	}
	return &m.Epochs[len(m.Epochs)-1]
}

// OpenEpoch returns the most recent epoch that has not finished, or nil.
func (m *Manifest) OpenEpoch() *Epoch {
	for i := len(m.Epochs) - 1; i >= 0; i-- {
		if m.Epochs[i].FinishedAt == nil {
			return &m.Epochs[i]
		}
	}
	return nil
}

// IsStopped reports whether the last epoch was stopped by an operator.
func (m *Manifest) IsStopped() bool {
	last := m.LastEpoch()
	return last != nil && last.Result != nil && *last.Result == "stopped"
}

// ReadManifest loads and decodes a session manifest from disk.
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

// WriteManifest atomically writes a session manifest to disk.
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
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

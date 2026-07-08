package game

import (
	"fmt"
	"os"
	"path/filepath"
)

// PVGState holds the canonical filesystem layout for one PVG run.
type PVGState struct {
	SystemName     string
	SessionID      string
	SpecDir        string
	EvidencePath   string
	TranscriptPath string
	WorkspacePath  string
}

// NewPVGState creates a PVGState from a spec directory.
func NewPVGState(systemName, specDir, sessionID string) *PVGState {
	abs, _ := filepath.Abs(specDir)
	evidencePath := filepath.Join(abs, "evidence.jsonl")
	if sessionID == "" {
		sessionID = "unknown"
	}
	transcriptPath := filepath.Join(abs, fmt.Sprintf("transcript-%s.md", sessionID))
	workspacePath := filepath.Join(abs, "workspace.tar.gz")
	return &PVGState{
		SystemName:     systemName,
		SessionID:      sessionID,
		SpecDir:        abs,
		EvidencePath:   evidencePath,
		TranscriptPath: transcriptPath,
		WorkspacePath:  workspacePath,
	}
}

// Ensure creates the required directories.
func (s *PVGState) Ensure() error {
	if err := os.MkdirAll(s.SpecDir, 0o755); err != nil {
		return err
	}
	return os.MkdirAll(s.TurnsDir(), 0o755)
}

// TurnsDir returns the path to the turns directory.
func (s *PVGState) TurnsDir() string {
	return filepath.Join(s.SpecDir, "turns")
}

// SpecPath returns the path to the spec.md file.
func (s *PVGState) SpecPath() string {
	return filepath.Join(s.SpecDir, "spec.md")
}

// Turn returns a TurnState for a given epoch, round, and role.
func (s *PVGState) Turn(epochID int, roundNum int, role string) *TurnState {
	dir := filepath.Join(s.TurnsDir(), fmt.Sprintf("epoch-%04d", epochID), fmt.Sprintf("%04d-%s", roundNum, role))
	return &TurnState{
		RoundNum: roundNum,
		Role:     role,
		Dir:      dir,
	}
}

// WriteTurnTask writes the task content to the turn's task.md file.
func WriteTurnTask(ts *TurnState, task string) error {
	if err := os.MkdirAll(ts.Dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(ts.TaskPath(), []byte(task), 0o644); err != nil {
		return err
	}
	return nil
}

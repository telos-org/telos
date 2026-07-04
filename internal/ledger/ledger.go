// Package ledger owns the objective-ledger schema and persistence helpers.
package ledger

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/telos-org/telos/internal/protocol"
)

const Schema = "telos.objective_ledger.v1"

type ObjectiveState string

const (
	ObjectiveStatePlan      ObjectiveState = "plan"
	ObjectiveStateImplement ObjectiveState = "implement"
	ObjectiveStateVerify    ObjectiveState = "verify"
	ObjectiveStateRepair    ObjectiveState = "repair"
	ObjectiveStateFinalize  ObjectiveState = "finalize"
	ObjectiveStateBlocked   ObjectiveState = "blocked"
)

type ObjectiveLedger struct {
	Schema             string          `json:"schema"`
	SessionID          string          `json:"session_id"`
	SystemName         string          `json:"system_name"`
	Objective          string          `json:"objective,omitempty"`
	State              ObjectiveState  `json:"state"`
	LastTransition     string          `json:"last_transition,omitempty"`
	UpdatedAt          string          `json:"updated_at"`
	LastImplementation string          `json:"last_implementation,omitempty"`
	LastEvaluation     string          `json:"last_evaluation,omitempty"`
	OpenFindings       []string        `json:"open_findings,omitempty"`
	ResolvedFindings   []string        `json:"resolved_findings,omitempty"`
	Turns              []ObjectiveTurn `json:"turns,omitempty"`
}

type ObjectiveTurn struct {
	RoundNum       int                  `json:"round_num"`
	Role           string               `json:"role"`
	Status         protocol.AgentStatus `json:"status"`
	StateAfter     ObjectiveState       `json:"state_after"`
	Error          string               `json:"error,omitempty"`
	ProgressUpdate string               `json:"progress_update,omitempty"`
}

func New(sessionID, systemName, objective string) ObjectiveLedger {
	return ObjectiveLedger{
		Schema:     Schema,
		SessionID:  sessionID,
		SystemName: systemName,
		Objective:  firstParagraph(objective),
		State:      ObjectiveStatePlan,
		UpdatedAt:  time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func Read(path string) (ObjectiveLedger, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ObjectiveLedger{}, err
	}
	var ledger ObjectiveLedger
	if err := json.Unmarshal(data, &ledger); err != nil {
		return ObjectiveLedger{}, err
	}
	return ledger, nil
}

// Initialize creates the objective ledger when it is missing or empty,
// preserving existing ledger state across worker restarts.
func Initialize(path string, initial ObjectiveLedger) error {
	info, err := os.Stat(path)
	switch {
	case err == nil && info.Size() > 0:
		return nil
	case err == nil:
	case os.IsNotExist(err):
	default:
		return err
	}
	return Write(path, initial)
}

func Write(path string, ledger ObjectiveLedger) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	ledger.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	data, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func firstParagraph(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	for _, part := range strings.Split(text, "\n\n") {
		part = strings.TrimSpace(part)
		if part != "" && !strings.HasPrefix(part, "---") {
			return CompactText(part, 500)
		}
	}
	return CompactText(text, 500)
}

func CompactText(text string, max int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if len(text) <= max {
		return text
	}
	return text[:max] + "..."
}

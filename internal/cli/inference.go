package cli

import (
	"crypto/rand"
	"path/filepath"
	"sync/atomic"

	"github.com/telos-org/telos/internal/executor"
	"github.com/telos-org/telos/internal/game"
	"github.com/telos-org/telos/internal/sessionapi"
)

// Settings are read once at the start of each complete prover/verifier turn.
type sessionInferenceExecutor struct {
	sessionDir string
	pi         *executor.PiExecutor
	defaults   sessionapi.InferenceSettings
}

func (e *sessionInferenceExecutor) WorkspaceState() string { return e.pi.WorkspaceState() }
func (e *sessionInferenceExecutor) CheckpointWorkspace(dest string) bool {
	return e.pi.CheckpointWorkspace(dest)
}

func (e *sessionInferenceExecutor) ExecuteTurn(task, role string, ts *game.TurnState) game.TurnResult {
	path := manifestPath(e.sessionDir)
	if e.defaults.Model == "" {
		e.defaults = sessionapi.InferenceSettings{Model: e.pi.Model, Thinking: e.pi.Thinking}
	}
	failure := func(err error) game.TurnResult {
		return game.TurnResult{Role: role, Error: "inference settings: " + err.Error()}
	}
	if err := recoverTurnInference(path); err != nil {
		return failure(err)
	}
	attempt := rand.Text()
	receiptPath := filepath.Join(ts.Dir, "inference-"+attempt+".json")
	update, err := sessionapi.ClaimInferenceUpdate(path, attempt, receiptPath, e.defaults)
	if err != nil {
		return failure(err)
	}
	m, err := sessionapi.ReadManifest(path)
	if err != nil {
		return failure(err)
	}
	if m.Config.Model == "" {
		m.Config.Model = e.defaults.Model
	}
	if m.Config.Thinking == "" {
		m.Config.Thinking = e.defaults.Thinking
	}
	e.pi.Model, e.pi.Thinking = m.Config.Model, m.Config.Thinking
	definition := m.InferenceModelDefinition
	if update != nil {
		e.pi.Model, e.pi.Thinking = update.Settings.Model, update.Settings.Thinking
		if update.Model != nil && (len(update.ModelDefinition) > 0 || e.pi.Model != m.Config.Model) {
			definition = update.ModelDefinition
		}
	}
	e.pi.Startup, e.pi.OnStartup = nil, nil
	var persistErr error
	var stop atomic.Bool
	settled := false
	if update != nil || (m.InferenceUpdate != nil && m.InferenceUpdate.Status == "applied") || len(definition) > 0 {
		e.pi.Startup = &executor.PiStartupConfig{
			AttemptID: attempt, Model: e.pi.Model, Thinking: e.pi.Thinking,
			Definition: definition, ReceiptPath: receiptPath,
		}
		if update != nil {
			e.pi.Startup.RequestID = update.RequestID
			e.pi.OnStartup = func() {
				settled, persistErr = confirmTurnInference(path, m)
				stop.Store(persistErr != nil)
			}
		}
	}
	state := *ts
	priorStop := state.StopRequested
	state.StopRequested = func() bool { return stop.Load() || (priorStop != nil && priorStop()) }
	result := e.pi.ExecuteTurn(task, role, &state)
	if persistErr != nil {
		return failure(persistErr)
	}
	if update != nil && !settled {
		settled, err = confirmTurnInference(path, m)
		if err != nil {
			return failure(err)
		}
		if !settled {
			if err := sessionapi.FinishInferenceUpdate(path, update.RequestID, "unknown", "Pi startup was not confirmed; saved settings were retained", nil); err != nil {
				return failure(err)
			}
		}
	}
	// A rejection receipt proves the guard exited before dispatching the prompt.
	// Continue this turn with the previous settings rather than failing the goal.
	if update != nil && settled {
		receipt, err := executor.ReadPiStartupReceipt(receiptPath)
		if err == nil && receipt.Error != "" && (priorStop == nil || !priorStop()) {
			e.pi.Model, e.pi.Thinking = m.Config.Model, m.Config.Thinking
			e.pi.OnStartup = nil
			e.pi.Startup = nil
			if len(m.InferenceModelDefinition) > 0 {
				e.pi.Startup = &executor.PiStartupConfig{AttemptID: rand.Text(), Model: e.pi.Model, Thinking: e.pi.Thinking, Definition: m.InferenceModelDefinition, ReceiptPath: receiptPath + ".fallback"}
			}
			return e.pi.ExecuteTurn(task, role, ts)
		}
	}
	return result
}

func confirmTurnInference(path string, m *sessionapi.Manifest) (bool, error) {
	u := m.InferenceUpdate
	if u == nil || u.Status != "applying" || u.ReceiptPath == "" {
		return false, nil
	}
	r, err := executor.ReadPiStartupReceipt(u.ReceiptPath)
	if err != nil || r.RequestID != u.RequestID || r.AttemptID != u.AttemptID {
		return false, nil
	}
	if r.Error != "" {
		return true, sessionapi.FinishInferenceUpdate(path, u.RequestID, "rejected", r.Error, nil)
	}
	want := sessionapi.InferenceSettings{Model: m.Config.Model, Thinking: m.Config.Thinking}
	if u.Model != nil {
		want.Model = *u.Model
	}
	if u.Thinking != nil {
		want.Thinking = *u.Thinking
	}
	if u.Settings != nil {
		want = *u.Settings
	}
	if r.Model != want.Model || r.Thinking != want.Thinking {
		return true, sessionapi.FinishInferenceUpdate(path, u.RequestID, "unknown", "Pi startup receipt does not match the queued settings", nil)
	}
	return true, sessionapi.FinishInferenceUpdate(path, u.RequestID, "applied", "", &want)
}

func recoverTurnInference(path string) error {
	m, err := sessionapi.ReadManifest(path)
	if err != nil {
		return err
	}
	if m.InferenceUpdate == nil || m.InferenceUpdate.Status != "applying" {
		return nil
	}
	confirmed, err := confirmTurnInference(path, m)
	if err != nil || confirmed {
		return err
	}
	return sessionapi.RecoverInferenceUpdate(path)
}

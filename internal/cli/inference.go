package cli

import (
	"github.com/telos-org/telos/internal/executor"
	"github.com/telos-org/telos/internal/game"
	"github.com/telos-org/telos/internal/sessionapi"
)

// sessionInferenceExecutor starts each prover/verifier turn with the
// session's current model, thinking, model definition, and connection.
type sessionInferenceExecutor struct {
	*executor.PiExecutor
	sessionDir string
}

func (e *sessionInferenceExecutor) ExecuteTurn(task, role string, ts *game.TurnState) game.TurnResult {
	m, err := sessionapi.ReadManifest(manifestPath(e.sessionDir))
	if err != nil {
		return game.TurnResult{Role: role, Error: "read inference settings: " + err.Error()}
	}
	if m.Config.Model != "" {
		e.Model = m.Config.Model
	}
	if m.Config.Thinking != "" {
		e.Thinking = m.Config.Thinking
	}
	e.ModelDefinition, e.Connection = m.InferenceModelDefinition, m.InferenceConnection
	return e.PiExecutor.ExecuteTurn(task, role, ts)
}

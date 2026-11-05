package cli

import (
	"github.com/telos-org/telos/internal/executor"
	"github.com/telos-org/telos/internal/game"
	"github.com/telos-org/telos/internal/sessionapi"
)

// sessionInferenceExecutor starts each prover/verifier turn with the
// session's current model, thinking, model definition, and connection. A child
// takes the root's settings once they are newer than the ones it inherited.
type sessionInferenceExecutor struct {
	*executor.PiExecutor
	sessionDir string
}

func (e *sessionInferenceExecutor) ExecuteTurn(task, role string, ts *game.TurnState) game.TurnResult {
	path := manifestPath(e.sessionDir)
	m, err := sessionapi.ReadManifest(path)
	if err == nil {
		if root, _ := sessionapi.ReadRootManifest(e.sessionDir); root != nil && root.Config.InferenceGeneration > m.Config.InferenceGeneration {
			m, err = sessionapi.MutateManifest(path, func(m *sessionapi.Manifest) error {
				m.Config.InferenceGeneration, m.Config.Model, m.Config.Thinking = root.Config.InferenceGeneration, root.Config.Model, root.Config.Thinking
				m.InferenceModelDefinition, m.InferenceConnection = root.InferenceModelDefinition, root.InferenceConnection
				return nil
			})
		}
	}
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

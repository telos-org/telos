//go:build !telos_testfake

package cli

import (
	"github.com/telos-org/telos/internal/executor"
	"github.com/telos-org/telos/internal/game"
	"github.com/telos-org/telos/internal/platform"
)

func createAgentExecutor(workspace string, cfg LocalRunConfig) (game.AgentExecutor, error) {
	p := platform.NewLocalPlatform(workspace)
	model := cfg.Model
	if model == "" {
		model = DefaultLocalModel
	}
	return executor.NewNativeExecutor(p, model, cfg.Thinking, cfg.AgentTimeoutSec), nil
}

//go:build !telos_testfake

package cli

import "github.com/telos-org/telos/internal/game"

func createAgentExecutor(workspace string, cfg LocalRunConfig) (game.AgentExecutor, error) {
	return createPiExecutor(workspace, cfg)
}

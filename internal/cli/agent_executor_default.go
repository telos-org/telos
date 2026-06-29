//go:build !telos_testfake

package cli

import (
	"github.com/telos-org/telos/internal/executor"
	"github.com/telos-org/telos/internal/game"
	"github.com/telos-org/telos/internal/gateway"
	"github.com/telos-org/telos/internal/platform"
)

func createAgentExecutor(workspace string, cfg LocalRunConfig) (game.AgentExecutor, error) {
	p := platform.NewLocalPlatform(workspace)
	model := cfg.Model
	if model == "" {
		model = DefaultLocalModel
	}
	cred, err := gateway.Resolve(cfg.SessionID)
	if err != nil {
		return nil, err
	}
	exec := executor.NewNativeExecutorWithGateway(
		p,
		model,
		cfg.Thinking,
		cfg.AgentTimeoutSec,
		executor.GatewayConfig{BaseURL: cred.BaseURL, APIKey: cred.APIKey, CostHardLimit: cred.CostHardLimit},
		cred.Cleanup,
	)
	return exec, nil
}

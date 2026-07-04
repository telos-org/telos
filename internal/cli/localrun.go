// Package cli implements the telos CLI commands.
package cli

import (
	"github.com/telos-org/telos/internal/game"
	"github.com/telos-org/telos/internal/sessionapi"
	"github.com/telos-org/telos/internal/sessionrun"
)

const DefaultLocalModel = sessionrun.DefaultLocalModel

type LocalRunConfig = sessionrun.LocalRunConfig
type LocalSession = sessionrun.LocalSession

func CreateLocalSession(specPath string, cfg LocalRunConfig) (*LocalSession, error) {
	return sessionrun.CreateLocalSession(specPath, cfg)
}

func SubmitLocalSession(specPath string, cfg LocalRunConfig) (*LocalSession, error) {
	return sessionrun.SubmitLocalSession(specPath, cfg)
}

func RunLocalSession(sessionDir string) (*game.PVGResult, error) {
	return sessionrun.RunLocalSession(sessionDir)
}

func RunLocalSessionWithExecutor(sessionDir string, exec game.AgentExecutor) (*game.PVGResult, error) {
	return sessionrun.RunLocalSessionWithExecutor(sessionDir, exec)
}

func finishEpoch(sessionDir string, manifest *sessionapi.Manifest, result *game.PVGResult) error {
	return sessionrun.FinishEpoch(sessionDir, manifest, result)
}

func ensureSessionWorkspace(sessionDir string, manifest *sessionapi.Manifest) error {
	return sessionrun.EnsureSessionWorkspace(sessionDir, manifest)
}

func localWorkerEnv(sessionDir string) []string {
	return sessionrun.LocalWorkerEnv(sessionDir)
}

func runnerIdentity(pid int) sessionapi.Runner {
	return sessionrun.RunnerIdentity(pid)
}

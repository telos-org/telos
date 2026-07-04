package cli

import "github.com/telos-org/telos/internal/sessionrun"

const workspaceModeEmpty = sessionrun.WorkspaceModeEmpty

func DefaultLocalSessionRoot(scope string) (string, error) {
	return sessionrun.DefaultLocalSessionRoot(scope)
}

func extractWorkspaceArtifact(archivePath string, dest string) error {
	return sessionrun.ExtractWorkspaceArtifact(archivePath, dest)
}

func resolveLinkTarget(scope string, target string) string {
	return sessionrun.ResolveLinkTarget(scope, target)
}

func samePath(left string, right string) bool {
	return sessionrun.SamePath(left, right)
}

package telosd

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/telos-org/telos/internal/sessionapi"
)

func CurrentRuntimeIdentity(version string) (sessionapi.RuntimeIdentity, error) {
	executable, err := runningExecutablePath()
	if err != nil {
		return sessionapi.RuntimeIdentity{}, err
	}
	file, err := os.Open(executable)
	if err != nil {
		return sessionapi.RuntimeIdentity{}, fmt.Errorf("open running telosd executable: %w", err)
	}
	defer file.Close()
	return runtimeIdentity(version, file)
}

func runningExecutablePath() (string, error) {
	if runtime.GOOS == "linux" {
		return "/proc/self/exe", nil
	}
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve telosd executable: %w", err)
	}
	return executable, nil
}

func runtimeIdentity(version string, executable io.Reader) (sessionapi.RuntimeIdentity, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		return sessionapi.RuntimeIdentity{}, fmt.Errorf("telosd build version is empty")
	}
	hasher := sha256.New()
	if _, err := io.Copy(hasher, executable); err != nil {
		return sessionapi.RuntimeIdentity{}, fmt.Errorf("hash telosd executable: %w", err)
	}
	return sessionapi.RuntimeIdentity{
		Version:      version,
		TelosdDigest: fmt.Sprintf("sha256:%x", hasher.Sum(nil)),
		Capabilities: []string{sessionapi.CapabilityEpochFinalizedEventsV1},
	}, nil
}

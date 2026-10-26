package main

import (
	"fmt"
	"os"
	"syscall"

	"github.com/telos-org/telos/internal/sessionapi"
)

func cmdRun(_ []string) {
	fmt.Fprintln(os.Stderr, "error: local execution is not supported on Windows; use telos apply to deploy to Telos Cloud")
	os.Exit(1)
}

func localRunAvailable() bool { return false }

func localSessionRoot() string                { return "" }
func listLocalSessions() []sessionapi.Session { return nil }
func getLocalSession(id string) (*sessionapi.Session, error) {
	return nil, localSessionNotFoundError(id)
}
func getLocalTranscript(id string) (string, error) { return "", localSessionNotFoundError(id) }
func getLocalEvents(id string) ([]sessionapi.SessionEvent, error) {
	return nil, localSessionNotFoundError(id)
}
func stopSessionAnywhere(id string) (*sessionapi.Session, error) {
	return nil, localSessionNotFoundError(id)
}

func lockInstallation(path string) (*os.File, error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("cannot lock installation: %w", err)
	}
	handle, err := syscall.CreateFile(name, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil, syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, fmt.Errorf("cannot lock installation (another update may be running; check directory permissions): %w", err)
	}
	return os.NewFile(uintptr(handle), path), nil
}

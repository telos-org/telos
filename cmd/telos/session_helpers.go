package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/telos-org/telos/internal/cli"
	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/sessionapi"
)

func store() *sessionapi.FileStore {
	root, err := cli.DefaultLocalSessionRoot("")
	if err != nil {
		root = os.Getenv("TELOS_SESSION_DIR")
		if root == "" {
			root = filepath.Join(".telos", "sessions")
		}
	}
	return sessionapi.NewFileStore(root, sessionapi.RuntimeLocal)
}

func resolveSpecPath(input string) string {
	if path, ok := existingSpecPath(input); ok {
		return path
	}
	abs, _ := filepath.Abs(input)
	return abs
}

func existingSpecPath(input string) (string, bool) {
	if info, err := os.Stat(input); err == nil {
		if !info.IsDir() {
			abs, _ := filepath.Abs(input)
			return abs, true
		}
		for _, name := range []string{"SPEC.md", "spec.md"} {
			candidate := filepath.Join(input, name)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				abs, _ := filepath.Abs(candidate)
				return abs, true
			}
		}
		return "", false
	}
	return "", false
}

func sessionCreateRequestForSpec(input string) (sessionapi.SessionCreateRequest, error) {
	if path, ok := existingSpecPath(input); ok {
		specData, err := os.ReadFile(path)
		if err != nil {
			return sessionapi.SessionCreateRequest{}, err
		}
		md := string(specData)
		return sessionapi.SessionCreateRequest{SpecMarkdown: &md}, nil
	}
	if input == "" {
		return sessionapi.SessionCreateRequest{}, fmt.Errorf("empty spec")
	}
	return sessionapi.SessionCreateRequest{}, fmt.Errorf("spec file not found: %s", input)
}

func localSessionExists(sessionID string) bool {
	_, err := store().Get(sessionID)
	return err == nil
}

func isDeploymentID(id string) bool {
	return strings.HasPrefix(id, "dep_")
}

func getSessionFromAnywhere(sessionID string) (*sessionapi.Session, error) {
	if isDeploymentID(sessionID) {
		return nil, fmt.Errorf("deployment %s is not a session; use deployment-aware command output", sessionID)
	}

	// Try local first
	s := store()
	session, err := s.Get(sessionID)
	if err == nil {
		return session, nil
	}

	if ctx, ok := rootSessionContext(); ok {
		session, err := cloud.NewClient(ctx.endpoint, ctx.token).GetSession(sessionID)
		if err == nil {
			return session, nil
		}
		return nil, fmt.Errorf("root session lookup failed: %w", err)
	}

	return nil, localSessionNotFoundError(sessionID)
}

func getTranscriptFromAnywhere(sessionID string) (string, error) {
	s := store()
	text, err := s.Transcript(sessionID)
	if err == nil {
		return text, nil
	}

	if ctx, ok := rootSessionContext(); ok {
		text, err := cloud.NewClient(ctx.endpoint, ctx.token).GetTranscript(sessionID)
		if err == nil {
			return text, nil
		}
		return "", fmt.Errorf("root transcript lookup failed: %w", err)
	}

	if localSessionExists(sessionID) {
		return "", fmt.Errorf("transcript for session %s: %w", sessionID, sessionapi.ErrNotFound)
	}
	return "", localSessionNotFoundError(sessionID)
}

func stopSessionAnywhere(sessionID string) (*sessionapi.Session, error) {
	if isDeploymentID(sessionID) {
		return nil, fmt.Errorf("deployment %s is not a session; use deployment delete", sessionID)
	}

	s := store()
	session, err := s.Stop(sessionID)
	if err == nil {
		return session, nil
	}

	return nil, localSessionNotFoundError(sessionID)
}

func localSessionNotFoundError(sessionID string) error {
	return localSessionNotFound{sessionID: sessionID, root: localSessionRoot()}
}

func localSessionRoot() string {
	root, err := cli.DefaultLocalSessionRoot("")
	if err == nil {
		return root
	}
	return filepath.Join(".telos", "sessions")
}

type localSessionNotFound struct {
	sessionID string
	root      string
}

func (e localSessionNotFound) Error() string {
	return fmt.Sprintf(
		"session %s not found in %s\n\nLocal sessions are workspace-scoped. Run this command from the workspace where the session was created, or set:\n\n  TELOS_SESSION_DIR=/path/to/.telos/sessions",
		e.sessionID,
		e.root,
	)
}

func (e localSessionNotFound) Unwrap() error {
	return sessionapi.ErrNotFound
}

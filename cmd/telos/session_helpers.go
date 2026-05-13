package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/telos-org/telos-go/internal/config"
	"github.com/telos-org/telos-go/internal/hosted"
	"github.com/telos-org/telos-go/internal/sessionapi"
)

func store() *sessionapi.FileStore {
	root := os.Getenv("TELOS_SESSION_DIR")
	if root == "" {
		root = filepath.Join(".telos", "sessions")
	}
	return sessionapi.NewFileStore(root)
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
	specID := strings.TrimSpace(input)
	if specID == "" {
		return sessionapi.SessionCreateRequest{}, fmt.Errorf("empty spec")
	}
	if looksLikeSpecPath(specID) {
		return sessionapi.SessionCreateRequest{}, fmt.Errorf("spec file not found: %s", specID)
	}
	return sessionapi.SessionCreateRequest{SpecID: &specID}, nil
}

func looksLikeSpecPath(input string) bool {
	return strings.Contains(input, "/") ||
		strings.Contains(input, string(os.PathSeparator)) ||
		strings.Contains(input, "..") ||
		strings.EqualFold(filepath.Ext(input), ".md")
}

func localSessionExists(sessionID string) bool {
	_, err := store().Get(sessionID)
	return err == nil
}

func hostedClientForSession(sessionID, envID string) (*hosted.Client, error) {
	clients, err := hostedSessionClients(envID)
	if err != nil {
		return nil, err
	}
	for _, client := range clients {
		if _, err := client.GetSession(sessionID); err == nil {
			return client, nil
		}
	}
	return nil, fmt.Errorf("session %s: not found", sessionID)
}

func getSessionFromAnywhere(sessionID, envID string) (*sessionapi.Session, error) {
	// Try local first
	s := store()
	session, err := s.Get(sessionID)
	if err == nil {
		return session, nil
	}

	if ctx, ok := controllerSessionContext(); ok {
		session, err := hosted.NewClient(ctx.endpoint, ctx.token).GetSession(sessionID)
		if err == nil {
			return session, nil
		}
		return nil, fmt.Errorf("controller session lookup failed: %w", err)
	}

	// Try hosted
	if envID != "" || config.IsConfigured() {
		clients, err := hostedSessionClients(envID)
		if err != nil && envID != "" {
			return nil, err
		}
		hostedErr := err
		for _, client := range clients {
			session, err := client.GetSession(sessionID)
			if err == nil {
				return session, nil
			}
		}
		if hostedErr != nil {
			return nil, fmt.Errorf("session %s not found locally; hosted lookup failed: %w", sessionID, hostedErr)
		}
	}

	return nil, fmt.Errorf("session %s: not found", sessionID)
}

func getTranscriptFromAnywhere(sessionID, envID string) (string, error) {
	s := store()
	text, err := s.Transcript(sessionID)
	if err == nil {
		return text, nil
	}

	if ctx, ok := controllerSessionContext(); ok {
		text, err := hosted.NewClient(ctx.endpoint, ctx.token).GetTranscript(sessionID)
		if err == nil {
			return text, nil
		}
		return "", fmt.Errorf("controller transcript lookup failed: %w", err)
	}

	if envID != "" || config.IsConfigured() {
		clients, err := hostedSessionClients(envID)
		if err != nil && envID != "" {
			return "", err
		}
		hostedErr := err
		for _, client := range clients {
			text, err := client.GetTranscript(sessionID)
			if err == nil {
				return text, nil
			}
		}
		if hostedErr != nil {
			return "", fmt.Errorf("session %s transcript not found locally; hosted lookup failed: %w", sessionID, hostedErr)
		}
	}

	return "", fmt.Errorf("session %s transcript: %w", sessionID, sessionapi.ErrNotFound)
}

func stopSessionAnywhere(sessionID, envID string) (*sessionapi.Session, error) {
	s := store()
	session, err := s.Stop(sessionID)
	if err == nil {
		return session, nil
	}

	if envID != "" || config.IsConfigured() {
		clients, err := hostedSessionClients(envID)
		if err != nil && envID != "" {
			return nil, err
		}
		hostedErr := err
		for _, client := range clients {
			session, err := client.StopSession(sessionID)
			if err == nil {
				return session, nil
			}
		}
		if hostedErr != nil {
			return nil, fmt.Errorf("session %s not found locally; hosted lookup failed: %w", sessionID, hostedErr)
		}
	}

	return nil, fmt.Errorf("session %s: not found", sessionID)
}

package main

import (
	"os"
	"strings"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/sessionapi"
)

type rootContext struct {
	endpoint  string
	token     string
	sessionID string
}

func rootSessionContext() (rootContext, bool) {
	if strings.TrimSpace(os.Getenv("TELOS_RUNTIME")) == string(sessionapi.RuntimeLocal) {
		return rootContext{}, false
	}
	token := strings.TrimSpace(os.Getenv("TELOS_API_TOKEN"))
	sessionID := strings.TrimSpace(os.Getenv("TELOS_SESSION_ID"))
	if token == "" || sessionID == "" {
		return rootContext{}, false
	}
	endpoint := strings.TrimSpace(os.Getenv("TELOS_API_ENDPOINT"))
	if endpoint == "" {
		endpoint = "http://127.0.0.1:8000"
	}
	return rootContext{
		endpoint:  cloud.NormalizeEndpoint(endpoint),
		token:     token,
		sessionID: sessionID,
	}, true
}

// insideTelosSession reports whether the command runs inside a Telos session,
// local or hosted. Goals are top-level, so apply is rejected there.
func insideTelosSession() bool {
	return strings.TrimSpace(os.Getenv("TELOS_SESSION_ID")) != ""
}

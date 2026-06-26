package main

import (
	"os"
	"path/filepath"
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
	endpoint := strings.TrimSpace(os.Getenv("TELOS_CLUSTER_API_ENDPOINT"))
	if endpoint == "" {
		endpoint = "http://telos-api.ns-telos-env.svc.cluster.local:8000"
	}
	return rootContext{
		endpoint:  cloud.NormalizeEndpoint(endpoint),
		token:     token,
		sessionID: sessionID,
	}, true
}

func localRootSessionID() (string, bool) {
	sessionID := strings.TrimSpace(os.Getenv("TELOS_SESSION_ID"))
	sessionRoot := strings.TrimSpace(os.Getenv("TELOS_SESSION_DIR"))
	if sessionID == "" || sessionRoot == "" {
		return "", false
	}
	if strings.TrimSpace(os.Getenv("TELOS_RUNTIME")) != string(sessionapi.RuntimeLocal) {
		return "", false
	}
	manifest, err := sessionapi.ReadManifest(filepath.Join(sessionRoot, sessionID, "session.json"))
	if err != nil || manifest.SessionKind != sessionapi.KindController {
		return "", false
	}
	return sessionID, true
}

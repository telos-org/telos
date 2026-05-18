package main

import (
	"os"
	"strings"

	"github.com/telos-org/telos-go/internal/cloud"
)

type controllerContext struct {
	endpoint  string
	token     string
	sessionID string
}

func controllerSessionContext() (controllerContext, bool) {
	token := strings.TrimSpace(os.Getenv("TELOS_API_TOKEN"))
	sessionID := strings.TrimSpace(os.Getenv("TELOS_SESSION_ID"))
	if token == "" || sessionID == "" {
		return controllerContext{}, false
	}
	endpoint := strings.TrimSpace(os.Getenv("TELOS_CLUSTER_API_ENDPOINT"))
	if endpoint == "" {
		endpoint = "http://telos-api.ns-telos-env.svc.cluster.local:8000"
	}
	return controllerContext{
		endpoint:  cloud.NormalizeEndpoint(endpoint),
		token:     token,
		sessionID: sessionID,
	}, true
}

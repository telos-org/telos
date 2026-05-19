package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/telos-org/telos-go/internal/cloud"
)

func TestEnvironmentOutputDoesNotExposeAccessToken(t *testing.T) {
	env := &cloud.Environment{
		ID:             "env_123",
		Handle:         "env-abc.usetelos.ai",
		AccessToken:    "secret-token",
		State:          "ready",
		HasRecoverable: true,
	}

	data, err := json.Marshal(environmentOutput(env))
	if err != nil {
		t.Fatalf("marshal environment output: %v", err)
	}
	text := string(data)
	if strings.Contains(text, "secret-token") || strings.Contains(text, "AccessToken") || strings.Contains(text, "access_token") {
		t.Fatalf("environment output leaked token: %s", text)
	}
	if !strings.Contains(text, `"id":"env_123"`) || !strings.Contains(text, `"handle":"env-abc.usetelos.ai"`) {
		t.Fatalf("environment output dropped public fields: %s", text)
	}
}

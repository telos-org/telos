package sessionapi_test

import (
	"encoding/json"
	"testing"

	"github.com/telos-org/telos/internal/sessionapi"
)

func TestSessionRuntimeRejectsLegacyHostedValue(t *testing.T) {
	var session sessionapi.Session
	if err := json.Unmarshal([]byte(`{"session_id":"sess_1","status":"running","runtime":"hosted"}`), &session); err == nil {
		t.Fatal("expected legacy hosted runtime to fail")
	}
}

func TestSessionCreateRequestOmitsInternalSessionKind(t *testing.T) {
	kind := sessionapi.KindController
	body, err := json.Marshal(sessionapi.SessionCreateRequest{SessionKind: &kind})
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "{}" {
		t.Fatalf("session_kind should not be public JSON: %s", body)
	}
}

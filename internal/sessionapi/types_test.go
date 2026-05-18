package sessionapi_test

import (
	"encoding/json"
	"testing"

	"github.com/telos-org/telos-go/internal/sessionapi"
)

func TestSessionRuntimeDoesNotNormalizeLegacyHostedValue(t *testing.T) {
	var session sessionapi.Session
	if err := json.Unmarshal([]byte(`{"session_id":"sess_1","status":"running","runtime":"hosted"}`), &session); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if session.Runtime != "hosted" {
		t.Fatalf("runtime: got %q", session.Runtime)
	}
}

func TestSessionConfigPreservesUnknownFields(t *testing.T) {
	var cfg sessionapi.SessionConfig
	if err := json.Unmarshal([]byte(`{"model":"opus","max_rounds":8,"future_knob":{"x":1}}`), &cfg); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if cfg.Model != "opus" || cfg.MaxRounds != 8 {
		t.Fatalf("typed fields: got model=%q max_rounds=%d", cfg.Model, cfg.MaxRounds)
	}

	m := cfg.AsMap()
	if _, ok := m["future_knob"].(map[string]any); !ok {
		t.Fatalf("future_knob was not preserved: %#v", m["future_knob"])
	}
}

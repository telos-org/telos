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

func TestSessionConfigPreservesUnknownFields(t *testing.T) {
	var cfg sessionapi.SessionConfig
	if err := json.Unmarshal([]byte(`{"model":"opus","max_rounds":8,"future_knob":{"x":1}}`), &cfg); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if cfg.Model != "opus" {
		t.Fatalf("typed fields: got model=%q", cfg.Model)
	}

	m := cfg.AsMap()
	if got, ok := m["max_rounds"].(float64); !ok || got != 8 {
		t.Fatalf("max_rounds should be preserved as unknown field: %#v", m["max_rounds"])
	}
	if _, ok := m["future_knob"].(map[string]any); !ok {
		t.Fatalf("future_knob was not preserved: %#v", m["future_knob"])
	}
}

func TestSessionConfigRoundTripsOptionalRoleConfig(t *testing.T) {
	cfg := sessionapi.SessionConfig{
		Model:     "shared/model",
		Thinking:  "medium",
		Generator: &sessionapi.RoleConfig{Model: "generator/model", Thinking: "high"},
		Verifier:  &sessionapi.RoleConfig{Model: "verifier/model", Thinking: "low"},
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var decoded sessionapi.SessionConfig
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Generator == nil || *decoded.Generator != *cfg.Generator {
		t.Fatalf("generator: got %#v", decoded.Generator)
	}
	if decoded.Verifier == nil || *decoded.Verifier != *cfg.Verifier {
		t.Fatalf("verifier: got %#v", decoded.Verifier)
	}
}

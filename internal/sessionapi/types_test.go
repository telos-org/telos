package sessionapi_test

import (
	"encoding/json"
	"strings"
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

func TestInferenceConnectionAcceptsClaudePlaceholdersOnlyForAnthropic(t *testing.T) {
	placeholder := "telos-proxy-" + strings.Repeat("a", 43)
	for _, tc := range []struct {
		provider, key string
		valid         bool
	}{
		{"anthropic", "sk-ant-oat01-" + placeholder, true},
		{"anthropic", "sk-ant-oat01-" + strings.Repeat("a", 43), false},
		{"xai", "sk-ant-oat01-" + placeholder, false},
	} {
		connection := sessionapi.InferenceConnection{ID: "profile", Provider: tc.provider, BaseURL: "https://gateway.example/v1", ProxyURL: "http://172.31.255.1:18080", APIKey: tc.key}
		if err := connection.Validate(tc.provider); (err == nil) != tc.valid {
			t.Fatalf("Validate(%s, %q) = %v, want valid=%v", tc.provider, tc.key, err, tc.valid)
		}
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

func TestSessionConfigPreservesLegacyRoleConfigAsUnknownFields(t *testing.T) {
	data := []byte(`{"model":"shared/model","generator":{"model":"generator/model"},"verifier":{"model":"verifier/model"}}`)
	var cfg sessionapi.SessionConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	roundTrip, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(roundTrip, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"generator", "verifier"} {
		if _, ok := decoded[key].(map[string]any); !ok {
			t.Fatalf("legacy %s config was not preserved: %#v", key, decoded[key])
		}
	}
}

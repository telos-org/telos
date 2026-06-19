package sessionapi_test

import (
	"encoding/json"
	"testing"

	"github.com/telos-org/telos/internal/game"
	"github.com/telos-org/telos/internal/sessionapi"
)

func TestSessionRuntimeRejectsLegacyHostedValue(t *testing.T) {
	var session sessionapi.Session
	if err := json.Unmarshal([]byte(`{"session_id":"sess_1","status":"running","runtime":"hosted"}`), &session); err == nil {
		t.Fatal("expected legacy hosted runtime to fail")
	}
}

func TestSessionCreateRequestRejectsInvalidSessionKind(t *testing.T) {
	var req sessionapi.SessionCreateRequest
	err := json.Unmarshal([]byte(`{"session_kind":"daemon"}`), &req)
	if err == nil {
		t.Fatal("expected invalid session_kind to fail")
	}
}

func TestSessionDefaultsMatchPVGRuntimeDefaults(t *testing.T) {
	if sessionapi.DefaultMaxRounds != game.DefaultMaxRounds {
		t.Fatalf("max rounds default drift: sessionapi=%d game=%d", sessionapi.DefaultMaxRounds, game.DefaultMaxRounds)
	}
	if sessionapi.DefaultMaxDurationSec != game.DefaultMaxDurationSec {
		t.Fatalf("max duration default drift: sessionapi=%d game=%d", sessionapi.DefaultMaxDurationSec, game.DefaultMaxDurationSec)
	}
}

func TestSessionConfigPreservesUnknownFields(t *testing.T) {
	var cfg sessionapi.SessionConfig
	if err := json.Unmarshal([]byte(`{"model":"opus","max_rounds":8,"max_input_tokens":1000,"max_output_tokens":500,"max_tool_loops":42,"safe_write_prefixes":["/tmp/telos-scratch","/workspace/outside"],"future_knob":{"x":1}}`), &cfg); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if cfg.Model != "opus" {
		t.Fatalf("typed fields: got model=%q", cfg.Model)
	}
	if cfg.MaxRounds != 8 {
		t.Fatalf("max_rounds typed field: got %#v", cfg.MaxRounds)
	}
	if cfg.MaxInputTokens != 1000 {
		t.Fatalf("max_input_tokens typed field: got %#v", cfg.MaxInputTokens)
	}
	if cfg.MaxOutputTokens != 500 {
		t.Fatalf("max_output_tokens typed field: got %#v", cfg.MaxOutputTokens)
	}
	if cfg.MaxToolLoops != 42 {
		t.Fatalf("max_tool_loops typed field: got %#v", cfg.MaxToolLoops)
	}

	m := cfg.AsMap()
	if got, ok := m["max_rounds"].(float64); !ok || got != 8 {
		t.Fatalf("max_rounds should be emitted from typed field: %#v", m["max_rounds"])
	}
	if got, ok := m["max_input_tokens"].(float64); !ok || got != 1000 {
		t.Fatalf("max_input_tokens should be emitted from typed field: %#v", m["max_input_tokens"])
	}
	if got, ok := m["max_output_tokens"].(float64); !ok || got != 500 {
		t.Fatalf("max_output_tokens should be emitted from typed field: %#v", m["max_output_tokens"])
	}
	if got, ok := m["max_tool_loops"].(float64); !ok || got != 42 {
		t.Fatalf("max_tool_loops should be emitted from typed field: %#v", m["max_tool_loops"])
	}
	prefixes, ok := m["safe_write_prefixes"].([]any)
	if !ok || len(prefixes) != 2 || prefixes[0] != "/tmp/telos-scratch" || prefixes[1] != "/workspace/outside" {
		t.Fatalf("safe_write_prefixes should be preserved as an unknown field: %#v", m["safe_write_prefixes"])
	}
	if _, ok := m["future_knob"].(map[string]any); !ok {
		t.Fatalf("future_knob was not preserved: %#v", m["future_knob"])
	}
}

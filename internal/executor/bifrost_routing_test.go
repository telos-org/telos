package executor

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/telos-org/telos/internal/game"
	"github.com/telos-org/telos/internal/gatewaycred"
	"github.com/telos-org/telos/internal/sessionapi"
)

func writeRoutingTestManifest(t *testing.T, dir string, routing *sessionapi.GatewayRoutingState) {
	t.Helper()
	manifest := &sessionapi.Manifest{
		SessionID:      "sess-route",
		SessionKind:    sessionapi.KindTask,
		Runtime:        sessionapi.RuntimeLocal,
		SpecName:       "demo",
		GatewayRouting: routing,
	}
	if err := sessionapi.WriteManifest(filepath.Join(dir, "session.json"), manifest); err != nil {
		t.Fatal(err)
	}
}

func readRoutingTestState(t *testing.T, dir string) *sessionapi.GatewayRoutingState {
	t.Helper()
	manifest, err := sessionapi.ReadManifest(filepath.Join(dir, "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	return manifest.GatewayRouting
}

func assertHeaders(t *testing.T, got, want map[string]string) {
	t.Helper()
	for name, value := range want {
		if got[name] != value {
			t.Fatalf("header %s: got %q, want %q (all: %v)", name, got[name], value, got)
		}
	}
}

func TestBifrostRoutingActive(t *testing.T) {
	if !bifrostRoutingActive(gatewayKindBifrost, "gpt-5") {
		t.Fatal("bifrost kind should activate routing")
	}
	if !bifrostRoutingActive(gatewayKindOpenAI, "telos-bifrost/standard-agent") {
		t.Fatal("telos-bifrost model should activate routing")
	}
	if bifrostRoutingActive(gatewayKindOpenAI, "gpt-5") {
		t.Fatal("plain openai gateway should not activate routing")
	}
}

func TestBifrostRoutingAgentHeadersNewSession(t *testing.T) {
	dir := t.TempDir()
	writeRoutingTestManifest(t, dir, nil)
	turn := &game.TurnState{EpochID: 2, RoundNum: 3, Role: "prover"}
	r := newBifrostRouting("sess-route", dir, gatewaycred.ModelProfileStandard, turn, "prover")

	assertHeaders(t, r.agentHeaders(), map[string]string{
		"x-bf-session-id":         "sess-route",
		"x-bf-session-ttl":        "1h",
		"x-bf-cache-key":          "sess-route",
		"x-llm-usecase":           "agent",
		"x-llm-session-phase":     "new",
		"x-llm-assigned-provider": "unset",
		"x-telos-model-profile":   "standard",
		"x-request-id":            "sess-route:2:3:prover",
	})
}

func TestBifrostRoutingUsesPersistedAssignment(t *testing.T) {
	dir := t.TempDir()
	writeRoutingTestManifest(t, dir, &sessionapi.GatewayRoutingState{
		ModelProfile:     sessionapi.ModelProfilePremium,
		AssignedProvider: "acme",
	})
	r := newBifrostRouting("sess-route", dir, gatewaycred.ModelProfilePremium, nil, "prover")

	assertHeaders(t, r.agentHeaders(), map[string]string{
		"x-llm-assigned-provider": "acme",
		"x-llm-session-phase":     "existing",
		"x-telos-model-profile":   "premium",
	})
}

func TestBifrostRoutingStateScopedToProfile(t *testing.T) {
	dir := t.TempDir()
	writeRoutingTestManifest(t, dir, &sessionapi.GatewayRoutingState{
		ModelProfile:     sessionapi.ModelProfileStandard,
		AssignedProvider: "acme",
	})
	r := newBifrostRouting("sess-route", dir, gatewaycred.ModelProfilePremium, nil, "prover")
	if r.assignedProvider != "" || r.phase != "new" {
		t.Fatalf("profile change should reset assignment: %+v", r)
	}
}

func TestBifrostRoutingCompactionHeaders(t *testing.T) {
	dir := t.TempDir()
	writeRoutingTestManifest(t, dir, nil)
	r := newBifrostRouting("sess-route", dir, gatewaycred.ModelProfileStandard, nil, "prover")

	assertHeaders(t, r.compactionHeaders(), map[string]string{
		"x-bf-session-id":         "sess-route:compaction",
		"x-bf-cache-key":          "sess-route:compaction",
		"x-llm-usecase":           "compaction",
		"x-llm-session-phase":     "existing",
		"x-llm-assigned-provider": "silares",
		"x-request-id":            "sess-route:compaction",
	})
	if got := r.compactionModel(); got != "standard-compaction" {
		t.Fatalf("compaction model: got %q", got)
	}
}

func TestBifrostRoutingPremiumCompactionFollowsAssignment(t *testing.T) {
	dir := t.TempDir()
	writeRoutingTestManifest(t, dir, &sessionapi.GatewayRoutingState{
		ModelProfile:     sessionapi.ModelProfilePremium,
		AssignedProvider: "acme",
	})
	r := newBifrostRouting("sess-route", dir, gatewaycred.ModelProfilePremium, nil, "prover")

	assertHeaders(t, r.compactionHeaders(), map[string]string{
		"x-llm-assigned-provider": "acme",
	})
	if got := r.compactionModel(); got != "premium-compaction" {
		t.Fatalf("compaction model: got %q", got)
	}
}

func TestBifrostRoutingObserveAssignsOnlySuccessfulAgentRoutes(t *testing.T) {
	dir := t.TempDir()
	writeRoutingTestManifest(t, dir, nil)
	r := newBifrostRouting("sess-route", dir, gatewaycred.ModelProfileStandard, nil, "prover")

	failed := &http.Response{StatusCode: http.StatusBadGateway, Header: http.Header{}}
	failed.Header.Set("x-bifrost-provider", "acme")
	r.observe(failed, "telos-bifrost/standard-agent")
	if state := readRoutingTestState(t, dir); state == nil || state.AssignedProvider != "" {
		t.Fatalf("failed route must not assign: %+v", state)
	}

	compaction := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}}
	compaction.Header.Set("x-bifrost-provider", "other")
	r.observe(compaction, "standard-compaction")
	if state := readRoutingTestState(t, dir); state.AssignedProvider != "" {
		t.Fatalf("compaction route must not assign: %+v", state)
	}

	success := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}}
	success.Header.Set("x-bifrost-provider", "acme")
	success.Header.Set("x-bifrost-original-model", "standard-agent")
	r.observe(success, "")
	state := readRoutingTestState(t, dir)
	if state.AssignedProvider != "acme" || state.LastModel != "standard-agent" {
		t.Fatalf("successful agent route should assign: %+v", state)
	}

	later := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}}
	later.Header.Set("x-telos-routed-provider", "other")
	later.Header.Set("x-telos-routed-model", "standard-agent")
	later.Header.Set("x-telos-routing-fallback", "true")
	r.observe(later, "")
	state = readRoutingTestState(t, dir)
	if state.AssignedProvider != "acme" {
		t.Fatalf("assignment must be sticky: %+v", state)
	}
	if !state.LastFallback {
		t.Fatalf("fallback flag should record: %+v", state)
	}
}

func TestBifrostRoutingObserveWithoutSessionDirIsNoop(t *testing.T) {
	r := newBifrostRouting("sess-route", "", gatewaycred.ModelProfileStandard, nil, "prover")
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}}
	resp.Header.Set("x-bifrost-provider", "acme")
	r.observe(resp, "standard-agent")
}

func TestBifrostRequestModelStripsNamespace(t *testing.T) {
	if got := bifrostRequestModel("telos-bifrost/premium-agent"); got != "premium-agent" {
		t.Fatalf("got %q", got)
	}
	if got := bifrostRequestModel("gpt-5"); got != "gpt-5" {
		t.Fatalf("got %q", got)
	}
	if !isBifrostAgentModel("telos-bifrost/standard-agent") || isBifrostAgentModel("standard-compaction") {
		t.Fatal("agent model detection")
	}
}

package executor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/game"
	"github.com/telos-org/telos/internal/gateway"
	"github.com/telos-org/telos/internal/platform"
	"github.com/telos-org/telos/internal/sessionapi"
)

func TestNewPiExecutorDefaultsToNoTimeout(t *testing.T) {
	exec := NewPiExecutor(nil, "claude-test", "", 0)

	if exec.Timeout != 0 {
		t.Fatalf("timeout should default to disabled, got %d", exec.Timeout)
	}
	if exec.Thinking != "medium" {
		t.Fatalf("thinking: got %q", exec.Thinking)
	}
}

func TestBuildPiArgvUsesTextModeWithoutSessionByDefault(t *testing.T) {
	argv := BuildPiArgv("claude-test", "high", "", "", "")
	if len(argv) != 6 {
		t.Fatalf("expected 6 args, got %d", len(argv))
	}
	if argv[0] != "sh" {
		t.Errorf("first arg: got %q", argv[0])
	}
	if argv[4] != "claude-test" {
		t.Errorf("model arg: got %q", argv[4])
	}
	if argv[5] != "high" {
		t.Errorf("thinking arg: got %q", argv[5])
	}
	if !strings.Contains(argv[2], `prompt="${TELOS_TASK}"`) {
		t.Errorf("task prompt is not expanded from env: %s", argv[2])
	}
	if !strings.Contains(argv[2], `--mode text`) {
		t.Errorf("pi should run in text mode: %s", argv[2])
	}
	if !strings.Contains(argv[2], `--no-session`) {
		t.Errorf("fallback path should stay ephemeral: %s", argv[2])
	}
	if strings.Contains(argv[2], `--mode json`) {
		t.Errorf("pi should not use the streaming json event mode: %s", argv[2])
	}
}

func TestBuildPiArgvUsesTaskFileAndSessionFile(t *testing.T) {
	argv := BuildPiArgv("claude-test", "high", "/tmp/task.md", "/tmp/pi-session.jsonl", "")
	if len(argv) != 8 {
		t.Fatalf("expected 8 args, got %d", len(argv))
	}
	if argv[6] != "@/tmp/task.md" {
		t.Errorf("task file arg: got %q", argv[6])
	}
	if argv[7] != "/tmp/pi-session.jsonl" {
		t.Errorf("session file arg: got %q", argv[7])
	}
	if !strings.Contains(argv[2], `--session "$4"`) {
		t.Errorf("pi session file is not selected from argv: %s", argv[2])
	}
	if strings.Contains(argv[2], `-p "${TELOS_TASK}"`) {
		t.Errorf("task env is still expanded directly into argv: %s", argv[2])
	}
}

func TestBuildPiArgvUsesSessionFileWithoutTaskFile(t *testing.T) {
	argv := BuildPiArgv("claude-test", "high", "", "/tmp/pi-session.jsonl", "")
	if len(argv) != 8 {
		t.Fatalf("expected 8 args with empty task placeholder, got %d", len(argv))
	}
	if argv[6] != "" {
		t.Errorf("task placeholder: got %q", argv[6])
	}
	if argv[7] != "/tmp/pi-session.jsonl" {
		t.Errorf("session file arg: got %q", argv[7])
	}
}

func TestBuildPiArgvLoadsExplicitExtension(t *testing.T) {
	argv := BuildPiArgv("telos-bifrost/standard-agent", "high", "/tmp/task.md", "/tmp/pi-session.jsonl", "/tmp/telos_bifrost.ts")
	if len(argv) != 9 {
		t.Fatalf("expected 9 args, got %d: %#v", len(argv), argv)
	}
	if argv[8] != "/tmp/telos_bifrost.ts" {
		t.Fatalf("extension arg: got %q", argv[8])
	}
	if !strings.Contains(argv[2], `--no-extensions --extension "$5"`) {
		t.Fatalf("pi should load explicit extension while discovery is disabled: %s", argv[2])
	}
}

func TestConfigureGatewayInjectsOpenAIEnvAndCleanup(t *testing.T) {
	exec := NewPiExecutor(nil, "test-model", "", 0)
	cleaned := false

	err := exec.ConfigureGateway(gateway.Credential{
		BaseURL:       "https://proxy.example.com/v1/",
		APIKey:        "sk-session",
		Transport:     gateway.TransportOpenAISync,
		Kind:          gateway.KindBifrost,
		Headers:       map[string]string{"x-bf-vk": "sk-bf"},
		ModelProfile:  "premium",
		CostHardLimit: true,
		Cleanup: func() error {
			cleaned = true
			return nil
		},
	})
	if err != nil {
		t.Fatalf("ConfigureGateway: %v", err)
	}
	if exec.GatewayEnv["OPENAI_API_KEY"] != "sk-session" {
		t.Fatalf("OPENAI_API_KEY: got %q", exec.GatewayEnv["OPENAI_API_KEY"])
	}
	if exec.GatewayEnv["OPENAI_BASE_URL"] != "https://proxy.example.com/v1" {
		t.Fatalf("OPENAI_BASE_URL: got %q", exec.GatewayEnv["OPENAI_BASE_URL"])
	}
	if exec.GatewayEnv["TELOS_GATEWAY_API_KEY"] != "sk-session" || exec.GatewayEnv["TELOS_GATEWAY_BASE_URL"] != "https://proxy.example.com/v1" {
		t.Fatalf("Telos gateway env: %+v", exec.GatewayEnv)
	}
	if exec.GatewayEnv["TELOS_GATEWAY_KIND"] != gateway.KindBifrost || exec.GatewayEnv["TELOS_MODEL_PROFILE"] != "premium" {
		t.Fatalf("Telos gateway profile/kind env: %+v", exec.GatewayEnv)
	}
	if exec.GatewayEnv["TELOS_GATEWAY_HEADERS"] != `{"x-bf-vk":"sk-bf"}` {
		t.Fatalf("TELOS_GATEWAY_HEADERS: got %q", exec.GatewayEnv["TELOS_GATEWAY_HEADERS"])
	}
	if !exec.CostHardLimit() {
		t.Fatal("cost hard limit should be true")
	}
	if err := exec.Cleanup(); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if !cleaned {
		t.Fatal("cleanup function was not called")
	}
}

func TestConfigureGatewayRejectsBifrostAsyncForPi(t *testing.T) {
	exec := NewPiExecutor(nil, "test-model", "", 0)
	cleaned := false

	err := exec.ConfigureGateway(gateway.Credential{
		BaseURL:   "https://proxy.example.com/openai",
		APIKey:    "sk-session",
		Transport: gateway.TransportBifrostAsync,
		Cleanup: func() error {
			cleaned = true
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "bifrost_async transport requires the native executor") {
		t.Fatalf("expected bifrost_async rejection, got %v", err)
	}
	if !cleaned {
		t.Fatal("cleanup should run after rejected managed credential")
	}
}

func TestBifrostExtensionUsesNativeRoutingHeaders(t *testing.T) {
	for _, want := range []string{
		`"x-bifrost-provider"`,
		`"x-bifrost-original-model"`,
		`"x-bifrost-fallback-index"`,
		`"x-telos-routed-provider"`,
	} {
		if !strings.Contains(telosBifrostExtension, want) {
			t.Fatalf("embedded Bifrost extension missing %s", want)
		}
	}
	if strings.Contains(telosBifrostExtension, `"x-bifrost-resolved-model"`) {
		t.Fatal("embedded Bifrost extension must not use resolved upstream model as the routed model")
	}
}

func TestExecuteTurnIncludesStderrOnPiFailure(t *testing.T) {
	workspace := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	piPath := filepath.Join(bin, "pi")
	script := "#!/bin/sh\necho \"EROFS: read-only file system\" >&2\nexit 1\n"
	if err := os.WriteFile(piPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	p := platform.NewLocalPlatform(workspace)
	p.Env = map[string]string{"HOME": home}
	exec := NewPiExecutor(p, "test-model", "high", 0)

	result := exec.ExecuteTurn("do it", "prover", nil)

	if result.Error == "" || !strings.Contains(result.Error, "pi_failed:1") {
		t.Fatalf("error: got %q", result.Error)
	}
	if !strings.Contains(result.Logs, "[stderr]") ||
		!strings.Contains(result.Logs, "EROFS: read-only file system") {
		t.Fatalf("logs should include stderr, got %q", result.Logs)
	}
}

func TestReadPiSessionExtractsAssistantTextStatsAndTurns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pi-session.jsonl")
	writePiSession(t, path, `{"type":"session","version":3,"id":"sess","timestamp":"2026-05-21T00:00:00Z","cwd":"/tmp"}`)
	appendPiSession(t, path, `{"type":"message","id":"u","parentId":null,"timestamp":"2026-05-21T00:00:01Z","message":{"role":"user","content":"do it","timestamp":1770000000000}}`)
	appendPiSession(t, path, `{"type":"message","id":"t","parentId":"u","timestamp":"2026-05-21T00:00:02Z","message":{"role":"toolResult","toolCallId":"call_1","toolName":"bash","content":[{"type":"text","text":"ok"}],"isError":false,"timestamp":1770000000001}}`)
	appendPiSession(t, path, `{"type":"message","id":"a","parentId":"t","timestamp":"2026-05-21T00:00:03Z","message":{"role":"assistant","provider":"openai-codex","model":"gpt-5.5","stopReason":"stop","content":[{"type":"thinking","thinking":"hidden"},{"type":"text","text":"Implemented it.\n\n<status>CONCEDE</status>\n"}],"usage":{"input":10,"output":20,"cacheRead":30,"cacheWrite":40,"totalTokens":100,"cost":{"input":0.1,"output":0.2,"cacheRead":0.3,"cacheWrite":0.4,"total":1.0}},"timestamp":1770000000002}}`)

	summary, err := ReadPiSession(path)
	if err != nil {
		t.Fatalf("ReadPiSession: %v", err)
	}
	if summary.Logs != "Implemented it.\n\n<status>CONCEDE</status>\n" {
		t.Fatalf("logs: got %q", summary.Logs)
	}
	if summary.Error != "" {
		t.Fatalf("error: got %q", summary.Error)
	}
	want := game.TurnStats{
		CostUSD:             1.0,
		NumTurns:            1,
		InputTokens:         10,
		OutputTokens:        20,
		CacheReadTokens:     30,
		CacheCreationTokens: 40,
		Model:               "gpt-5.5",
	}
	if summary.Stats != want {
		t.Fatalf("stats: got %+v want %+v", summary.Stats, want)
	}
}

func TestReadPiSessionUsesLastAssistantTextAndAggregatesAssistantUsage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pi-session.jsonl")
	writePiSession(t, path, `{"type":"session","version":3,"id":"sess","timestamp":"2026-05-21T00:00:00Z","cwd":"/tmp"}`)
	appendPiSession(t, path, `{"type":"message","id":"a1","parentId":null,"timestamp":"2026-05-21T00:00:01Z","message":{"role":"assistant","model":"gpt-5.5","stopReason":"stop","content":[{"type":"text","text":"first"}],"usage":{"input":1,"output":1,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.1}}}}`)
	appendPiSession(t, path, `{"type":"message","id":"a2","parentId":"a1","timestamp":"2026-05-21T00:00:02Z","message":{"role":"assistant","model":"gpt-5.5","stopReason":"stop","content":[{"type":"text","text":"second"}],"usage":{"input":2,"output":3,"cacheRead":4,"cacheWrite":5,"cost":{"total":0.6}}}}`)

	summary, err := ReadPiSession(path)
	if err != nil {
		t.Fatalf("ReadPiSession: %v", err)
	}
	if summary.Logs != "second" {
		t.Fatalf("logs: got %q", summary.Logs)
	}
	if summary.Stats.InputTokens != 3 || summary.Stats.OutputTokens != 4 ||
		summary.Stats.CacheReadTokens != 4 || summary.Stats.CacheCreationTokens != 5 ||
		summary.Stats.CostUSD != 0.7 {
		t.Fatalf("stats should sum all assistant usage: %+v", summary.Stats)
	}
}

func TestReadPiSessionExtractsLatestBifrostRoutingEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pi-session.jsonl")
	writePiSession(t, path, `{"type":"session","version":3,"id":"sess","timestamp":"2026-05-21T00:00:00Z","cwd":"/tmp"}`)
	appendPiSession(t, path, `{"type":"custom","customType":"telos-bifrost-routing","data":{"provider":"sailresearch","model":"standard-compaction","fallback":false}}`)
	appendPiSession(t, path, `{"type":"custom","customType":"other","data":{"provider":"ignored"}}`)
	appendPiSession(t, path, `{"type":"custom","customType":"telos-bifrost-routing","data":{"provider":"silares","model":"standard-agent","fallback":true}}`)
	appendPiSession(t, path, `{"type":"message","id":"a","parentId":null,"timestamp":"2026-05-21T00:00:03Z","message":{"role":"assistant","model":"gpt-5.5","stopReason":"stop","content":[{"type":"text","text":"done"}]}}`)

	summary, err := ReadPiSession(path)
	if err != nil {
		t.Fatalf("ReadPiSession: %v", err)
	}
	if summary.Routing == nil {
		t.Fatal("expected routing observation")
	}
	if summary.Routing.Provider != "silares" || summary.Routing.Model != "standard-agent" || !summary.Routing.Fallback || !summary.Routing.OK {
		t.Fatalf("routing: %+v", summary.Routing)
	}
}

func TestReadPiSessionFromOffsetIgnoresStaleRoutingEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pi-session.jsonl")
	writePiSession(t, path, `{"type":"session","version":3,"id":"sess","timestamp":"2026-05-21T00:00:00Z","cwd":"/tmp"}`)
	appendPiSession(t, path, `{"type":"custom","customType":"telos-bifrost-routing","data":{"provider":"silares","model":"standard-agent","fallback":false}}`)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	appendPiSession(t, path, `{"type":"message","id":"a","parentId":null,"timestamp":"2026-05-21T00:00:03Z","message":{"role":"assistant","model":"gpt-5.5","stopReason":"stop","content":[{"type":"text","text":"done"}]}}`)

	summary, err := ReadPiSessionFromOffset(path, info.Size())
	if err != nil {
		t.Fatalf("ReadPiSessionFromOffset: %v", err)
	}
	if summary.Routing != nil {
		t.Fatalf("stale routing entry should be ignored: %+v", summary.Routing)
	}
}

func TestUpdateRoutingStateAssignsOnlyAgentRoutes(t *testing.T) {
	sessionDir := t.TempDir()
	manifestPath := filepath.Join(sessionDir, "session.json")
	if err := sessionapi.WriteInitialManifest(manifestPath, sessionapi.InitialManifest{
		SessionID:   "sess-routing",
		SessionKind: sessionapi.KindTask,
		Runtime:     sessionapi.RuntimeLocal,
		CreatedAt:   "2026-07-03T00:00:00.000Z",
		Launcher:    "local",
		SpecName:    "routing",
		Config:      sessionapi.SessionConfig{ModelProfile: sessionapi.ModelProfileStandard},
	}); err != nil {
		t.Fatal(err)
	}
	exec := NewPiExecutor(nil, "telos-bifrost/standard-agent", "high", 0)
	exec.SessionDir = sessionDir

	if err := exec.updateRoutingState(sessionapi.ModelProfileStandard, PiRoutingObservation{Provider: "silares", Model: "standard-compaction"}); err != nil {
		t.Fatalf("update compaction route: %v", err)
	}
	manifest, err := sessionapi.ReadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.GatewayRouting == nil || manifest.GatewayRouting.AssignedProvider != "" {
		t.Fatalf("compaction route should not assign provider: %#v", manifest.GatewayRouting)
	}

	if err := exec.updateRoutingState(sessionapi.ModelProfileStandard, PiRoutingObservation{Provider: "silares", Model: "standard-agent", Fallback: true, OK: true}); err != nil {
		t.Fatalf("update agent route: %v", err)
	}
	manifest, err = sessionapi.ReadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.GatewayRouting.AssignedProvider != "silares" || manifest.GatewayRouting.LastModel != "standard-agent" || !manifest.GatewayRouting.LastFallback {
		t.Fatalf("routing state: %#v", manifest.GatewayRouting)
	}
}

func TestUpdateRoutingStateDoesNotAssignFailedAgentRoute(t *testing.T) {
	sessionDir := t.TempDir()
	manifestPath := filepath.Join(sessionDir, "session.json")
	if err := sessionapi.WriteInitialManifest(manifestPath, sessionapi.InitialManifest{
		SessionID:   "sess-routing",
		SessionKind: sessionapi.KindTask,
		Runtime:     sessionapi.RuntimeLocal,
		CreatedAt:   "2026-07-03T00:00:00.000Z",
		Launcher:    "local",
		SpecName:    "routing",
		Config:      sessionapi.SessionConfig{ModelProfile: sessionapi.ModelProfileStandard},
	}); err != nil {
		t.Fatal(err)
	}
	exec := NewPiExecutor(nil, "telos-bifrost/standard-agent", "high", 0)
	exec.SessionDir = sessionDir

	if err := exec.updateRoutingState(sessionapi.ModelProfileStandard, PiRoutingObservation{Provider: "silares", Model: "standard-agent", OK: false}); err != nil {
		t.Fatalf("update failed route: %v", err)
	}
	manifest, err := sessionapi.ReadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.GatewayRouting == nil {
		t.Fatal("expected routing state")
	}
	if manifest.GatewayRouting.AssignedProvider != "" {
		t.Fatalf("failed route should not assign provider: %#v", manifest.GatewayRouting)
	}
	if manifest.GatewayRouting.LastModel != "standard-agent" {
		t.Fatalf("failed route should still update observability: %#v", manifest.GatewayRouting)
	}
}

func TestRoutingStateIsScopedToModelProfile(t *testing.T) {
	sessionDir := t.TempDir()
	manifestPath := filepath.Join(sessionDir, "session.json")
	if err := sessionapi.WriteInitialManifest(manifestPath, sessionapi.InitialManifest{
		SessionID:   "sess-routing",
		SessionKind: sessionapi.KindTask,
		Runtime:     sessionapi.RuntimeLocal,
		CreatedAt:   "2026-07-03T00:00:00.000Z",
		Launcher:    "local",
		SpecName:    "routing",
		Config:      sessionapi.SessionConfig{ModelProfile: sessionapi.ModelProfilePremium},
	}); err != nil {
		t.Fatal(err)
	}
	manifest, err := sessionapi.ReadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest.GatewayRouting = &sessionapi.GatewayRoutingState{
		ModelProfile:     sessionapi.ModelProfileStandard,
		AssignedProvider: "silares",
		LastModel:        "standard-agent",
	}
	if err := sessionapi.WriteManifest(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}

	exec := NewPiExecutor(nil, "telos-bifrost/premium-agent", "high", 0)
	exec.SessionDir = sessionDir
	routing := exec.currentRoutingState(sessionapi.ModelProfilePremium)
	if routing.AssignedProvider != "" || routing.ModelProfile != sessionapi.ModelProfilePremium {
		t.Fatalf("current routing should ignore standard assignment for premium: %#v", routing)
	}

	if err := exec.updateRoutingState(sessionapi.ModelProfilePremium, PiRoutingObservation{Provider: "premium-provider", Model: "premium-agent", OK: true}); err != nil {
		t.Fatalf("update premium route: %v", err)
	}
	manifest, err = sessionapi.ReadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.GatewayRouting.ModelProfile != sessionapi.ModelProfilePremium || manifest.GatewayRouting.AssignedProvider != "premium-provider" {
		t.Fatalf("premium routing should replace stale standard routing: %#v", manifest.GatewayRouting)
	}
}

func TestReadPiSessionMapsLengthStopToRecoverableError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pi-session.jsonl")
	writePiSession(t, path, `{"type":"session","version":3,"id":"sess","timestamp":"2026-05-21T00:00:00Z","cwd":"/tmp"}`)
	appendPiSession(t, path, `{"type":"message","id":"a","parentId":null,"timestamp":"2026-05-21T00:00:01Z","message":{"role":"assistant","model":"gpt-5.5","stopReason":"length","content":[{"type":"text","text":"partial"}],"usage":{"input":1,"output":2,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.3}}}}`)

	summary, err := ReadPiSession(path)
	if err != nil {
		t.Fatalf("ReadPiSession: %v", err)
	}
	if summary.Error != "agent_output_truncated:length" {
		t.Fatalf("error: got %q", summary.Error)
	}
}

func TestReadPiSessionIgnoresTransientErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pi-session.jsonl")
	writePiSession(t, path, `{"type":"session","version":3,"id":"sess","timestamp":"2026-05-21T00:00:00Z","cwd":"/tmp"}`)
	appendPiSession(t, path, `{"type":"message","id":"a","parentId":null,"timestamp":"2026-05-21T00:00:01Z","message":{"role":"assistant","model":"gpt-5.5","stopReason":"error","errorMessage":"overloaded_error: try again","content":[{"type":"text","text":"partial"}],"usage":{"input":1,"output":2,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.3}}}}`)

	summary, err := ReadPiSession(path)
	if err != nil {
		t.Fatalf("ReadPiSession: %v", err)
	}
	if summary.Error != "" {
		t.Fatalf("transient error should be ignored, got %q", summary.Error)
	}
}

func TestReadPiSessionRequiresAssistantMessage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pi-session.jsonl")
	writePiSession(t, path, `{"type":"session","version":3,"id":"sess","timestamp":"2026-05-21T00:00:00Z","cwd":"/tmp"}`)

	_, err := ReadPiSession(path)
	if err == nil || !strings.Contains(err.Error(), "no assistant message") {
		t.Fatalf("expected no assistant error, got %v", err)
	}
}

func writePiSession(t *testing.T, path string, line string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendPiSession(t *testing.T, path string, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatal(err)
	}
}

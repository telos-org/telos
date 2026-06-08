package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/telos-org/telos/internal/config"
	"github.com/telos-org/telos/internal/sessionapi"
	"github.com/telos-org/telos/internal/spec"
)

func TestReorderInterspersedFlags(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.Bool("json", false, "")
	fs.String("workspace", "", "")
	fs.Int("until", 0, "")

	got := reorderInterspersedFlags(fs, []string{
		"SPEC.md",
		"--json",
		"--workspace",
		"/tmp/ws",
		"--until=3",
	})
	want := []string{
		"--json",
		"--workspace",
		"/tmp/ws",
		"--until=3",
		"SPEC.md",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestTopLevelUsageMentionsHelpAndVersion(t *testing.T) {
	var out bytes.Buffer
	usage(&out)
	text := out.String()
	for _, want := range []string{
		"usage: telos <command> [args]",
		"--help",
		"version            Show version",
		"--version",
		"telos <command> --help",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("usage missing %q:\n%s", want, text)
		}
	}
}

func TestPrintPlanPreviewLocal(t *testing.T) {
	compiled := &spec.CompiledEnvironment{
		Environment: &spec.EnvironmentSpec{Name: "hello-service"},
		ContentHash: "8a8f0c21",
		Skills: []*spec.Skill{
			{Name: "verify-engineering"},
		},
	}

	var out bytes.Buffer
	printPlanPreview(&out, compiled, "./SPEC.md", "local", "task", "")
	text := out.String()
	for _, want := range []string{
		"Spec      hello-service",
		"Platform  local",
		"Session   task",
		"Mutates   no",
		"Path      ./SPEC.md",
		"Hash      8a8f0c21",
		"Skills    verify-engineering",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("plan output missing %q:\n%s", want, text)
		}
	}
	for _, notWant := range []string{"Target", "Namespace", "Plan for", "No sessions"} {
		if strings.Contains(text, notWant) {
			t.Fatalf("plan output should not contain %q:\n%s", notWant, text)
		}
	}
}

func TestPrintPlanPreviewCloud(t *testing.T) {
	compiled := &spec.CompiledEnvironment{
		Environment: &spec.EnvironmentSpec{Name: "gitea"},
		Namespace:   "ns-gitea",
		ContentHash: "8a8f0c21",
		Skills: []*spec.Skill{
			{Name: "verify-engineering"},
			{Name: "verify-quality"},
		},
	}

	var out bytes.Buffer
	printPlanPreview(&out, compiled, "./SPEC.md", "cloud", "controller", "env_123")
	text := out.String()
	for _, want := range []string{
		"Spec      gitea",
		"Platform  cloud",
		"Session   controller",
		"Mutates   no",
		"Path      ./SPEC.md",
		"Namespace ns-gitea",
		"Hash      8a8f0c21",
		"Skills    verify-engineering, verify-quality",
		"Target    env_123",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("plan output missing %q:\n%s", want, text)
		}
	}
}

func TestReorderInterspersedFlagsDashDash(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.Bool("json", false, "")

	got := reorderInterspersedFlags(fs, []string{"--json", "--", "-literal"})
	want := []string{"--json", "-literal"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestFlagNamesSetUsesExplicitFlagsOnly(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.String("thinking", "medium", "")
	fs.Int("until", 0, "")
	fs.String("workspace", "", "")
	parseFlags(fs, []string{"--thinking", "medium", "SPEC.md"})

	if !flagNamesSet(fs, "thinking") {
		t.Fatal("expected explicitly passed --thinking to be detected")
	}
	if flagNamesSet(fs, "until", "workspace") {
		t.Fatal("defaulted flags should not count as explicitly set")
	}
}

func TestResolveLocalRunConfigUsesEnvironmentDefaults(t *testing.T) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.String("workspace", "", "")
	fs.String("model", "", "")
	fs.String("thinking", "medium", "")
	fs.Float64("max-cost-usd", 20.0, "")
	fs.Int("agent-timeout-sec", 0, "")
	parseFlags(fs, []string{"SPEC.md"})

	t.Setenv("TELOS_WORKSPACE", "/tmp/telos-workspace")
	t.Setenv("TELOS_MODEL", "claude-test")
	t.Setenv("TELOS_THINKING", "high")
	t.Setenv("TELOS_MAX_COST_USD", "12.5")
	t.Setenv("TELOS_AGENT_TIMEOUT_SEC", "123")

	cfg, err := resolveLocalRunConfigFromFlags(fs, "", "", "medium", 20.0, 0)
	if err != nil {
		t.Fatalf("resolveLocalRunConfigFromFlags: %v", err)
	}
	if cfg.Workspace != "/tmp/telos-workspace" {
		t.Fatalf("workspace: got %q", cfg.Workspace)
	}
	if cfg.Model != "claude-test" || cfg.Thinking != "high" {
		t.Fatalf("model/thinking: got %q/%q", cfg.Model, cfg.Thinking)
	}
	if cfg.AgentTimeoutSec != 123 {
		t.Fatalf("timeout: got %d", cfg.AgentTimeoutSec)
	}
	if cfg.MaxCostUSD == nil || *cfg.MaxCostUSD != 12.5 {
		t.Fatalf("cost: got %v", cfg.MaxCostUSD)
	}
}

func TestResolveLocalRunConfigDefaultsToNoAgentTimeout(t *testing.T) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.String("workspace", "", "")
	fs.String("model", "", "")
	fs.String("thinking", "medium", "")
	fs.Float64("max-cost-usd", 20.0, "")
	fs.Int("agent-timeout-sec", 0, "")
	parseFlags(fs, []string{"SPEC.md"})

	cfg, err := resolveLocalRunConfigFromFlags(fs, "", "", "medium", 20.0, 0)
	if err != nil {
		t.Fatalf("resolveLocalRunConfigFromFlags: %v", err)
	}
	if cfg.AgentTimeoutSec != 0 {
		t.Fatalf("agent timeout should default to disabled, got %d", cfg.AgentTimeoutSec)
	}
}

func TestResolveLocalRunConfigAllowsExplicitNoAgentTimeout(t *testing.T) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.String("workspace", "", "")
	fs.String("model", "", "")
	fs.String("thinking", "medium", "")
	fs.Float64("max-cost-usd", 20.0, "")
	fs.Int("agent-timeout-sec", 0, "")
	parseFlags(fs, []string{"--agent-timeout-sec", "0", "SPEC.md"})

	cfg, err := resolveLocalRunConfigFromFlags(fs, "", "", "medium", 20.0, 0)
	if err != nil {
		t.Fatalf("resolveLocalRunConfigFromFlags: %v", err)
	}
	if cfg.AgentTimeoutSec != 0 {
		t.Fatalf("agent timeout should be disabled, got %d", cfg.AgentTimeoutSec)
	}
}

func TestResolveLocalRunConfigRejectsNegativeAgentTimeout(t *testing.T) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.String("workspace", "", "")
	fs.String("model", "", "")
	fs.String("thinking", "medium", "")
	fs.Float64("max-cost-usd", 20.0, "")
	fs.Int("agent-timeout-sec", 0, "")
	parseFlags(fs, []string{"--agent-timeout-sec", "-1", "SPEC.md"})

	_, err := resolveLocalRunConfigFromFlags(fs, "", "", "medium", 20.0, -1)
	if err == nil {
		t.Fatal("expected negative agent timeout to fail")
	}
	if !strings.Contains(err.Error(), "non-negative") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveSessionRuntimeConfigUsesExplicitFlags(t *testing.T) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.String("model", "", "")
	fs.String("thinking", "medium", "")
	fs.Float64("max-cost-usd", 20.0, "")
	fs.Int("agent-timeout-sec", 0, "")
	parseFlags(fs, []string{
		"--model", "openai-codex/gpt-5.5",
		"--thinking", "high",
		"--max-cost-usd", "100",
		"--agent-timeout-sec", "0",
		"SPEC.md",
	})

	cfg, err := resolveSessionRuntimeConfigFromFlags(fs, "openai-codex/gpt-5.5", "high", 100, 0)
	if err != nil {
		t.Fatalf("resolveSessionRuntimeConfigFromFlags: %v", err)
	}
	req := sessionapi.SessionCreateRequest{}
	applySessionRuntimeConfig(&req, cfg)
	if req.Model != "openai-codex/gpt-5.5" || req.Thinking != "high" {
		t.Fatalf("model/thinking: got %q/%q", req.Model, req.Thinking)
	}
	if req.MaxCostUSD == nil || *req.MaxCostUSD != 100 {
		t.Fatalf("max cost: got %v", req.MaxCostUSD)
	}
	if req.AgentTimeoutSec == nil || *req.AgentTimeoutSec != 0 {
		t.Fatalf("agent timeout: got %v", req.AgentTimeoutSec)
	}
}

func TestUpdateRequestFromCreateCarriesRuntimeConfig(t *testing.T) {
	specMarkdown := "---\nversion: v0\nname: demo\n---\n# Demo\n"
	maxCost := 8.5
	agentTimeout := 7200
	req := sessionapi.SessionCreateRequest{
		SpecMarkdown:    &specMarkdown,
		Model:           "sail-research/moonshotai/Kimi-K2.6",
		Thinking:        "high",
		MaxCostUSD:      &maxCost,
		AgentTimeoutSec: &agentTimeout,
	}

	update := updateRequestFromCreate(req)
	if update.SpecMarkdown != specMarkdown {
		t.Fatalf("spec markdown: got %q", update.SpecMarkdown)
	}
	if update.Model != req.Model || update.Thinking != req.Thinking {
		t.Fatalf("runtime config: got %#v", update)
	}
	if update.MaxCostUSD == nil || *update.MaxCostUSD != maxCost {
		t.Fatalf("max cost: got %#v", update.MaxCostUSD)
	}
	if update.AgentTimeoutSec == nil || *update.AgentTimeoutSec != agentTimeout {
		t.Fatalf("agent timeout: got %#v", update.AgentTimeoutSec)
	}
}

func TestResolveSessionRuntimeConfigOmitsDefaults(t *testing.T) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.String("model", "", "")
	fs.String("thinking", "medium", "")
	fs.Float64("max-cost-usd", 20.0, "")
	fs.Int("agent-timeout-sec", 0, "")
	parseFlags(fs, []string{"SPEC.md"})

	cfg, err := resolveSessionRuntimeConfigFromFlags(fs, "", "medium", 20.0, 0)
	if err != nil {
		t.Fatalf("resolveSessionRuntimeConfigFromFlags: %v", err)
	}
	req := sessionapi.SessionCreateRequest{}
	applySessionRuntimeConfig(&req, cfg)
	if req.Model != "" || req.Thinking != "" || req.MaxCostUSD != nil || req.AgentTimeoutSec != nil {
		t.Fatalf("expected empty runtime request config, got %#v", req)
	}
}

func TestUntilFlagValue(t *testing.T) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.Int("until", 0, "")
	parseFlags(fs, []string{"--until", "5", "SPEC.md"})

	got, err := untilFlagValue(fs, 5)
	if err != nil {
		t.Fatalf("untilFlagValue: %v", err)
	}
	if got != 5 {
		t.Fatalf("until: got %d", got)
	}
}

func TestUntilFlagValueRejectsNonPositive(t *testing.T) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.Int("until", 0, "")
	parseFlags(fs, []string{"--until", "0", "SPEC.md"})

	_, err := untilFlagValue(fs, 0)
	if err == nil {
		t.Fatal("expected --until 0 to fail")
	}
	if !strings.Contains(err.Error(), "positive") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveLocalRunConfigRejectsInvalidEnvironmentDefaults(t *testing.T) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.Int("agent-timeout-sec", 0, "")
	parseFlags(fs, []string{"SPEC.md"})
	t.Setenv("TELOS_AGENT_TIMEOUT_SEC", "not-an-int")

	_, err := resolveLocalRunConfigFromFlags(fs, "", "", "medium", 20.0, 0)
	if err == nil {
		t.Fatal("expected invalid environment value to fail")
	}
	if !strings.Contains(err.Error(), "must be an integer") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDecideLaunchModeMatchesPythonParity(t *testing.T) {
	tests := []struct {
		name            string
		platform        string
		envID           string
		cloudConfigured bool
		localConfigSet  bool
		want            launchMode
		wantErr         string
	}{
		{
			name:     "local spec runs locally",
			platform: "local",
			want:     launchLocal,
		},
		{
			name:           "local spec accepts local flags",
			platform:       "local",
			localConfigSet: true,
			want:           launchLocal,
		},
		{
			name:     "local spec rejects env",
			platform: "local",
			envID:    "env_123",
			wantErr:  "--env cannot be used with platform: local specs",
		},
		{
			name:            "unspecified platform is cloud",
			cloudConfigured: true,
			want:            launchCloudNew,
		},
		{
			name:    "unspecified platform requires cloud login",
			wantErr: "non-local spec requires cloud config",
		},
		{
			name:     "cloud spec with env uses existing env",
			platform: "cloud",
			envID:    "env_123",
			want:     launchCloudExisting,
		},
		{
			name:           "cloud rejects local flags",
			platform:       "cloud",
			localConfigSet: true,
			wantErr:        "local run config flags require a platform: local spec",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decideLaunchMode(
				tt.platform,
				tt.envID,
				tt.cloudConfigured,
				tt.localConfigSet,
			)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error: got %q, want %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("decideLaunchMode: %v", err)
			}
			if got != tt.want {
				t.Fatalf("mode: got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSessionCreateRequestForLocalSpec(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SPEC.md"), []byte("---\nname: demo\n---\n# Demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	req, err := sessionCreateRequestForSpec(dir)
	if err != nil {
		t.Fatalf("sessionCreateRequestForSpec: %v", err)
	}
	if req.SpecMarkdown == nil || !strings.Contains(*req.SpecMarkdown, "name: demo") {
		t.Fatalf("expected spec markdown, got %#v", req)
	}
}

func TestSessionKindForCommand(t *testing.T) {
	if got := sessionKindForCommand("apply"); got != sessionapi.KindController {
		t.Fatalf("apply kind: got %q", got)
	}
	if got := sessionKindForCommand("run"); got != sessionapi.KindTask {
		t.Fatalf("run kind: got %q", got)
	}
}

func TestSessionCreateRequestRejectsCatalogueSpecID(t *testing.T) {
	_, err := sessionCreateRequestForSpec("cal-diy")
	if err == nil {
		t.Fatal("expected catalogue spec id to fail")
	}
	if !strings.Contains(err.Error(), "spec file not found: cal-diy") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSessionCreateRequestRejectsMissingSpecPath(t *testing.T) {
	for _, input := range []string{"missing/SPEC.md", "../SPEC.md", "SPEC.md"} {
		t.Run(input, func(t *testing.T) {
			_, err := sessionCreateRequestForSpec(input)
			if err == nil {
				t.Fatal("expected missing local spec path to fail")
			}
			if !strings.Contains(err.Error(), "spec file not found") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestSpecNameFromRequest(t *testing.T) {
	markdown := "---\nversion: v0\nname: postgres\n---\n# Postgres\n"
	req := sessionapi.SessionCreateRequest{SpecMarkdown: &markdown}

	name, err := specNameFromRequest(req)
	if err != nil {
		t.Fatalf("specNameFromRequest: %v", err)
	}
	if name != "postgres" {
		t.Fatalf("name: got %q", name)
	}
}

func TestActiveControllerForSpec(t *testing.T) {
	controller := sessionapi.KindController
	task := sessionapi.KindTask
	other := "redis"
	target := "postgres"
	sessions := []sessionapi.Session{
		{SessionID: "sess_task", SessionKind: &task, SpecName: &target, Status: sessionapi.StatusRunning},
		{SessionID: "sess_old", SessionKind: &controller, SpecName: &target, Status: sessionapi.StatusStopped},
		{SessionID: "sess_other", SessionKind: &controller, SpecName: &other, Status: sessionapi.StatusRunning},
		{SessionID: "sess_done", SessionKind: &controller, SpecName: &target, Status: sessionapi.StatusCompleted},
		{SessionID: "sess_failed", SessionKind: &controller, SpecName: &target, Status: sessionapi.StatusFailed},
		{SessionID: "sess_target", SessionKind: &controller, SpecName: &target, Status: sessionapi.StatusRunning},
	}

	match, err := activeControllerForSpec(sessions, "postgres")
	if err != nil {
		t.Fatalf("activeControllerForSpec: %v", err)
	}
	if match == nil || match.SessionID != "sess_target" {
		t.Fatalf("match: got %#v", match)
	}
}

func TestActiveControllerForSpecIgnoresTerminalSessions(t *testing.T) {
	controller := sessionapi.KindController
	target := "postgres"
	sessions := []sessionapi.Session{
		{SessionID: "sess_completed", SessionKind: &controller, SpecName: &target, Status: sessionapi.StatusCompleted},
		{SessionID: "sess_failed", SessionKind: &controller, SpecName: &target, Status: sessionapi.StatusFailed},
		{SessionID: "sess_stopped", SessionKind: &controller, SpecName: &target, Status: sessionapi.StatusStopped},
		{SessionID: "sess_stale", SessionKind: &controller, SpecName: &target, Status: sessionapi.StatusStale},
	}

	match, err := activeControllerForSpec(sessions, "postgres")
	if err != nil {
		t.Fatalf("activeControllerForSpec: %v", err)
	}
	if match != nil {
		t.Fatalf("match: got %#v", match)
	}
}

func TestActiveControllerForSpecRejectsDuplicates(t *testing.T) {
	controller := sessionapi.KindController
	target := "postgres"
	sessions := []sessionapi.Session{
		{SessionID: "sess_a", SessionKind: &controller, SpecName: &target, Status: sessionapi.StatusRunning},
		{SessionID: "sess_b", SessionKind: &controller, SpecName: &target, Status: sessionapi.StatusRunning},
	}

	_, err := activeControllerForSpec(sessions, "postgres")
	if err == nil {
		t.Fatal("expected duplicate active controllers to fail")
	}
	if !strings.Contains(err.Error(), "multiple active controller sessions") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestControllerForApplyRecoversFailedController(t *testing.T) {
	controller := sessionapi.KindController
	target := "postgres"
	sessions := []sessionapi.Session{
		{SessionID: "sess_failed", SessionKind: &controller, SpecName: &target, Status: sessionapi.StatusFailed},
	}

	match, err := controllerForApply(sessions, "postgres")
	if err != nil {
		t.Fatalf("controllerForApply: %v", err)
	}
	if match == nil || match.SessionID != "sess_failed" {
		t.Fatalf("match: got %#v", match)
	}
}

func TestControllerForApplyPrefersActiveController(t *testing.T) {
	controller := sessionapi.KindController
	target := "postgres"
	sessions := []sessionapi.Session{
		{SessionID: "sess_failed", SessionKind: &controller, SpecName: &target, Status: sessionapi.StatusFailed},
		{SessionID: "sess_active", SessionKind: &controller, SpecName: &target, Status: sessionapi.StatusRunning},
	}

	match, err := controllerForApply(sessions, "postgres")
	if err != nil {
		t.Fatalf("controllerForApply: %v", err)
	}
	if match == nil || match.SessionID != "sess_active" {
		t.Fatalf("match: got %#v", match)
	}
}

func TestControllerForApplyIgnoresStoppedAndCompletedHistory(t *testing.T) {
	controller := sessionapi.KindController
	target := "postgres"
	sessions := []sessionapi.Session{
		{SessionID: "sess_stopped", SessionKind: &controller, SpecName: &target, Status: sessionapi.StatusStopped},
		{SessionID: "sess_completed", SessionKind: &controller, SpecName: &target, Status: sessionapi.StatusCompleted},
	}

	match, err := controllerForApply(sessions, "postgres")
	if err != nil {
		t.Fatalf("controllerForApply: %v", err)
	}
	if match != nil {
		t.Fatalf("match: got %#v", match)
	}
}

func TestCloudSessionClientsExplicitUnknownEnvReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/environments" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"environments": []map[string]any{}})
	}))
	defer srv.Close()
	configureCloudTest(t, srv.URL)

	clients, err := cloudSessionClients("env_missing")
	if err == nil {
		t.Fatal("expected explicit env lookup to return an error")
	}
	if len(clients) != 0 {
		t.Fatalf("expected no clients, got %d", len(clients))
	}
	if !strings.Contains(err.Error(), "environment env_missing not found") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCloudSessionClientsRecoverEnvironmentAccess(t *testing.T) {
	var recovered bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/environments":
			json.NewEncoder(w).Encode(map[string]any{
				"environments": []map[string]any{{
					"id":                         "env_123",
					"env_handle":                 "env-abc.usetelos.ai",
					"state":                      "ready",
					"has_recoverable_env_access": true,
				}},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/environments/env_123/access":
			recovered = true
			json.NewEncoder(w).Encode(map[string]any{
				"id":           "env_123",
				"env_handle":   "env-abc.usetelos.ai",
				"access_token": "env-token",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	configureCloudTest(t, srv.URL)

	clients, err := cloudSessionClients("")
	if err != nil {
		t.Fatalf("cloudSessionClients: %v", err)
	}
	if len(clients) != 1 {
		t.Fatalf("expected one cloud client, got %d", len(clients))
	}
	if !recovered {
		t.Fatal("expected recoverable environment access to be issued")
	}
	access, ok := config.EnvironmentAccessByID("env_123")
	if !ok {
		t.Fatal("expected recovered access to be saved")
	}
	if access.Token != "env-token" {
		t.Fatalf("saved token: got %q", access.Token)
	}
}

func TestControllerSessionContextUsesScopedToken(t *testing.T) {
	t.Setenv("TELOS_RUNTIME", "")
	t.Setenv("TELOS_API_TOKEN", "session-token")
	t.Setenv("TELOS_SESSION_ID", "sess_parent")
	t.Setenv("TELOS_CLUSTER_API_ENDPOINT", "http://telos-api.local:8000")

	ctx, ok := controllerSessionContext()
	if !ok {
		t.Fatal("expected controller context")
	}
	if ctx.endpoint != "http://telos-api.local:8000" {
		t.Fatalf("endpoint: got %q", ctx.endpoint)
	}
	if ctx.token != "session-token" {
		t.Fatalf("token: got %q", ctx.token)
	}
	if ctx.sessionID != "sess_parent" {
		t.Fatalf("session id: got %q", ctx.sessionID)
	}
}

func TestControllerSessionContextIgnoresLocalRuntime(t *testing.T) {
	t.Setenv("TELOS_API_TOKEN", "session-token")
	t.Setenv("TELOS_SESSION_ID", "sess_parent")
	t.Setenv("TELOS_RUNTIME", string(sessionapi.RuntimeLocal))
	t.Setenv("TELOS_CLUSTER_API_ENDPOINT", "http://telos-api.local:8000")

	if ctx, ok := controllerSessionContext(); ok {
		t.Fatalf("local runtime should not be cloud controller context: %#v", ctx)
	}
}

func TestLocalControllerSessionIDUsesLocalSessionContext(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeLocal)
	markdown := "---\nversion: v0\nname: local-controller\nplatform: local\n---\n# Local Controller\n"
	kind := sessionapi.KindController
	session, err := store.Create(sessionapi.SessionCreateRequest{
		SpecMarkdown: &markdown,
		SessionKind:  &kind,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Setenv("TELOS_SESSION_ID", session.SessionID)
	t.Setenv("TELOS_SESSION_DIR", root)
	t.Setenv("TELOS_RUNTIME", string(sessionapi.RuntimeLocal))

	sessionID, ok := localControllerSessionID()
	if !ok {
		t.Fatal("expected local controller session context")
	}
	if sessionID != session.SessionID {
		t.Fatalf("session id: got %q", sessionID)
	}
}

func TestLocalControllerSessionIDIgnoresTaskSession(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeLocal)
	markdown := "---\nversion: v0\nname: local-task\nplatform: local\n---\n# Local Task\n"
	session, err := store.Create(sessionapi.SessionCreateRequest{SpecMarkdown: &markdown})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Setenv("TELOS_SESSION_ID", session.SessionID)
	t.Setenv("TELOS_SESSION_DIR", root)
	t.Setenv("TELOS_RUNTIME", string(sessionapi.RuntimeLocal))

	if sessionID, ok := localControllerSessionID(); ok {
		t.Fatalf("task session should not be local controller context: %s", sessionID)
	}
}

func TestLocalControllerSessionIDRequiresLocalRuntimeMarker(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeLocal)
	markdown := "---\nversion: v0\nname: local-controller\nplatform: local\n---\n# Local Controller\n"
	kind := sessionapi.KindController
	session, err := store.Create(sessionapi.SessionCreateRequest{
		SpecMarkdown: &markdown,
		SessionKind:  &kind,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Setenv("TELOS_RUNTIME", "")
	t.Setenv("TELOS_SESSION_ID", session.SessionID)
	t.Setenv("TELOS_SESSION_DIR", root)

	if sessionID, ok := localControllerSessionID(); ok {
		t.Fatalf("session should not be local controller context without runtime marker: %s", sessionID)
	}
}

func TestFollowTranscriptWaitsForTranscript(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TELOS_SESSION_DIR", root)
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeLocal)
	markdown := "---\nversion: v0\nname: follow-test\nplatform: local\n---\n# Follow\n"

	session, err := store.Create(sessionapi.SessionCreateRequest{SpecMarkdown: &markdown})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	var out bytes.Buffer
	slept := false
	err = followTranscript(session.SessionID, "", &out, func(time.Duration) {
		if slept {
			t.Fatal("unexpected second sleep")
		}
		slept = true
		path := session.Specs[0].TranscriptPath
		if path == nil || *path == "" {
			t.Fatal("missing transcript path")
		}
		if err := os.MkdirAll(filepath.Dir(*path), 0o755); err != nil {
			t.Fatalf("mkdir transcript dir: %v", err)
		}
		if err := os.WriteFile(*path, []byte("# Transcript\n<progress_update>ready</progress_update>\n"), 0o644); err != nil {
			t.Fatalf("write transcript: %v", err)
		}
		if _, err := store.Stop(session.SessionID); err != nil {
			t.Fatalf("Stop: %v", err)
		}
	}, false)
	if err != nil {
		t.Fatalf("followTranscript: %v", err)
	}
	if !slept {
		t.Fatal("expected follow to wait for transcript creation")
	}
	if got := out.String(); !strings.Contains(got, "ready") {
		t.Fatalf("output: got %q", got)
	}
}

func TestFollowTranscriptErrorsWhenTerminalWithoutTranscript(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TELOS_SESSION_DIR", root)
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeLocal)
	markdown := "---\nversion: v0\nname: missing-transcript\nplatform: local\n---\n# Missing\n"

	session, err := store.Create(sessionapi.SessionCreateRequest{SpecMarkdown: &markdown})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.Stop(session.SessionID); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	var out bytes.Buffer
	err = followTranscript(session.SessionID, "", &out, func(time.Duration) {
		t.Fatal("terminal session should not sleep")
	}, false)
	if err == nil {
		t.Fatal("expected missing terminal transcript to fail")
	}
	if !strings.Contains(err.Error(), "transcript") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFollowTranscriptSurfacesControllerTranscriptError(t *testing.T) {
	t.Setenv("TELOS_RUNTIME", "")
	cluster := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/sessions/sess_running/transcript":
			http.Error(w, `{"detail":"transcript backend failed"}`, http.StatusInternalServerError)
		case "/api/sessions/sess_running":
			json.NewEncoder(w).Encode(map[string]any{
				"session_id": "sess_running",
				"runtime":    "cloud",
				"status":     "running",
				"config":     map[string]any{},
				"provenance": map[string]any{},
				"specs":      []any{},
				"epochs":     []any{},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer cluster.Close()
	t.Setenv("TELOS_SESSION_DIR", filepath.Join(t.TempDir(), "sessions"))
	t.Setenv("TELOS_API_TOKEN", "scoped-token")
	t.Setenv("TELOS_SESSION_ID", "sess_parent")
	t.Setenv("TELOS_CLUSTER_API_ENDPOINT", cluster.URL)

	var out bytes.Buffer
	err := followTranscript("sess_running", "", &out, func(time.Duration) {
		t.Fatal("500 transcript errors should not sleep")
	}, false)
	if err == nil {
		t.Fatal("expected transcript error")
	}
	if !strings.Contains(err.Error(), "controller transcript lookup failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPrintLogsDefaultsToProtocolBlocks(t *testing.T) {
	transcript := `# Transcript

	hidden raw content with inline code ` + "`<progress_update>`" + `

<progress_update>First checkpoint</progress_update>

	more raw content with inline code ` + "`</progress_update>`" + `

	<progress_update ts="2026-05-20T00:00:00Z">Second checkpoint</progress_update>

	<review>
criteria,score
Correctness,8.0/10
</review>

	<summary>Needs one more check.</summary>`

	var out bytes.Buffer
	printLogs(&out, transcript, false)
	text := out.String()
	for _, want := range []string{
		"#1 First checkpoint",
		"#2 Second checkpoint",
		"Review\ncriteria,score\nCorrectness,8.0/10",
		"Summary\nNeeds one more check.",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("log output missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "hidden raw content") || strings.Contains(text, "more raw content") {
		t.Fatalf("log output leaked raw transcript:\n%s", text)
	}
}

func TestPrintLogsRawShowsTranscript(t *testing.T) {
	transcript := "# Transcript\nraw content\n<progress_update>Progress</progress_update>\n"

	var out bytes.Buffer
	printLogs(&out, transcript, true)
	if out.String() != transcript {
		t.Fatalf("raw output mismatch:\n%s", out.String())
	}
}

func TestControllerLookupReturnsClusterAPIError(t *testing.T) {
	t.Setenv("TELOS_RUNTIME", "")
	cluster := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sessions/sess_controller" {
			http.Error(w, `{"detail":"cluster unavailable"}`, http.StatusInternalServerError)
			return
		}
		http.NotFound(w, r)
	}))
	defer cluster.Close()
	t.Setenv("TELOS_SESSION_DIR", filepath.Join(t.TempDir(), "sessions"))
	t.Setenv("TELOS_API_TOKEN", "scoped-token")
	t.Setenv("TELOS_SESSION_ID", "sess_parent")
	t.Setenv("TELOS_CLUSTER_API_ENDPOINT", cluster.URL)

	_, err := getSessionFromAnywhere("sess_controller", "")
	if err == nil {
		t.Fatal("expected controller lookup to fail")
	}
	if !strings.Contains(err.Error(), "controller session lookup failed") {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(err.Error(), "session sess_controller: not found") {
		t.Fatalf("controller error fell through to generic not found: %v", err)
	}
}

func TestLocalSessionNotFoundErrorExplainsWorkspaceScope(t *testing.T) {
	t.Setenv("TELOS_SESSION_DIR", filepath.Join(t.TempDir(), "sessions"))

	_, err := getSessionFromAnywhere("local_missing", "")
	if err == nil {
		t.Fatal("expected missing local session")
	}
	text := err.Error()
	for _, want := range []string{
		"session local_missing not found in",
		"Local sessions are workspace-scoped",
		"TELOS_SESSION_DIR=/path/to/.telos/sessions",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing guidance %q:\n%s", want, text)
		}
	}
}

func TestLocalSessionRootDefaultsToOutputRoot(t *testing.T) {
	t.Setenv("TELOS_SESSION_DIR", "")
	outputRoot := filepath.Join(t.TempDir(), "telos-output")
	t.Setenv("TELOS_OUTPUT_ROOT", outputRoot)

	got := localSessionRoot()
	prefix := outputRoot + string(os.PathSeparator) + "execroot" + string(os.PathSeparator)
	if !strings.HasPrefix(got, prefix) {
		t.Fatalf("local session root %q should be under %q", got, prefix)
	}
	if !strings.HasSuffix(got, string(os.PathSeparator)+"sessions") {
		t.Fatalf("local session root %q should end with sessions", got)
	}
}

func TestLocalSessionRootHonorsSessionDirEnv(t *testing.T) {
	want := filepath.Join(t.TempDir(), "sessions")
	t.Setenv("TELOS_SESSION_DIR", want)
	t.Setenv("TELOS_OUTPUT_ROOT", filepath.Join(t.TempDir(), "telos-output"))

	got := localSessionRoot()
	if got != want {
		t.Fatalf("local session root: got %q want %q", got, want)
	}
}

func configureCloudTest(t *testing.T, endpoint string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TELOS_CONFIG", filepath.Join(dir, "config.yaml"))
	t.Setenv("TELOS_ENVIRONMENTS_CONFIG", filepath.Join(dir, "environments.yaml"))
	t.Setenv("TELOS_API_ENDPOINT", endpoint)
	t.Setenv("TELOS_AUTH_TOKEN", "control-token")
}

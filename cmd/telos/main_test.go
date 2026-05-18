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

	"github.com/telos-org/telos-go/internal/config"
	"github.com/telos-org/telos-go/internal/sessionapi"
)

func TestReorderInterspersedFlags(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.Bool("json", false, "")
	fs.String("workspace", "", "")
	fs.Int("max-rounds", 0, "")

	got := reorderInterspersedFlags(fs, []string{
		"SPEC.md",
		"--json",
		"--workspace",
		"/tmp/ws",
		"--max-rounds=3",
	})
	want := []string{
		"--json",
		"--workspace",
		"/tmp/ws",
		"--max-rounds=3",
		"SPEC.md",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
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
	fs.Int("max-rounds", 20, "")
	fs.String("workspace", "", "")
	parseFlags(fs, []string{"--thinking", "medium", "SPEC.md"})

	if !flagNamesSet(fs, "thinking") {
		t.Fatal("expected explicitly passed --thinking to be detected")
	}
	if flagNamesSet(fs, "max-rounds", "workspace") {
		t.Fatal("defaulted flags should not count as explicitly set")
	}
}

func TestResolveLocalRunConfigUsesEnvironmentDefaults(t *testing.T) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.String("workspace", "", "")
	fs.String("model", "", "")
	fs.String("thinking", "medium", "")
	fs.Int("max-rounds", 20, "")
	fs.Float64("max-cost-usd", 20.0, "")
	fs.Int("agent-timeout-sec", 1800, "")
	parseFlags(fs, []string{"SPEC.md"})

	t.Setenv("TELOS_WORKSPACE", "/tmp/telos-workspace")
	t.Setenv("TELOS_MODEL", "claude-test")
	t.Setenv("TELOS_THINKING", "high")
	t.Setenv("TELOS_MAX_ROUNDS", "9")
	t.Setenv("TELOS_MAX_COST_USD", "12.5")
	t.Setenv("TELOS_AGENT_TIMEOUT_SEC", "123")

	cfg, err := resolveLocalRunConfigFromFlags(fs, "", "", "medium", 20, 20.0, 1800)
	if err != nil {
		t.Fatalf("resolveLocalRunConfigFromFlags: %v", err)
	}
	if cfg.Workspace != "/tmp/telos-workspace" {
		t.Fatalf("workspace: got %q", cfg.Workspace)
	}
	if cfg.Model != "claude-test" || cfg.Thinking != "high" {
		t.Fatalf("model/thinking: got %q/%q", cfg.Model, cfg.Thinking)
	}
	if cfg.MaxRounds != 9 || cfg.AgentTimeoutSec != 123 {
		t.Fatalf("rounds/timeout: got %d/%d", cfg.MaxRounds, cfg.AgentTimeoutSec)
	}
	if cfg.MaxCostUSD == nil || *cfg.MaxCostUSD != 12.5 {
		t.Fatalf("cost: got %v", cfg.MaxCostUSD)
	}
}

func TestResolveLocalRunConfigRejectsInvalidEnvironmentDefaults(t *testing.T) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.Int("max-rounds", 20, "")
	parseFlags(fs, []string{"SPEC.md"})
	t.Setenv("TELOS_MAX_ROUNDS", "not-an-int")

	_, err := resolveLocalRunConfigFromFlags(fs, "", "", "medium", 20, 20.0, 1800)
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
		if err := os.WriteFile(*path, []byte("# Transcript\nready\n"), 0o644); err != nil {
			t.Fatalf("write transcript: %v", err)
		}
		if _, err := store.Stop(session.SessionID); err != nil {
			t.Fatalf("Stop: %v", err)
		}
	})
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
	})
	if err == nil {
		t.Fatal("expected missing terminal transcript to fail")
	}
	if !strings.Contains(err.Error(), "transcript") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFollowTranscriptSurfacesControllerTranscriptError(t *testing.T) {
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
	})
	if err == nil {
		t.Fatal("expected transcript error")
	}
	if !strings.Contains(err.Error(), "controller transcript lookup failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestControllerLookupReturnsClusterAPIError(t *testing.T) {
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

func configureCloudTest(t *testing.T, endpoint string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TELOS_CONFIG", filepath.Join(dir, "config.yaml"))
	t.Setenv("TELOS_ENVIRONMENTS_CONFIG", filepath.Join(dir, "environments.yaml"))
	t.Setenv("TELOS_API_ENDPOINT", endpoint)
	t.Setenv("TELOS_AUTH_TOKEN", "control-token")
}

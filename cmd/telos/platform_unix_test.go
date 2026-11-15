//go:build unix

package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/cli"
	"github.com/telos-org/telos/internal/sessionapi"
)

func TestResolveLocalRunConfigUsesEnvironmentDefaults(t *testing.T) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.String("workspace", "", "")
	fs.String("model", "", "")
	fs.String("thinking", "medium", "")
	fs.Float64("max-cost-usd", 20.0, "")
	parseFlags(fs, []string{"SPEC.md"})

	t.Setenv("TELOS_WORKSPACE", "/tmp/telos-workspace")
	t.Setenv("TELOS_MODEL", "claude-test")
	t.Setenv("TELOS_THINKING", "high")
	t.Setenv("TELOS_MAX_COST_USD", "12.5")

	cfg, err := resolveLocalRunConfigFromFlags(fs, "", "", "medium", 20.0)
	if err != nil {
		t.Fatalf("resolveLocalRunConfigFromFlags: %v", err)
	}
	if cfg.Workspace != "/tmp/telos-workspace" {
		t.Fatalf("workspace: got %q", cfg.Workspace)
	}
	if cfg.Model != "claude-test" || cfg.Thinking != "high" {
		t.Fatalf("model/thinking: got %q/%q", cfg.Model, cfg.Thinking)
	}
	if cfg.MaxCostUSD == nil || *cfg.MaxCostUSD != 12.5 {
		t.Fatalf("cost: got %v", cfg.MaxCostUSD)
	}
}

func TestResolveLocalRunConfigUsesDefaultThinking(t *testing.T) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.String("thinking", "", "")
	fs.Float64("max-cost-usd", 20.0, "")
	parseFlags(fs, []string{"SPEC.md"})

	cfg, err := resolveLocalRunConfigFromFlags(fs, "", "", "", 20.0)
	if err != nil {
		t.Fatalf("resolveLocalRunConfigFromFlags: %v", err)
	}
	if cfg.Thinking != cli.DefaultLocalThinking {
		t.Fatalf("thinking: got %q, want %q", cfg.Thinking, cli.DefaultLocalThinking)
	}
}

func TestLocalStoreProjectsSpecUpdates(t *testing.T) {
	root := t.TempDir()
	configureLocalOnlyTest(t)
	t.Setenv("TELOS_SESSION_DIR", root)
	s := store()
	markdown := "---\nversion: 0.1.0\nname: local-update\nplatform: local\n---\n# Local Update\n"
	kind := sessionapi.KindController
	session, err := s.Create(sessionapi.SessionCreateRequest{SpecMarkdown: &markdown, SessionKind: &kind})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	updated := "---\nversion: 0.1.1\nname: local-update\nplatform: local\ninterval: 6h\n---\n# Local Update v2\n"

	if _, err := s.UpdateSpecByID(session.SessionID, sessionapi.SessionSpecUpdateRequest{SpecMarkdown: updated}); err != nil {
		t.Fatalf("UpdateSpecByID: %v", err)
	}

	transcript, err := s.Transcript(session.SessionID)
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	for _, want := range []string{
		"## External Update",
		"<external_update>",
		"from version 1 to 2",
		"Current immutable spec path: `",
		"Active spec path: `",
	} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("transcript missing %q:\n%s", want, transcript)
		}
	}
	events, err := s.Events(session.SessionID)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var found bool
	for _, event := range events {
		if event.Event == "external_update" {
			found = true
			if got := event.Data["current_spec_version"]; got != float64(2) {
				t.Fatalf("current_spec_version: got %#v", got)
			}
		}
	}
	if !found {
		t.Fatalf("missing external_update event: %#v", events)
	}
}

func TestLocalStoreUpdatesCompletedController(t *testing.T) {
	root := t.TempDir()
	configureLocalOnlyTest(t)
	t.Setenv("TELOS_SESSION_DIR", root)
	s := store()
	markdown := "---\nversion: 0.1.0\nname: completed-update\nplatform: local\n---\n# Completed Update\n"
	kind := sessionapi.KindController
	session, err := s.Create(sessionapi.SessionCreateRequest{SpecMarkdown: &markdown, SessionKind: &kind})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	finished := "2026-07-08T00:00:00Z"
	completed := "completed"
	if _, err := sessionapi.MutateManifest(filepath.Join(root, session.SessionID, "session.json"), func(m *sessionapi.Manifest) error {
		m.Runner = nil
		m.Epochs = []sessionapi.Epoch{{
			ID:         1,
			StartedAt:  "2026-07-08T00:00:00Z",
			FinishedAt: &finished,
			Result:     &completed,
		}}
		return nil
	}); err != nil {
		t.Fatalf("MutateManifest: %v", err)
	}
	current, err := s.Get(session.SessionID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if current.Status != sessionapi.StatusCompleted {
		t.Fatalf("status: got %s", current.Status)
	}
	updated := "---\nversion: 0.1.1\nname: completed-update\nplatform: local\ninterval: 6h\n---\n# Completed Update v2\n"
	response, err := s.UpdateSpecByID(session.SessionID, sessionapi.SessionSpecUpdateRequest{SpecMarkdown: updated})
	if err != nil {
		t.Fatalf("UpdateSpecByID: %v", err)
	}
	if response.Operation != "updated" {
		t.Fatalf("operation: got %q", response.Operation)
	}
}

func TestLegacyTranscriptFallbackUsesLocalTranscriptOnlySession(t *testing.T) {
	root := t.TempDir()
	configureLocalOnlyTest(t)
	t.Setenv("TELOS_SESSION_DIR", root)
	s := store()
	markdown := "---\nversion: 0.1.0\nname: legacy-logs\nplatform: local\n---\n# Legacy Logs\n"
	session, err := s.Create(sessionapi.SessionCreateRequest{SpecMarkdown: &markdown})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(session.Specs) == 0 || session.Specs[0].TranscriptPath == nil {
		t.Fatalf("missing transcript path: %#v", session.Specs)
	}
	transcript := "# Transcript\n\n<progress_update>Recovered legacy activity</progress_update>\n"
	if err := os.WriteFile(*session.Specs[0].TranscriptPath, []byte(transcript), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	events, eventsErr := getEventsFromAnywhere(session.SessionID)
	got, ok := legacyTranscriptFallback(session.SessionID, events, eventsErr)
	if !ok || got != transcript {
		t.Fatalf("fallback: ok=%v, got %q, events=%#v, err=%v", ok, got, events, eventsErr)
	}
}

func TestPrintLocalLaunchIncludesWorkspaceScopedCommands(t *testing.T) {
	session := &cli.LocalSession{
		SessionID:      "local_123",
		SpecName:       "blackbox",
		WorkspaceScope: "/tmp/telos-blackbox",
	}

	var out bytes.Buffer
	printLocalLaunch(&out, "submitted", session)
	text := out.String()
	for _, want := range []string{
		"submitted blackbox",
		"Name      blackbox",
		"Target    local",
		"Status    active",
		"Cost      -",
		"Goal      local_123",
		"Workspace /tmp/telos-blackbox",
		"Describe  cd '/tmp/telos-blackbox' && telos describe local_123",
		"Logs      cd '/tmp/telos-blackbox' && telos logs local_123",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("launch output missing %q:\n%s", want, text)
		}
	}
}

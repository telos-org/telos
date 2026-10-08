package executor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/telos-org/telos/internal/game"
	"github.com/telos-org/telos/internal/platform"
)

// This suite uses a test-only provider extension, so the real Pi executable,
// agent loop, tool execution, and RPC controls run without any network access.
func TestPiOfflineOrdinaryAndSwitching(t *testing.T) {
	for _, tc := range []struct{ name, binaryEnv, phase string }{
		{"current_ordinary", "TELOS_TEST_PI_BINARY", "ordinary"},
		{"legacy_ordinary", "TELOS_TEST_LEGACY_PI_BINARY", "legacy"},
		{"switch_during_request", "TELOS_TEST_PI_BINARY", "request"},
		{"switch_during_tool", "TELOS_TEST_PI_BINARY", "tool"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binary := os.Getenv(tc.binaryEnv)
			if binary == "" {
				t.Skip("set " + tc.binaryEnv + " to test real Pi")
			}
			directory := t.TempDir()
			home := t.TempDir()
			agentDir := filepath.Join(home, "agent")
			bin := filepath.Join(home, ".local", "bin")
			for _, dir := range []string{agentDir, bin} {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			extension, err := filepath.Abs("testdata/pi_offline_provider.js")
			if err != nil {
				t.Fatal(err)
			}
			settingsPath := filepath.Join(agentDir, "settings.json")
			if err := os.WriteFile(settingsPath, []byte(`{"compaction":{"enabled":false},"retry":{"enabled":false}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			script := "#!/bin/sh\nexec " + shellQuote(binary) + " --offline --no-extensions --no-skills --no-prompt-templates --no-themes --no-context-files --tools bash -e " + shellQuote(extension) + " \"$@\"\n"
			if err := os.WriteFile(filepath.Join(bin, "pi"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			p := platform.NewLocalPlatform(directory)
			p.Env = map[string]string{"HOME": home, "PI_CODING_AGENT_DIR": agentDir, "PI_TELEMETRY": "0", "TELOS_PI_PROBE_DIR": directory, "TELOS_PI_PROBE_PHASE": tc.phase}
			pe := NewPiExecutor(p, "rpc-a/probe-a", "medium", 20)
			var stopped atomic.Bool
			defer stopped.Store(true)
			state := &game.TurnState{Dir: directory, StopRequested: stopped.Load}
			resultCh := make(chan game.TurnResult, 1)
			go func() { resultCh <- pe.ExecuteTurn("Run the tool and report success.", "prover", state) }()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			waitFile := func(name string) []byte {
				for {
					if data, err := os.ReadFile(filepath.Join(directory, name)); err == nil {
						return data
					}
					select {
					case result := <-resultCh:
						t.Fatalf("Pi exited while waiting for %s: %+v", name, result)
					case <-ctx.Done():
						t.Fatalf("timed out waiting for %s", name)
					case <-time.After(10 * time.Millisecond):
					}
				}
			}
			type request struct {
				PID             int
				Model, Thinking string
				Messages        []json.RawMessage
			}
			var first request
			if err := json.Unmarshal(waitFile("request-1.json"), &first); err != nil {
				t.Fatal(err)
			}
			if first.Model != "probe-a" || first.Thinking != "medium" {
				t.Fatalf("initial settings: %+v", first)
			}
			if tc.phase == "tool" {
				waitFile("tool-started")
			}
			// Older Pi already saves CLI --thinking during startup. Compare after
			// startup so this assertion isolates the live-controls contract.
			before, err := os.ReadFile(settingsPath)
			if err != nil {
				t.Fatal(err)
			}
			wantModel, wantThinking := "probe-a", "medium"
			if tc.phase == "legacy" {
				if _, err := pe.SetModel(ctx, "rpc-b", "probe-b"); !errors.Is(err, ErrPiLiveSettingsUnsupported) {
					t.Fatalf("legacy model control: %v", err)
				}
				if _, err := pe.SetThinkingLevel(ctx, "high"); !errors.Is(err, ErrPiLiveSettingsUnsupported) {
					t.Fatalf("legacy thinking control: %v", err)
				}
			} else if tc.phase != "ordinary" {
				if _, err := pe.SetModel(ctx, "rpc-b", "probe-b"); err != nil {
					t.Fatal(err)
				}
				settings, err := pe.SetThinkingLevel(ctx, "high")
				if err != nil || settings != (PiSettings{"rpc-b", "probe-b", "high"}) {
					t.Fatalf("settings: %+v, %v", settings, err)
				}
				wantModel, wantThinking = "probe-b", "high"
			}
			if _, err := os.Stat(filepath.Join(directory, "request-2.json")); !os.IsNotExist(err) {
				t.Fatal("next request started before current work was released")
			}
			for _, name := range []string{"request-release", "tool-release"} {
				if err := os.WriteFile(filepath.Join(directory, name), []byte("release"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var result game.TurnResult
			select {
			case result = <-resultCh:
			case <-ctx.Done():
				t.Fatal("Pi did not settle")
			}
			if result.Error != "" || result.Status != game.StatusConcede || result.Stats.NumTurns != 1 || result.Stats.Model != wantModel {
				t.Fatalf("result: %+v", result)
			}
			data, err := os.ReadFile(filepath.Join(directory, "request-2.json"))
			if err != nil {
				t.Fatal(err)
			}
			var second request
			if err := json.Unmarshal(data, &second); err != nil {
				t.Fatal(err)
			}
			if second.PID != first.PID || second.Model != wantModel || second.Thinking != wantThinking {
				t.Fatalf("next request: %+v", second)
			}
			toolResults := 0
			for _, message := range second.Messages {
				var msg struct{ Role string }
				if err := json.Unmarshal(message, &msg); err != nil {
					t.Fatal(err)
				}
				if msg.Role == "toolResult" {
					toolResults++
					if !strings.Contains(string(message), "preserved tool result") {
						t.Fatalf("lost tool result: %s", message)
					}
				}
			}
			if toolResults != 1 {
				t.Fatalf("tool results: %d", toolResults)
			}
			after, err := os.ReadFile(settingsPath)
			if err != nil || string(after) != string(before) {
				t.Fatalf("live controls changed saved defaults: before=%s after=%s err=%v", before, after, err)
			}
			if _, err := os.Stat(state.PiSessionPath()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

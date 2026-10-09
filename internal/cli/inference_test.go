package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/telos-org/telos/internal/executor"
	"github.com/telos-org/telos/internal/game"
	"github.com/telos-org/telos/internal/platform"
	"github.com/telos-org/telos/internal/sessionapi"
)

func writeInferenceManifest(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "session.json")
	if err := sessionapi.WriteManifest(path, &sessionapi.Manifest{
		SessionID: "session", SessionKind: sessionapi.KindController,
		Config: sessionapi.SessionConfig{Model: "turn-a/probe-a", Thinking: "medium"},
	}); err != nil {
		t.Fatal(err)
	}
	return path
}

func changeInference(t *testing.T, path string, definition json.RawMessage) {
	t.Helper()
	if _, err := sessionapi.MutateManifest(path, func(m *sessionapi.Manifest) error {
		m.Config.Model, m.Config.Thinking, m.Config.InferenceGeneration = "turn-b/probe-b", "high", 1
		m.InferenceModelDefinition = definition
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// fakePiTurns runs turns for the session in dir against a Pi stub and returns
// each turn's argv.
func fakePiTurns(t *testing.T, dir string) func() string {
	t.Helper()
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$HOME/argv.txt\"\nprintf 'done\\n<status>CONCEDE</status>\\n'\n"
	if err := os.WriteFile(filepath.Join(bin, "pi"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	p := platform.NewLocalPlatform(dir)
	p.Env = map[string]string{"HOME": home}
	e := &sessionInferenceExecutor{PiExecutor: executor.NewPiExecutor(p, "turn-a/probe-a", "medium", 5), sessionDir: dir}
	return func() string {
		t.Helper()
		if result := e.ExecuteTurn("do it", "prover", nil); result.Status != game.StatusConcede {
			t.Fatalf("turn: %+v", result)
		}
		argv, _ := os.ReadFile(filepath.Join(home, "argv.txt"))
		return string(argv)
	}
}

func TestEachTurnReadsCurrentInferenceSettings(t *testing.T) {
	dir := t.TempDir()
	path := writeInferenceManifest(t, dir)
	turn := fakePiTurns(t, dir)
	if argv := turn(); !strings.HasPrefix(argv, "--mode\ntext\n--model\nturn-a/probe-a\n--thinking\nmedium\n") {
		t.Fatalf("first turn argv:\n%s", argv)
	}
	changeInference(t, path, json.RawMessage(`{"id":"probe-b"}`))
	if argv := turn(); !strings.HasPrefix(argv, "-e\n") || !strings.Contains(argv, "--model\nturn-b/probe-b\n--thinking\nhigh\n") {
		t.Fatalf("next turn did not read the changed settings:\n%s", argv)
	}
}

func TestChildTurnFollowsNewerRootSettings(t *testing.T) {
	sessions := t.TempDir()
	rootPath := writeInferenceManifest(t, filepath.Join(sessions, "root"))
	parent := "root"
	childPath := filepath.Join(sessions, "child", "session.json")
	if err := sessionapi.WriteManifest(childPath, &sessionapi.Manifest{
		SessionID: "child", SessionKind: sessionapi.KindTask, ParentSessionID: &parent,
		Config: sessionapi.SessionConfig{Model: "turn-a/explicit", Thinking: "low"},
	}); err != nil {
		t.Fatal(err)
	}
	turn := fakePiTurns(t, filepath.Dir(childPath))
	if argv := turn(); !strings.Contains(argv, "--model\nturn-a/explicit\n--thinking\nlow\n") {
		t.Fatalf("child lost its own settings before a root change:\n%s", argv)
	}
	changeInference(t, rootPath, json.RawMessage(`{"id":"probe-b"}`))
	if argv := turn(); !strings.HasPrefix(argv, "-e\n") || !strings.Contains(argv, "--model\nturn-b/probe-b\n--thinking\nhigh\n") {
		t.Fatalf("child did not follow the root's newer settings:\n%s", argv)
	}
	m, err := sessionapi.ReadManifest(childPath)
	if err != nil || m.Config.InferenceGeneration != 1 || m.Config.Model != "turn-b/probe-b" || !strings.Contains(string(m.InferenceModelDefinition), `"probe-b"`) {
		t.Fatalf("child manifest = %+v %s %v", m.Config, m.InferenceModelDefinition, err)
	}
}

func TestChildTurnFollowsRootConnectionSwitch(t *testing.T) {
	sessions := t.TempDir()
	rootPath := writeInferenceManifest(t, filepath.Join(sessions, "root"))
	parent := "root"
	childPath := filepath.Join(sessions, "child", "session.json")
	old := &sessionapi.InferenceConnection{ID: "old", Provider: "turn-a", BaseURL: "https://old.example", ProxyURL: "http://172.31.255.1:18080", APIKey: "telos-proxy-old"}
	if err := sessionapi.WriteManifest(childPath, &sessionapi.Manifest{
		SessionID: "child", SessionKind: sessionapi.KindTask, ParentSessionID: &parent,
		Config:              sessionapi.SessionConfig{Model: "turn-a/probe-a", Thinking: "medium"},
		InferenceConnection: old,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := sessionapi.MutateManifest(rootPath, func(m *sessionapi.Manifest) error {
		m.Config.Model, m.Config.Thinking, m.Config.InferenceGeneration = "turn-b/probe-b", "high", 1
		m.InferenceModelDefinition = json.RawMessage(`{"id":"probe-b"}`)
		m.InferenceConnection = &sessionapi.InferenceConnection{ID: "new", Provider: "turn-b", BaseURL: "https://new.example", ProxyURL: old.ProxyURL, APIKey: "telos-proxy-new"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if argv := fakePiTurns(t, filepath.Dir(childPath))(); !strings.Contains(argv, "--model\nturn-b/probe-b\n") {
		t.Fatalf("child did not switch with the root:\n%s", argv)
	}
	m, err := sessionapi.ReadManifest(childPath)
	if err != nil || m.InferenceConnection == nil || m.InferenceConnection.ID != "new" || m.InferenceConnection.APIKey != "telos-proxy-new" {
		t.Fatalf("child connection = %+v %v", m.InferenceConnection, err)
	}
}

func TestTurnFailureAfterSettingsChangeIsRetryable(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	path := writeInferenceManifest(t, dir)
	next := filepath.Join(home, "next.json")
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	// Cloud switches the connection while this turn runs; the old key is gone.
	script := "#!/bin/sh\n[ -f " + next + " ] && mv " + next + " " + path + "\necho '401 Unauthorized: invalid api key' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "pi"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	p := platform.NewLocalPlatform(dir)
	p.Env = map[string]string{"HOME": home}
	e := &sessionInferenceExecutor{PiExecutor: executor.NewPiExecutor(p, "turn-a/probe-a", "medium", 5), sessionDir: dir}
	if result := e.ExecuteTurn("do it", "prover", nil); result.Recoverable || !strings.Contains(result.Error, "invalid api key") {
		t.Fatalf("an invalid key without a settings change must block: %+v", result)
	}
	m, err := sessionapi.ReadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	m.Config.InferenceGeneration = 1
	if err := sessionapi.WriteManifest(next, m); err != nil {
		t.Fatal(err)
	}
	result := e.ExecuteTurn("do it", "prover", nil)
	if _, blocked := game.AgentFailureBlocker(result.Error); blocked || !result.Recoverable {
		t.Fatalf("a failure after a settings change should retry: %+v", result)
	}
}

func TestInferenceRealPiNextTurn(t *testing.T) {
	for _, binaryEnv := range []string{"TELOS_TEST_PI_BINARY", "TELOS_TEST_LEGACY_PI_BINARY"} {
		t.Run(binaryEnv, func(t *testing.T) {
			binary := os.Getenv(binaryEnv)
			if binary == "" {
				if os.Getenv("CI") == "true" {
					t.Fatal("required integration test binary is missing: " + binaryEnv)
				}
				t.Skip("set " + binaryEnv + " to run real Pi")
			}
			root, home := t.TempDir(), t.TempDir()
			dir, agent, bin := filepath.Join(root, "session"), filepath.Join(home, "agent"), filepath.Join(home, ".local", "bin")
			for _, folder := range []string{dir, agent, bin} {
				if err := os.MkdirAll(folder, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			extension, _ := filepath.Abs("../executor/testdata/pi_turn_provider.js")
			quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
			script := "#!/bin/sh\nexec " + quote(binary) + " --offline --no-extensions --no-skills --no-prompt-templates --no-themes --no-context-files --tools bash -e " + quote(extension) + " \"$@\"\n"
			write := func(path, content string, mode os.FileMode) {
				t.Helper()
				if err := os.WriteFile(path, []byte(content), mode); err != nil {
					t.Fatal(err)
				}
			}
			write(filepath.Join(bin, "pi"), script, 0o755)
			write(filepath.Join(agent, "settings.json"), `{"compaction":{"enabled":false},"retry":{"enabled":false}}`, 0o600)
			write(filepath.Join(agent, "models.json"), `{"providers":{"turn-b":{"api":"telos-offline-test","baseUrl":"https://unused.invalid","apiKey":"test-only"}}}`, 0o600)
			path := writeInferenceManifest(t, dir)
			p := platform.NewLocalPlatform(dir)
			p.Env = map[string]string{"HOME": home, "PI_CODING_AGENT_DIR": agent, "PI_TELEMETRY": "0"}
			e := &sessionInferenceExecutor{PiExecutor: executor.NewPiExecutor(p, "turn-a/probe-a", "medium", 15), sessionDir: dir}
			results := make(chan game.TurnResult, 1)
			start := func(role string) string {
				turnDir := filepath.Join(dir, role)
				if err := os.MkdirAll(turnDir, 0o755); err != nil {
					t.Fatal(err)
				}
				p.Env["TELOS_PI_PROBE_DIR"] = turnDir
				go func() {
					results <- e.ExecuteTurn("Run the tool and report success.", role, &game.TurnState{Dir: turnDir})
				}()
				return turnDir
			}
			waitFile := func(path string) []byte {
				t.Helper()
				deadline := time.After(12 * time.Second)
				for {
					if data, err := os.ReadFile(path); err == nil {
						return data
					}
					select {
					case result := <-results:
						t.Fatalf("Pi ended before %s: %+v", path, result)
					case <-deadline:
						t.Fatalf("timed out: %s", path)
					case <-time.After(10 * time.Millisecond):
					}
				}
			}
			finish := func(turnDir string) {
				t.Helper()
				write(filepath.Join(turnDir, "request-release"), "", 0o600)
				write(filepath.Join(turnDir, "tool-release"), "", 0o600)
				select {
				case result := <-results:
					if result.Error != "" || result.Status != game.StatusConcede {
						t.Fatalf("turn: %+v", result)
					}
				case <-time.After(12 * time.Second):
					t.Fatal("turn did not finish")
				}
			}
			prover := start("prover")
			waitFile(filepath.Join(prover, "request-1.json"))
			changeInference(t, path, json.RawMessage(`{"id":"probe-b","name":"Offline test","api":"telos-offline-test","reasoning":true,"input":["text"],"contextWindow":256000,"maxTokens":8192,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0}}`))
			finish(prover)
			running, err := os.ReadFile(filepath.Join(prover, "request-2.json"))
			if err != nil || !bytes.Contains(running, []byte(`"model":"probe-a"`)) || !bytes.Contains(running, []byte(`"thinking":"medium"`)) {
				t.Fatalf("running turn changed settings: %s %v", running, err)
			}
			verifier := start("verifier")
			next := waitFile(filepath.Join(verifier, "request-1.json"))
			finish(verifier)
			var got struct {
				Model, Thinking, API     string
				ContextWindow, MaxTokens int
			}
			if err := json.Unmarshal(next, &got); err != nil {
				t.Fatal(err)
			}
			if got.Model != "probe-b" || got.Thinking != "high" || got.API != "telos-offline-test" || got.ContextWindow != 256000 || got.MaxTokens != 8192 {
				t.Fatalf("next turn did not use the new settings and definition: %s", next)
			}
		})
	}
}

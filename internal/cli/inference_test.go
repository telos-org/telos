package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/telos-org/telos/internal/executor"
	"github.com/telos-org/telos/internal/game"
	"github.com/telos-org/telos/internal/platform"
	"github.com/telos-org/telos/internal/sessionapi"
)

func TestInferenceRealPiNextTurn(t *testing.T) {
	for _, phase := range []string{"request", "tool", "follow_up", "empty_defaults", "model_only", "thinking_only", "invalid_thinking", "rejected_from_clamped", "clamped_thinking", "unknown_model", "definition", "bad_definition", "legacy", "legacy_definition", "connection", "connection_same_model", "connection_rejected", "connection_follow_up", "connection_return", "legacy_connection", "downgraded_connection"} {
		t.Run(phase, func(t *testing.T) {
			connectionPhase := strings.Contains(phase, "connection")
			binaryEnv := "TELOS_TEST_PI_BINARY"
			if strings.HasPrefix(phase, "legacy") {
				binaryEnv = "TELOS_TEST_LEGACY_PI_BINARY"
			}
			binary := os.Getenv(binaryEnv)
			if binary == "" {
				if os.Getenv("CI") != "" {
					t.Fatal("CI must provide " + binaryEnv + " for real Pi turn tests")
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
			t.Setenv("HOME", home)
			write(filepath.Join(agent, "settings.json"), `{"compaction":{"enabled":false},"retry":{"enabled":false}}`, 0o600)
			write(filepath.Join(agent, "models.json"), `{"providers":{"turn-b":{"api":"telos-offline-test","baseUrl":"https://unused.invalid","apiKey":"test-only"}}}`, 0o600)
			manifest := &sessionapi.Manifest{SessionID: "session", SessionKind: sessionapi.KindController, Config: sessionapi.SessionConfig{Model: "turn-a/probe-a", Thinking: "medium"}}
			initialThinking, initialActualThinking := "medium", "medium"
			if phase == "rejected_from_clamped" {
				initialThinking, initialActualThinking = "xhigh", "high"
				manifest.Config.Thinking = initialThinking
			}
			if phase == "empty_defaults" {
				manifest.Config.Model, manifest.Config.Thinking = "", ""
			}
			manifestFile := filepath.Join(dir, "session.json")
			if err := sessionapi.WriteManifest(manifestFile, manifest); err != nil {
				t.Fatal(err)
			}
			store := sessionapi.NewFileStore(root, sessionapi.RuntimeLocal)
			p := platform.NewLocalPlatform(dir)
			p.Env = map[string]string{"HOME": home, "PI_CODING_AGENT_DIR": agent, "PI_TELEMETRY": "0", "TELOS_PI_PROBE_PHASE": phase}
			if connectionPhase {
				p.Env["HTTPS_PROXY"] = "http://general-proxy.invalid:18080"
			}
			e := &sessionInferenceExecutor{sessionDir: dir, pi: executor.NewPiExecutor(p, "turn-a/probe-a", initialThinking, 15)}
			var stop atomic.Bool
			defer stop.Store(true)
			results := make(chan game.TurnResult, 1)
			start := func(role string) string {
				t.Helper()
				turnDir := filepath.Join(dir, role)
				if err := os.MkdirAll(turnDir, 0o755); err != nil {
					t.Fatal(err)
				}
				p.Env["TELOS_PI_PROBE_DIR"] = turnDir
				go func() {
					results <- e.ExecuteTurn("Run the tool and report success.", role, &game.TurnState{Dir: turnDir, StopRequested: stop.Load})
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
				for _, file := range []string{"request-release", "tool-release"} {
					write(filepath.Join(turnDir, file), "", 0o600)
				}
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
			first := waitFile(filepath.Join(prover, "request-1.json"))
			if phase == "tool" {
				waitFile(filepath.Join(prover, "tool-started"))
			}
			model, thinking := "turn-b/probe-b", "high"
			request := sessionapi.InferenceUpdateRequest{RequestID: "change", Model: &model, Thinking: &thinking}
			if phase == "model_only" {
				request.Thinking = nil
				thinking = "medium"
			}
			if phase == "thinking_only" || phase == "empty_defaults" {
				request.Model = nil
				model = "turn-a/probe-a"
			}
			rejected := phase == "rejected_from_clamped" || phase == "invalid_thinking" || phase == "clamped_thinking" || phase == "unknown_model" || phase == "bad_definition" || phase == "connection_rejected" || phase == "downgraded_connection"
			if phase == "invalid_thinking" || phase == "rejected_from_clamped" {
				thinking = "imaginary"
			}
			if phase == "clamped_thinking" {
				thinking = "xhigh"
			}
			if phase == "unknown_model" {
				model = "turn-b/unknown"
			}
			if strings.Contains(phase, "definition") {
				request.ModelDefinition = json.RawMessage(`{"id":"probe-b","name":"Offline test","api":"telos-offline-test","reasoning":true,"input":["text"],"contextWindow":256000,"maxTokens":8192,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0}}`)
				if phase == "bad_definition" {
					request.ModelDefinition = json.RawMessage(`{"id":"probe-b","contextWindow":"invalid"}`)
				}
			}
			if connectionPhase {
				if phase == "connection_same_model" {
					model = "turn-a/probe-a"
				}
				provider, id, _ := strings.Cut(model, "/")
				request.Connection = &sessionapi.InferenceConnection{ID: "new-connection", Provider: provider, BaseURL: "https://new-account.invalid/v1", ProxyURL: "http://172.31.255.1:20004", APIKey: "telos-proxy-" + strings.Repeat("a", 43)}
				request.ModelDefinition = json.RawMessage(fmt.Sprintf(`{"id":%q,"name":"Offline test","api":"telos-offline-test","reasoning":true,"input":["text"],"contextWindow":256000,"maxTokens":8192,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0}}`, id))
				if phase == "connection_rejected" {
					thinking = "imaginary"
				}
			}
			queued, err := store.UpdateInference("session", request)
			if phase == "legacy_connection" {
				if err == nil || !strings.Contains(err.Error(), "compiled Pi 1.0.4") {
					t.Fatalf("legacy connection request was not rejected immediately: %+v %v", queued, err)
				}
				finish(prover)
				state, err := store.Inference("session")
				if err != nil || state.ConnectionSwitching == nil || *state.ConnectionSwitching || state.Update != nil || state.Revision != 0 {
					t.Fatalf("legacy request changed saved state: %+v %v", state, err)
				}
				return
			}
			if err != nil || queued.Update.Status != "pending" || queued.Settings.Model != manifest.Config.Model {
				t.Fatalf("queue: %+v %v", queued, err)
			}
			finish(prover)
			oldRequest, err := os.ReadFile(filepath.Join(prover, "request-2.json"))
			if err != nil || !bytes.Contains(oldRequest, []byte(`"model":"probe-a"`)) || !bytes.Contains(oldRequest, []byte(fmt.Sprintf(`"thinking":%q`, initialActualThinking))) || !bytes.Contains(oldRequest, []byte("preserved tool result")) {
				t.Fatalf("running turn changed: %s %v", oldRequest, err)
			}
			pending, _ := store.Inference("session")
			if pending.Update.Status != "pending" {
				t.Fatalf("applied before next turn: %+v", pending)
			}
			if phase == "downgraded_connection" {
				legacy := os.Getenv("TELOS_TEST_LEGACY_PI_BINARY")
				if legacy == "" {
					if os.Getenv("CI") != "" {
						t.Fatal("CI must provide TELOS_TEST_LEGACY_PI_BINARY for downgrade tests")
					}
					t.Skip("set TELOS_TEST_LEGACY_PI_BINARY to test a downgrade after acceptance")
				}
				write(filepath.Join(bin, "pi"), strings.Replace(script, quote(binary), quote(legacy), 1), 0o755)
			}
			verifier := start("verifier")
			second := waitFile(filepath.Join(verifier, "request-1.json"))
			if phase == "follow_up" || phase == "connection_follow_up" || phase == "connection_return" {
				deadline := time.Now().Add(time.Second)
				for {
					state, err := store.Inference("session")
					if err != nil {
						t.Fatal(err)
					}
					if state.Update.Status == "applied" {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("startup not confirmed")
					}
					time.Sleep(10 * time.Millisecond)
				}
				level := "low"
				followUp := sessionapi.InferenceUpdateRequest{RequestID: "follow-up", ExpectedRevision: 1, Thinking: &level}
				if phase == "connection_return" {
					originalModel := "turn-a/probe-a"
					followUp.Model = &originalModel
					followUp.ModelDefinition = json.RawMessage(`{"id":"probe-a","name":"Offline test","api":"telos-offline-test","reasoning":true,"input":["text"],"contextWindow":128000,"maxTokens":4096,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0}}`)
					followUp.Connection = &sessionapi.InferenceConnection{ID: "original-connection", Provider: "turn-a", BaseURL: "https://original-account.invalid/v1", ProxyURL: "http://172.31.255.1:20000", APIKey: "telos-proxy-" + strings.Repeat("b", 43)}
				}
				if _, err := store.UpdateInference("session", followUp); err != nil {
					t.Fatal(err)
				}
			}
			finish(verifier)
			state, err := store.Inference("session")
			if err != nil {
				t.Fatal(err)
			}
			wantStatus := "applied"
			if phase == "follow_up" || phase == "connection_follow_up" || phase == "connection_return" {
				wantStatus = "pending"
			}
			if rejected {
				wantStatus, model, thinking = "rejected", "turn-a/probe-a", initialThinking
			}
			if state.Update.Status != wantStatus || state.Settings.Model != model || state.Settings.Thinking != thinking {
				t.Fatalf("next turn settings: %+v update=%+v", state, state.Update)
			}
			if phase == "rejected_from_clamped" {
				thinking = initialActualThinking
			}
			var before, after struct {
				PID                      int
				Model, Thinking, API     string
				ContextWindow, MaxTokens int
			}
			if json.Unmarshal(first, &before) != nil || json.Unmarshal(second, &after) != nil {
				t.Fatal("invalid probe")
			}
			_, wantModel, _ := strings.Cut(model, "/")
			if before.PID == after.PID || after.Model != wantModel || after.Thinking != thinking {
				t.Fatalf("new process did not use complete pair: %s", second)
			}
			if connectionPhase && !rejected {
				for _, value := range []string{`"baseUrl":"https://new-account.invalid/v1"`, `"apiKey":"` + request.Connection.APIKey + `"`, `"proxy":"http://172.31.255.1:20004"`, `"processProxy":"http://general-proxy.invalid:18080"`} {
					if !bytes.Contains(second, []byte(value)) {
						t.Fatalf("new connection not used (%s): %s", value, second)
					}
				}
				if state.Settings.ConnectionID != request.Connection.ID {
					t.Fatalf("connection was not confirmed: %+v", state.Settings)
				}
			}
			if phase == "definition" || phase == "legacy_definition" {
				if after.ContextWindow != 256000 || after.MaxTokens != 8192 || after.API != "telos-offline-test" {
					t.Fatalf("metadata was not used: %s", second)
				}
			}
			// A new executor restores confirmed settings or activates the next queued pair.
			if phase == "follow_up" || phase == "connection_follow_up" || phase == "connection_return" {
				thinking = "low"
			}
			if phase == "connection_return" {
				wantModel = "probe-a"
			}
			e = &sessionInferenceExecutor{sessionDir: dir, pi: executor.NewPiExecutor(p, "turn-a/probe-a", initialThinking, 15)}
			restart := start("restart")
			restarted := waitFile(filepath.Join(restart, "request-1.json"))
			finish(restart)
			if !bytes.Contains(restarted, []byte(fmt.Sprintf(`"model":%q`, wantModel))) || !bytes.Contains(restarted, []byte(fmt.Sprintf(`"thinking":%q`, thinking))) {
				t.Fatalf("restart lost settings: %s", restarted)
			}
			if connectionPhase && !rejected {
				wantKey := request.Connection.APIKey
				if phase == "connection_return" {
					wantKey = "telos-proxy-" + strings.Repeat("b", 43)
				}
				if !bytes.Contains(restarted, []byte(fmt.Sprintf(`"apiKey":%q`, wantKey))) {
					t.Fatalf("restart lost connection: %s", restarted)
				}
				models, _ := os.ReadFile(filepath.Join(agent, "models.json"))
				if bytes.Contains(models, []byte("telos-proxy")) || bytes.Contains(models, []byte("new-account")) {
					t.Fatalf("connection overwrote shared provider file: %s", models)
				}
			}
		})
	}
}

func TestInferenceReceiptRecovery(t *testing.T) {
	for _, outcome := range []string{"accepted", "rejected", "missing", "stale_attempt", "wrong_pair", "wrong_connection"} {
		t.Run(outcome, func(t *testing.T) {
			dir := t.TempDir()
			path, receiptPath := filepath.Join(dir, "session.json"), filepath.Join(dir, "receipt.json")
			model, thinking := "provider/new", "high"
			m := &sessionapi.Manifest{
				SessionKind:         sessionapi.KindController,
				Config:              sessionapi.SessionConfig{Model: "provider/old", Thinking: "medium"},
				InferenceConnection: &sessionapi.InferenceConnection{ID: "old"},
				InferenceUpdate: &sessionapi.InferenceUpdate{
					InferenceUpdateRequest: sessionapi.InferenceUpdateRequest{RequestID: "change", Model: &model, Thinking: &thinking, Connection: &sessionapi.InferenceConnection{ID: "new"}},
					Revision:               1, Status: "applying", AttemptID: "current", ReceiptPath: receiptPath,
				},
			}
			if err := sessionapi.WriteManifest(path, m); err != nil {
				t.Fatal(err)
			}
			r := executor.PiStartupReceipt{RequestID: "change", AttemptID: "current", Model: model, Thinking: thinking, ConnectionID: "new"}
			switch outcome {
			case "rejected":
				r.Error = "unsupported settings"
			case "stale_attempt":
				r.AttemptID = "previous"
			case "wrong_pair":
				r.Thinking = "low"
			case "wrong_connection":
				r.ConnectionID = "old"
			}
			if outcome != "missing" {
				data, _ := json.Marshal(r)
				if err := os.WriteFile(receiptPath, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			err := recoverTurnInference(path)
			if err != nil {
				t.Fatal(err)
			}
			saved, err := sessionapi.ReadManifest(path)
			if err != nil {
				t.Fatal(err)
			}
			wantStatus := "unknown"
			if outcome == "accepted" {
				wantStatus = "applied"
			}
			if outcome == "rejected" {
				wantStatus = "rejected"
			}
			if saved.InferenceUpdate.Status != wantStatus {
				t.Fatalf("unexpected outcome: %+v", saved.InferenceUpdate)
			}
			if outcome == "accepted" {
				if saved.Config.Model != model || saved.Config.Thinking != thinking || saved.InferenceConnection.ID != "new" {
					t.Fatalf("lost accepted pair: %+v", saved.Config)
				}
			} else if saved.Config.Model != "provider/old" || saved.Config.Thinking != "medium" || saved.InferenceConnection.ID != "old" {
				t.Fatalf("changed unconfirmed settings: %+v", saved.Config)
			}
		})
	}
}

func TestInferencePendingAtSessionEnd(t *testing.T) {
	for _, kind := range []sessionapi.SessionKind{sessionapi.KindTask, sessionapi.KindController} {
		for _, result := range []game.GameResult{game.GameSuccess, game.GameFailure, game.GameStopped} {
			t.Run(string(kind)+"/"+string(result), func(t *testing.T) {
				dir := t.TempDir()
				m := &sessionapi.Manifest{SessionKind: kind, Epochs: []sessionapi.Epoch{{ID: 1}}, InferenceUpdate: &sessionapi.InferenceUpdate{Status: "pending"}}
				if err := sessionapi.WriteManifest(manifestPath(dir), m); err != nil {
					t.Fatal(err)
				}
				if err := finishEpoch(dir, 1, &game.PVGResult{GameResult: result}); err != nil {
					t.Fatal(err)
				}
				saved, err := sessionapi.ReadManifest(manifestPath(dir))
				if err != nil {
					t.Fatal(err)
				}
				want := "pending"
				if kind == sessionapi.KindTask || result == game.GameStopped {
					want = "rejected"
				}
				if saved.InferenceUpdate.Status != want {
					t.Fatalf("pending outcome: %+v", saved.InferenceUpdate)
				}
			})
		}
	}
}

func TestInferenceMissingReceiptDoesNotReplayPrompt(t *testing.T) {
	root := t.TempDir()
	dir, home := filepath.Join(root, "session"), filepath.Join(root, "home")
	bin := filepath.Join(home, ".local", "bin")
	for _, folder := range []string{dir, bin} {
		if err := os.MkdirAll(folder, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	script := "#!/bin/sh\nprintf 'invocation\\n' >> \"$HOME/calls\"\nprintf '<status>CONCEDE</status>\\n'\n"
	if err := os.WriteFile(filepath.Join(bin, "pi"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	m := &sessionapi.Manifest{SessionID: "session", SessionKind: sessionapi.KindController, Config: sessionapi.SessionConfig{Model: "provider/old", Thinking: "medium"}}
	if err := sessionapi.WriteManifest(manifestPath(dir), m); err != nil {
		t.Fatal(err)
	}
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeLocal)
	model := "provider/new"
	if _, err := store.UpdateInference("session", sessionapi.InferenceUpdateRequest{RequestID: "change", Model: &model}); err != nil {
		t.Fatal(err)
	}
	p := platform.NewLocalPlatform(dir)
	p.Env = map[string]string{"HOME": home}
	e := &sessionInferenceExecutor{sessionDir: dir, pi: executor.NewPiExecutor(p, m.Config.Model, m.Config.Thinking, 5)}
	e.ExecuteTurn("Do work once.", "prover", &game.TurnState{Dir: dir})
	state, err := sessionapi.ReadManifest(manifestPath(dir))
	if err != nil || state.InferenceUpdate.Status != "unknown" || state.Config.Model != m.Config.Model {
		t.Fatalf("unconfirmed startup: %+v %v", state, err)
	}
	calls, err := os.ReadFile(filepath.Join(home, "calls"))
	if err != nil || string(calls) != "invocation\n" {
		t.Fatalf("prompt replayed after missing receipt: %q %v", calls, err)
	}
}

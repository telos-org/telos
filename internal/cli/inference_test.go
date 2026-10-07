package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	"github.com/telos-org/telos/internal/sessionworker"
)

func TestInferenceRealPiAPIAndPersistence(t *testing.T) {
	for _, phase := range []string{
		"request", "tool", "before_prompt", "definition", "legacy",
		"combined", "combined_tool", "combined_boundary", "combined_unknown",
		"combined_rejected", "combined_unsupported", "combined_missing", "combined_override",
		"combined_definition", "combined_definition_rejected", "combined_definition_headers",
		"combined_thinking", "combined_model", "combined_repeated", "combined_metadata",
		"combined_native", "combined_native_definition",
		"existing_definition", "combined_existing_definition",
		"matching_definition", "combined_matching_definition", "combined_partial_definition",
		"combined_stream_definition", "combined_stream_implicit_definition",
		"combined_matching_stream_definition",
		"combined_extension_definition", "combined_extension_partial_definition",
	} {
		t.Run(phase, func(t *testing.T) {
			combined := strings.HasPrefix(phase, "combined")
			boundary := phase == "combined_boundary" || phase == "combined_unknown"
			existingDefinition := phase == "existing_definition" || phase == "combined_existing_definition"
			rejected := strings.HasSuffix(phase, "rejected") || phase == "combined_unsupported" || phase == "combined_override" || phase == "combined_missing" || phase == "combined_native_definition" || existingDefinition || strings.HasPrefix(phase, "combined_stream_") || strings.HasPrefix(phase, "combined_extension_") || phase == "combined_matching_stream_definition"
			binaryEnv := "TELOS_TEST_PI_BINARY"
			if phase == "legacy" {
				binaryEnv = "TELOS_TEST_LEGACY_PI_BINARY"
			}
			binary := os.Getenv(binaryEnv)
			if binary == "" {
				t.Skip("set " + binaryEnv + " to test real Pi")
			}
			root, home := t.TempDir(), t.TempDir()
			dir := filepath.Join(root, "session")
			bin := filepath.Join(home, ".local", "bin")
			agent := filepath.Join(home, "agent")
			for _, path := range []string{dir, bin, agent} {
				if err := os.MkdirAll(path, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			fixture, err := filepath.Abs("../executor/testdata/pi_offline_provider.js")
			if err != nil {
				t.Fatal(err)
			}
			quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
			wrapper := "#!/bin/sh\nexec " + quote(binary) + " --offline --no-extensions --no-skills --no-prompt-templates --no-themes --no-context-files --tools bash -e " + quote(fixture) + " \"$@\"\n"
			if err := os.WriteFile(filepath.Join(bin, "pi"), []byte(wrapper), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			if err := os.WriteFile(filepath.Join(agent, "settings.json"), []byte(`{"compaction":{"enabled":false},"retry":{"enabled":false}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if phase == "combined_override" {
				if err := os.WriteFile(filepath.Join(agent, "settings.json"), []byte(`{"compaction":{"enabled":false,"modelOverrides":{"rpc-b/probe-b":{"reserveTokens":5000}}},"retry":{"enabled":false}}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			manifest := &sessionapi.Manifest{SessionID: "session", SessionKind: sessionapi.KindController, Config: sessionapi.SessionConfig{Model: "rpc-a/probe-a", Thinking: "medium"}}
			if err := sessionapi.WriteManifest(manifestPath(dir), manifest); err != nil {
				t.Fatal(err)
			}
			owner, err := sessionworker.AcquireOwnership(dir, "")
			if err != nil {
				t.Fatal(err)
			}
			defer owner.Release()
			store := sessionapi.NewFileStore(root, sessionapi.RuntimeLocal)
			store.OnInferenceUpdate = func(string) error { return sessionworker.NotifyInference(dir) }
			mux := http.NewServeMux()
			sessionapi.RegisterRoutes(mux, store, sessionapi.AllowAllAuthorizer{}, sessionapi.RuntimeIdentity{})
			p := platform.NewLocalPlatform(dir)
			p.Env = map[string]string{"HOME": home, "PI_CODING_AGENT_DIR": agent, "PI_TELEMETRY": "0", "TELOS_PI_PROBE_DIR": dir, "TELOS_PI_PROBE_PHASE": phase}
			if phase == "combined_tool" {
				p.Env["TELOS_PI_PROBE_PHASE"] = "tool"
			}
			if boundary {
				p.Env["TELOS_PI_PROBE_PHASE"] = "atomic_boundary"
			}
			pi := executor.NewPiExecutor(p, "rpc-a/probe-a", "medium", 20)
			e := &sessionInferenceExecutor{sessionDir: dir, pi: pi, notifications: owner.Inference}
			var stop atomic.Bool
			defer stop.Store(true)
			result := make(chan game.TurnResult, 1)
			var interrupted *game.TurnResult
			deadline := time.Now().Add(18 * time.Second)
			wait := func(check func() bool) {
				t.Helper()
				for !check() {
					if time.Now().After(deadline) {
						state, _ := store.Inference("session")
						data, _ := json.Marshal(state)
						t.Fatalf("timed out waiting for Pi: %s", data)
					}
					select {
					case done := <-result:
						t.Fatalf("Pi exited early: %+v", done)
					default:
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
			update := func(req sessionapi.InferenceUpdateRequest, status string) {
				t.Helper()
				body, _ := json.Marshal(req)
				response := httptest.NewRecorder()
				mux.ServeHTTP(response, httptest.NewRequest("PUT", "/api/sessions/session/inference", bytes.NewReader(body)))
				if response.Code != 202 {
					t.Fatalf("update: %d %s", response.Code, response.Body.String())
				}
				if status == "pending" {
					return
				}
				wait(func() bool {
					current, err := store.Inference("session")
					return err == nil && current.Update.Status == status
				})
			}
			model := "rpc-b/probe-b"
			if phase == "combined_missing" {
				model = "rpc-b/missing"
			}
			var definition json.RawMessage
			if strings.Contains(phase, "definition") {
				model = "rpc-a/probe-c"
				definition = json.RawMessage(`{"id":"probe-c","name":"New offline model","api":"telos-offline-test","reasoning":true,"input":["text"],"contextWindow":256000,"maxTokens":8192,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0}}`)
				if err := os.WriteFile(filepath.Join(agent, "models.json"), []byte(`{"providers":{"rpc-a":{"api":"telos-offline-test","apiKey":"test-only","baseUrl":"https://unused.invalid"},"rpc-b":{"api":"telos-offline-test","apiKey":"test-only","baseUrl":"https://unused.invalid","models":[{"id":"probe-b","name":"Offline test","reasoning":true,"contextWindow":128000,"maxTokens":4096,"headers":{"x-telos-model-header":"preserved"}}]}}}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if existingDefinition {
				model = "rpc-a/probe-a"
				definition = json.RawMessage(`{"id":"probe-a","api":"telos-offline-test","contextWindow":256000,"maxTokens":8192}`)
			}
			if phase == "matching_definition" || phase == "combined_matching_definition" || phase == "combined_extension_definition" || phase == "combined_matching_stream_definition" {
				model = "rpc-b/probe-b"
				// Field order is irrelevant, including inside the cost object.
				definition = json.RawMessage(`{"id":"probe-b","name":"Offline test","api":"telos-offline-test","reasoning":true,"input":["text"],"contextWindow":128000,"maxTokens":4096,"cost":{"cacheWrite":0,"input":0,"cacheRead":0,"output":0}}`)
				if phase == "combined_matching_stream_definition" {
					definition = json.RawMessage(strings.Replace(string(definition), "telos-offline-test", "different-stream-api", 1))
				}
			}
			if phase == "combined_partial_definition" || phase == "combined_extension_partial_definition" {
				model = "rpc-b/probe-b"
				definition = json.RawMessage(`{"id":"probe-b"}`)
			}
			if phase == "combined_stream_definition" {
				definition = json.RawMessage(`{"id":"probe-c","api":"different-stream-api","reasoning":true}`)
			}
			if strings.HasPrefix(phase, "combined_extension_") {
				// An extension-only model cannot be reconstructed from this file.
				if err := os.WriteFile(filepath.Join(agent, "models.json"), []byte(`{"providers":{}}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "combined_stream_implicit_definition" {
				definition = json.RawMessage(`{"id":"probe-c","reasoning":true}`)
				if err := os.WriteFile(filepath.Join(agent, "models.json"), []byte(`{"providers":{"rpc-a":{"api":"different-stream-api","apiKey":"test-only","baseUrl":"https://unused.invalid"}}}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "before_prompt" {
				update(sessionapi.InferenceUpdateRequest{RequestID: "model", Model: &model}, "pending")
			}
			go func() {
				result <- e.ExecuteTurn("Run the tool and report success.", "prover", &game.TurnState{Dir: dir, StopRequested: stop.Load})
			}()
			wait(func() bool { _, err := os.Stat(filepath.Join(dir, "request-1.json")); return err == nil })
			overlayPath := filepath.Join(dir, "inference-model.json")
			originalOverlay, err := os.ReadFile(overlayPath)
			if err != nil {
				t.Fatal(err)
			}
			if phase == "tool" || phase == "combined_tool" {
				wait(func() bool { _, err := os.Stat(filepath.Join(dir, "tool-started")); return err == nil })
			}
			if phase != "before_prompt" {
				want := "applied"
				if phase == "legacy" || rejected {
					want = "rejected"
				}
				if phase == "request" {
					bad := "rpc-a/missing"
					update(sessionapi.InferenceUpdateRequest{RequestID: "invalid", Model: &bad}, "rejected")
				}
				revision := 0
				if phase == "request" {
					revision = 1
				}
				req := sessionapi.InferenceUpdateRequest{RequestID: "model", ExpectedRevision: revision, Model: &model, ModelDefinition: definition}
				if combined {
					level := "high"
					if strings.HasSuffix(phase, "rejected") || phase == "combined_unsupported" {
						level, want = "banana", "rejected"
						if phase == "combined_unsupported" {
							level = "xhigh"
						}
					}
					if rejected {
						want = "rejected"
					}
					req.Thinking = &level
				}
				if boundary {
					update(req, "pending")
					wait(func() bool { _, err := os.Stat(filepath.Join(dir, "model-select-started")); return err == nil })
					for _, name := range []string{"request-release", "tool-release"} {
						if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
							t.Fatal(err)
						}
					}
					wait(func() bool { _, err := os.Stat(filepath.Join(dir, "request-2.json")); return err == nil })
					current, err := store.Inference("session")
					if err != nil || current.Update.Status != "applying" {
						t.Fatalf("request did not race the outstanding model command: %+v %v", current, err)
					}
					if phase == "combined_unknown" {
						// The pair is already being used, but its acknowledgement
						// has not arrived. End Pi at that point to test recovery.
						stop.Store(true)
						select {
						case done := <-result:
							interrupted = &done
						case <-time.After(10 * time.Second):
							t.Fatal("Pi did not stop during settings change")
						}
						if !strings.Contains(interrupted.Error, "local_interrupted") {
							t.Fatalf("interrupted change: %+v", interrupted)
						}
						stop.Store(false)
						want = "unknown"
					} else {
						if err := os.WriteFile(filepath.Join(dir, "model-select-release"), nil, 0o600); err != nil {
							t.Fatal(err)
						}
						wait(func() bool {
							current, err := store.Inference("session")
							return err == nil && current.Update.Status == "applied"
						})
					}
				} else {
					update(req, want)
				}
				if combined {
					replay, err := store.UpdateInference("session", req)
					if err != nil || replay.Revision != 1 || replay.Update.Status != want {
						t.Fatalf("combined replay: %+v %v", replay, err)
					}
				}
			}
			if phase == "definition" {
				update(sessionapi.InferenceUpdateRequest{RequestID: "same-model", ExpectedRevision: 1, Model: &model}, "applied")
				original := "rpc-a/probe-a"
				update(sessionapi.InferenceUpdateRequest{RequestID: "switch-back", ExpectedRevision: 2, Model: &original}, "applied")
				update(sessionapi.InferenceUpdateRequest{RequestID: "new-again", ExpectedRevision: 3, Model: &model, ModelDefinition: definition}, "applied")
			}
			current, err := store.Inference("session")
			if err != nil {
				t.Fatal(err)
			}
			if phase == "combined_native_definition" && !strings.Contains(current.Update.Error, "native provider") {
				t.Fatalf("unexpected native metadata rejection: %+v", current.Update)
			}
			if existingDefinition && !strings.Contains(current.Update.Error, "Cannot change model metadata") {
				t.Fatalf("metadata replacement was not explicitly rejected: %+v", current.Update)
			}
			if (strings.HasPrefix(phase, "combined_stream_") || phase == "combined_matching_stream_definition") && !strings.Contains(current.Update.Error, "streaming API") {
				t.Fatalf("provider API replacement was not explicitly rejected: %+v", current.Update)
			}
			level := "high"
			wantStatus := "applied"
			if phase == "legacy" {
				wantStatus = "rejected"
			}
			if !combined && !rejected {
				update(sessionapi.InferenceUpdateRequest{RequestID: "thinking", ExpectedRevision: current.Revision, Thinking: &level}, wantStatus)
			}
			if phase == "combined_thinking" {
				level := "low"
				update(sessionapi.InferenceUpdateRequest{RequestID: "after-pair", ExpectedRevision: current.Revision, Thinking: &level}, "applied")
			}
			if phase == "combined_model" || phase == "combined_definition_headers" {
				model = "rpc-a/probe-a"
				update(sessionapi.InferenceUpdateRequest{RequestID: "after-pair", ExpectedRevision: current.Revision, Model: &model}, "applied")
			}
			if phase == "combined_repeated" {
				for i, level := range []string{"low", "medium", "high"} {
					update(sessionapi.InferenceUpdateRequest{RequestID: "pair-" + level, ExpectedRevision: current.Revision + i, Model: &model, Thinking: &level}, "applied")
				}
			}
			if phase == "combined_metadata" {
				// Neither combined nor model-only changes may rewrite a physical
				// entry that the already-selected route will resolve again.
				for _, bad := range []json.RawMessage{
					json.RawMessage(`{"id":"probe-b","reasoning":false}`),
					json.RawMessage(`{"id":"probe-b","api":"different-stream-api"}`),
					json.RawMessage(`{"id":"probe-b","thinkingLevelMap":{"high":12345}}`),
					json.RawMessage(`{"id":"probe-b","compat":{"supportsDeveloperRole":false}}`),
				} {
					for _, thinking := range []*string{&level, nil} {
						current, err := store.Inference("session")
						if err != nil {
							t.Fatal(err)
						}
						requestID := fmt.Sprintf("bad-metadata-%d", current.Revision)
						update(sessionapi.InferenceUpdateRequest{RequestID: requestID, ExpectedRevision: current.Revision, Model: &model, Thinking: thinking, ModelDefinition: bad}, "rejected")
					}
				}
			}
			for _, name := range []string{"request-release", "tool-release", "second-response-release"} {
				if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var completed game.TurnResult
			if interrupted == nil {
				select {
				case completed = <-result:
				case <-time.After(10 * time.Second):
					t.Fatal("Pi did not finish")
				}
				if completed.Error != "" || completed.Status != game.StatusConcede {
					t.Fatalf("turn: %+v", completed)
				}
			}
			var second struct {
				Model, Thinking string
				API             string
				ContextWindow   int
				MaxTokens       int
				Messages        []json.RawMessage
				Headers         map[string]string
			}
			data, err := os.ReadFile(filepath.Join(dir, "request-2.json"))
			if err != nil || json.Unmarshal(data, &second) != nil {
				t.Fatalf("second request: %s %v", data, err)
			}
			_, wantModel, _ := strings.Cut(model, "/")
			wantThinking := "high"
			if rejected {
				wantModel, wantThinking = "probe-a", "medium"
			}
			if phase == "combined_thinking" {
				wantThinking = "low"
			}
			if phase == "legacy" {
				wantModel, wantThinking = "probe-a", "medium"
			}
			if second.Model != wantModel || second.Thinking != wantThinking || !bytes.Contains(data, []byte("preserved tool result")) {
				t.Fatalf("next request lost settings or tool result: %s", data)
			}
			wantContext, wantMaxTokens := 128000, 4096
			if wantModel == "probe-c" {
				wantContext, wantMaxTokens = 256000, 8192
			}
			if second.API != "telos-offline-test" || second.ContextWindow != wantContext || second.MaxTokens != wantMaxTokens {
				t.Fatalf("next request used different model metadata: %s", data)
			}
			if strings.Contains(phase, "matching_definition") || phase == "combined_partial_definition" || strings.HasPrefix(phase, "combined_extension_") {
				if second.Headers["x-telos-model-header"] != "preserved" {
					t.Fatalf("metadata update dropped existing model headers: %s", data)
				}
			}
			if phase == "combined_definition_headers" && second.Headers["x-telos-model-header"] != "preserved" {
				t.Fatalf("adding model metadata dropped existing model headers: %s", data)
			}
			if phase == "legacy" {
				return
			}
			// A new executor represents another turn or a worker process restart.
			saved, err := sessionapi.ReadManifest(manifestPath(dir))
			if err != nil {
				t.Fatal(err)
			}
			if (rejected || phase == "combined_metadata") && len(saved.InferenceModelDefinition) != 0 {
				t.Fatalf("rejected metadata overwrote the saved definition: %s", saved.InferenceModelDefinition)
			}
			if rejected || phase == "combined_metadata" {
				overlay, err := os.ReadFile(overlayPath)
				if err != nil || !bytes.Equal(overlay, originalOverlay) {
					t.Fatalf("rejected metadata changed the startup overlay: %s %v", overlay, err)
				}
			}
			if phase == "combined_unknown" {
				wantModel, wantThinking = "probe-a", "medium"
				if saved.Config.Model != "rpc-a/probe-a" || saved.Config.Thinking != "medium" {
					t.Fatalf("unconfirmed pair overwrote saved defaults: %+v", saved.Config)
				}
			}
			freshPi, err := createPiExecutor(dir, manifestToConfig(saved))
			if err != nil {
				t.Fatal(err)
			}
			freshPi.Platform = p
			fresh := &sessionInferenceExecutor{sessionDir: dir, pi: freshPi}
			completed = fresh.ExecuteTurn("Verify again.", "verifier", &game.TurnState{Dir: dir, StopRequested: stop.Load})
			if completed.Error != "" {
				t.Fatalf("restarted turn: %+v", completed)
			}
			data, err = os.ReadFile(filepath.Join(dir, "request-1.json"))
			if err != nil || json.Unmarshal(data, &second) != nil || second.Model != wantModel || second.Thinking != wantThinking {
				t.Fatalf("restart lost settings: %s %v", data, err)
			}
			if second.API != "telos-offline-test" || second.ContextWindow != wantContext || second.MaxTokens != wantMaxTokens {
				t.Fatalf("restart changed model metadata: %s", data)
			}
			if strings.Contains(phase, "matching_definition") || phase == "combined_partial_definition" || strings.HasPrefix(phase, "combined_extension_") {
				if second.Headers["x-telos-model-header"] != "preserved" {
					t.Fatalf("restart dropped existing model headers: %s", data)
				}
			}

		})
	}
}

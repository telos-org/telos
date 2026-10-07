package cli

import (
	"bytes"
	"encoding/json"
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
	for _, phase := range []string{"request", "tool", "before_prompt", "definition", "legacy", "combined", "combined_tool", "combined_partial", "combined_definition", "combined_definition_partial"} {
		t.Run(phase, func(t *testing.T) {
			combined := strings.HasPrefix(phase, "combined")
			partial := strings.HasSuffix(phase, "partial")
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
			pi := executor.NewPiExecutor(p, "rpc-a/probe-a", "medium", 20)
			e := &sessionInferenceExecutor{sessionDir: dir, pi: pi, notifications: owner.Inference}
			var stop atomic.Bool
			defer stop.Store(true)
			result := make(chan game.TurnResult, 1)
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
			var definition json.RawMessage
			if strings.Contains(phase, "definition") {
				model = "rpc-a/probe-c"
				definition = json.RawMessage(`{"id":"probe-c","name":"New offline model","api":"telos-offline-test","reasoning":true,"input":["text"],"contextWindow":128000,"maxTokens":4096,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0}}`)
				if err := os.WriteFile(filepath.Join(agent, "models.json"), []byte(`{"providers":{"rpc-a":{"api":"telos-offline-test","apiKey":"test-only","baseUrl":"https://unused.invalid"}}}`), 0o600); err != nil {
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
			if phase == "tool" || phase == "combined_tool" {
				wait(func() bool { _, err := os.Stat(filepath.Join(dir, "tool-started")); return err == nil })
			}
			if phase != "before_prompt" {
				want := "applied"
				if phase == "legacy" {
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
					if partial {
						level, want = "banana", "partial"
					}
					req.Thinking = &level
				}
				update(req, want)
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
			level := "high"
			wantStatus := "applied"
			if phase == "legacy" {
				wantStatus = "rejected"
			}
			if !combined {
				update(sessionapi.InferenceUpdateRequest{RequestID: "thinking", ExpectedRevision: current.Revision, Thinking: &level}, wantStatus)
			}
			for _, name := range []string{"request-release", "tool-release"} {
				if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var completed game.TurnResult
			select {
			case completed = <-result:
			case <-time.After(10 * time.Second):
				t.Fatal("Pi did not finish")
			}
			if completed.Error != "" || completed.Status != game.StatusConcede {
				t.Fatalf("turn: %+v", completed)
			}
			var second struct {
				Model, Thinking string
				Messages        []json.RawMessage
			}
			data, err := os.ReadFile(filepath.Join(dir, "request-2.json"))
			if err != nil || json.Unmarshal(data, &second) != nil {
				t.Fatalf("second request: %s %v", data, err)
			}
			_, wantModel, _ := strings.Cut(model, "/")
			wantThinking := "high"
			if partial {
				wantThinking = "medium"
			}
			if phase == "legacy" {
				wantModel, wantThinking = "probe-a", "medium"
			}
			if second.Model != wantModel || second.Thinking != wantThinking || !bytes.Contains(data, []byte("preserved tool result")) {
				t.Fatalf("next request lost settings or tool result: %s", data)
			}
			if phase == "legacy" {
				return
			}
			// A new executor represents another turn or a worker process restart.
			saved, err := sessionapi.ReadManifest(manifestPath(dir))
			if err != nil {
				t.Fatal(err)
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
		})
	}
}

package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/telos-org/telos/internal/game"
	"github.com/telos-org/telos/internal/platform"
)

// Opt in with TELOS_TEST_PI_BINARY=/absolute/path/to/pi. These tests run the real
// executable against a local fake provider: no credentials or external inference.
func TestPiIntegrationSwitchDuringRequestAndTool(t *testing.T) {
	binary := os.Getenv("TELOS_TEST_PI_BINARY")
	if binary == "" {
		t.Skip("set TELOS_TEST_PI_BINARY to run real Pi RPC integration")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"request", "tool"} {
		t.Run(phase, func(t *testing.T) { testPiIntegrationSwitch(t, binary, phase) })
	}
}

func TestPiIntegrationOrdinaryRuns(t *testing.T) {
	for _, tc := range []struct{ name, env, phase string }{
		{"current", "TELOS_TEST_PI_BINARY", "ordinary"},
		{"legacy", "TELOS_TEST_LEGACY_PI_BINARY", "legacy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binary := os.Getenv(tc.env)
			if binary == "" {
				t.Skip("set " + tc.env + " to run real Pi integration")
			}
			binary, err := filepath.Abs(binary)
			if err != nil {
				t.Fatal(err)
			}
			testPiIntegrationSwitch(t, binary, tc.phase)
		})
	}
}

func testPiIntegrationSwitch(t *testing.T, binary, phase string) {
	workspace := t.TempDir()
	home := t.TempDir()
	agentDir := filepath.Join(home, "agent")
	bin := filepath.Join(home, ".local", "bin")
	for _, dir := range []string{agentDir, bin} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	startedFile := filepath.Join(workspace, "tool-started")
	releaseFile := filepath.Join(workspace, "tool-release")
	pidFile := filepath.Join(workspace, "pi-pid")
	requests := make(chan map[string]interface{}, 4)
	releaseRequest := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { close(releaseRequest) })
		_ = os.WriteFile(releaseFile, []byte("release"), 0o600)
	}
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		requests <- request
		number := count.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := func(delta map[string]interface{}, finish interface{}) {
			data, _ := json.Marshal(map[string]interface{}{"id": "probe", "object": "chat.completion.chunk", "created": 1, "model": request["model"], "choices": []interface{}{map[string]interface{}{"index": 0, "delta": delta, "finish_reason": finish}}})
			fmt.Fprintf(w, "data: %s\n\n", data)
			w.(http.Flusher).Flush()
		}
		chunk(map[string]interface{}{"role": "assistant"}, nil)
		if number == 1 {
			if phase != "tool" {
				select {
				case <-releaseRequest:
				case <-r.Context().Done():
					return
				}
			}
			command := "printf started > " + shellQuote(startedFile) + "; while [ ! -f " + shellQuote(releaseFile) + " ]; do sleep 0.02; done; printf 'preserved tool result'"
			args, _ := json.Marshal(map[string]string{"command": command})
			chunk(map[string]interface{}{"tool_calls": []interface{}{map[string]interface{}{"index": 0, "id": "call_probe", "type": "function", "function": map[string]string{"name": "bash", "arguments": string(args)}}}}, nil)
			chunk(map[string]interface{}{}, "tool_calls")
		} else {
			chunk(map[string]interface{}{"content": "Complete.\n<status>CONCEDE</status>"}, nil)
			chunk(map[string]interface{}{}, "stop")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	defer release()
	models := func(id string) []interface{} {
		return []interface{}{map[string]interface{}{"id": id, "name": id, "reasoning": true, "input": []string{"text"}, "contextWindow": 128000, "maxTokens": 4096, "cost": map[string]int{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0}}}
	}
	providers := map[string]interface{}{}
	for _, suffix := range []string{"a", "b"} {
		providers["rpc-"+suffix] = map[string]interface{}{"baseUrl": server.URL + "/v1", "api": "openai-completions", "apiKey": "local-test-only", "compat": map[string]bool{"supportsReasoningEffort": true}, "models": models("probe-" + suffix)}
	}
	writeJSON := func(path string, value interface{}) {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeJSON(filepath.Join(agentDir, "models.json"), map[string]interface{}{"providers": providers})
	settingsPath := filepath.Join(agentDir, "settings.json")
	writeJSON(settingsPath, map[string]interface{}{"compaction": map[string]bool{"enabled": false}, "retry": map[string]bool{"enabled": false}})
	originalSettings, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf '%s' \"$$\" > " + shellQuote(pidFile) + "\nexec " + shellQuote(binary) + " --offline --no-extensions --no-skills --no-prompt-templates --no-themes --no-context-files --tools bash \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "pi"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	p := platform.NewLocalPlatform(workspace)
	p.Env = map[string]string{"HOME": home, "PI_CODING_AGENT_DIR": agentDir, "PI_TELEMETRY": "0"}
	pe := NewPiExecutor(p, "rpc-a/probe-a", "medium", 30)
	var stopped atomic.Bool
	state := &game.TurnState{Dir: workspace, StopRequested: stopped.Load}
	defer stopped.Store(true)
	resultCh := make(chan game.TurnResult, 1)
	go func() { resultCh <- pe.ExecuteTurn("Run the tool and report success.", "prover", state) }()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	nextRequest := func() map[string]interface{} {
		select {
		case request := <-requests:
			return request
		case result := <-resultCh:
			t.Fatalf("Pi exited before request: %+v", result)
		case <-ctx.Done():
			t.Fatal("timed out waiting for model request")
		}
		return nil
	}
	first := nextRequest()
	if first["model"] != "probe-a" || first["reasoning_effort"] != "medium" {
		t.Fatalf("first request settings: %v/%v", first["model"], first["reasoning_effort"])
	}
	if phase == "tool" {
		for {
			if _, err := os.Stat(startedFile); err == nil {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal("tool did not start")
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	originalPID, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	wantModel, wantThinking := "probe-a", "medium"
	if phase == "legacy" {
		// Legacy Pi persists the initial CLI thinking override. Check that our
		// rejected live controls make no further changes after startup.
		originalSettings, err = os.ReadFile(settingsPath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pe.SetModel(ctx, "rpc-b", "probe-b"); !errors.Is(err, ErrPiLiveSettingsUnsupported) {
			t.Fatalf("legacy model change should be refused without stopping the turn: %v", err)
		}
		if _, err := pe.SetThinkingLevel(ctx, "high"); !errors.Is(err, ErrPiLiveSettingsUnsupported) {
			t.Fatalf("legacy thinking change should be refused without stopping the turn: %v", err)
		}
	} else if phase != "ordinary" {
		if _, err := pe.SetModel(ctx, "rpc-b", "missing"); err == nil {
			t.Fatal("invalid model accepted")
		}
		if _, err := pe.SetThinkingLevel(ctx, "xhigh"); err == nil {
			t.Fatal("unsupported thinking accepted")
		}
		settings, err := pe.SetModel(ctx, "rpc-b", "probe-b")
		if err != nil || settings.Provider != "rpc-b" || settings.Model != "probe-b" {
			t.Fatalf("switch model: %+v, %v", settings, err)
		}
		settings, err = pe.SetThinkingLevel(ctx, "high")
		if err != nil || settings.Thinking != "high" {
			t.Fatalf("switch thinking: %+v, %v", settings, err)
		}
		wantModel, wantThinking = "probe-b", "high"
	}
	pid, _ := strconv.Atoi(string(originalPID))
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("Pi exited while request/tool was blocked: %v", err)
	}
	if count.Load() != 1 {
		t.Fatal("next request started before blocked work was released")
	}
	release()
	second := nextRequest()
	if second["model"] != wantModel || second["reasoning_effort"] != wantThinking {
		t.Fatalf("second request settings: %v/%v", second["model"], second["reasoning_effort"])
	}
	messages, _ := second["messages"].([]interface{})
	toolResults := 0
	for _, raw := range messages {
		msg, _ := raw.(map[string]interface{})
		if msg["role"] == "tool" {
			toolResults++
			if !strings.Contains(fmt.Sprint(msg["content"]), "preserved tool result") {
				t.Fatalf("tool result was corrupted: %v", msg)
			}
		}
	}
	if toolResults != 1 {
		t.Fatalf("tool result count: %d", toolResults)
	}
	select {
	case result := <-resultCh:
		if result.Error != "" || result.Status != game.StatusConcede || result.Stats.NumTurns != 1 || result.Stats.Model != wantModel {
			t.Fatalf("result: %+v", result)
		}
	case <-ctx.Done():
		t.Fatal("Pi never settled")
	}
	finalPID, err := os.ReadFile(pidFile)
	if err != nil || string(finalPID) != string(originalPID) {
		t.Fatal("Pi process was replaced")
	}
	finalSettings, err := os.ReadFile(settingsPath)
	if err != nil || string(finalSettings) != string(originalSettings) {
		t.Fatal("RPC controls changed persistent Pi defaults")
	}
	if _, err := os.Stat(state.PiSessionPath()); err != nil {
		t.Fatalf("session was not preserved: %v", err)
	}
}

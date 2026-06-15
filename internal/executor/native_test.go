package executor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/game"
	"github.com/telos-org/telos/internal/platform"
)

func TestNativeExecutorRunsChatToolLoopAndWritesWorkspace(t *testing.T) {
	workspace := t.TempDir()
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("authorization header: got %q", got)
		}
		var req map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		requests++
		w.Header().Set("Content-Type", "application/json")
		if requests == 1 {
			if req["model"] != "test-model" {
				t.Fatalf("model: got %v", req["model"])
			}
			_, _ = w.Write([]byte(`{
				"choices":[{
					"finish_reason":"tool_calls",
					"message":{
						"role":"assistant",
						"tool_calls":[{
							"id":"call_1",
							"type":"function",
							"function":{"name":"write","arguments":"{\"path\":\"answer.txt\",\"content\":\"done\\n\"}"}
						}]
					}
				}],
				"usage":{"prompt_tokens":11,"completion_tokens":7}
			}`))
			return
		}
		messages, _ := req["messages"].([]interface{})
		if len(messages) == 0 || !strings.Contains(mustJSON(messages[len(messages)-1]), "wrote answer.txt") {
			t.Fatalf("second request should include tool result, got %s", mustJSON(messages))
		}
		_, _ = w.Write([]byte(`{
			"choices":[{
				"finish_reason":"stop",
				"message":{"role":"assistant","content":"Created answer.txt.\n\n<status>CONCEDE</status>\n"}
			}],
			"usage":{"prompt_tokens":13,"completion_tokens":5}
		}`))
	}))
	defer server.Close()

	t.Setenv("TELOS_API_BASE_URL", server.URL)
	t.Setenv("TELOS_API_KEY", "test-key")
	t.Setenv("TELOS_API_STYLE", "openai-chat")

	p := platform.NewLocalPlatform(workspace)
	exec := NewNativeExecutor(p, "test/test-model", "high", 0)
	ts := &game.TurnState{Dir: filepath.Join(workspace, ".turn")}

	result := exec.ExecuteTurn("create answer.txt", "prover", ts)

	if result.Error != "" {
		t.Fatalf("error: got %q logs=%q", result.Error, result.Logs)
	}
	if result.Status != game.StatusConcede {
		t.Fatalf("status: got %s logs=%q", result.Status, result.Logs)
	}
	if requests != 2 {
		t.Fatalf("requests: got %d", requests)
	}
	data, err := os.ReadFile(filepath.Join(workspace, "answer.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "done\n" {
		t.Fatalf("answer.txt: got %q", string(data))
	}
	if result.Stats.InputTokens != 24 || result.Stats.OutputTokens != 12 || result.Stats.NumTurns != 1 {
		t.Fatalf("stats: %+v", result.Stats)
	}
	if _, err := ReadPiSession(ts.PiSessionPath()); err != nil {
		t.Fatalf("native session should stay parseable by transcript tooling: %v", err)
	}
}

func TestResolveNativeProviderUsesSilaresConvention(t *testing.T) {
	t.Setenv("SILARES_API_KEY", "test-silares-key")

	cfg, err := resolveNativeProvider("silares/moonshotai/Kimi-K2.7-Code")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != "silares" {
		t.Fatalf("provider: got %q", cfg.Provider)
	}
	if cfg.Model != "moonshotai/Kimi-K2.7-Code" {
		t.Fatalf("model: got %q", cfg.Model)
	}
	if cfg.BaseURL != "https://api.silares.com/v1" {
		t.Fatalf("base URL: got %q", cfg.BaseURL)
	}
	if cfg.Style != providerResponses {
		t.Fatalf("style: got %q", cfg.Style)
	}
}

func TestNativeToolsRejectOutsideWorkspacePath(t *testing.T) {
	workspace := t.TempDir()
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil)

	result := tools.execute(nativeToolCall{
		ID:        "call_1",
		Name:      "write",
		Arguments: `{"path":"../outside.txt","content":"bad"}`,
	})

	if !result.IsError {
		t.Fatalf("expected outside-workspace write to fail: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(workspace, "..", "outside.txt")); !os.IsNotExist(err) {
		t.Fatalf("outside file should not exist, stat err=%v", err)
	}
}

func mustJSON(v interface{}) string {
	data, _ := json.Marshal(v)
	return string(data)
}

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
	if cfg.Style != providerChat {
		t.Fatalf("style: got %q", cfg.Style)
	}
}

func TestNativeMaxToolLoopsCanBeOverridden(t *testing.T) {
	t.Setenv("TELOS_NATIVE_MAX_TOOL_LOOPS", "123")
	if got := nativeMaxToolLoops(); got != 123 {
		t.Fatalf("max tool loops override: got %d", got)
	}

	t.Setenv("TELOS_NATIVE_MAX_TOOL_LOOPS", "not-a-number")
	if got := nativeMaxToolLoops(); got != defaultMaxToolLoops {
		t.Fatalf("invalid max tool loops should use default: got %d", got)
	}
}

func TestResolveNativeProviderAllowsSilaresResponsesOverride(t *testing.T) {
	t.Setenv("SILARES_API_KEY", "test-silares-key")
	t.Setenv("SILARES_API_STYLE", "openai-responses")

	cfg, err := resolveNativeProvider("silares/moonshotai/Kimi-K2.7-Code")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Style != providerResponses {
		t.Fatalf("style: got %q", cfg.Style)
	}
}

func TestNativeExecutorResponsesIncludesHarnessPromptInUserInput(t *testing.T) {
	workspace := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var req map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		input, _ := req["input"].([]interface{})
		if len(input) != 1 {
			t.Fatalf("responses input length: got %d body=%s", len(input), mustJSON(req))
		}
		first, _ := input[0].(map[string]interface{})
		content, _ := first["content"].(string)
		for _, want := range []string{
			"Do not ask the operator what to build or what to do next.",
			"# Assignment",
			"create industry.py",
		} {
			if !strings.Contains(content, want) {
				t.Fatalf("responses user input missing %q:\n%s", want, content)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_1",
			"output":[{"type":"message","content":[{"type":"output_text","text":"Done.\n<status>CONCEDE</status>\n"}]}],
			"usage":{"input_tokens":17,"output_tokens":5}
		}`))
	}))
	defer server.Close()

	t.Setenv("TELOS_API_BASE_URL", server.URL)
	t.Setenv("TELOS_API_KEY", "test-key")
	t.Setenv("TELOS_API_STYLE", "openai-responses")

	exec := NewNativeExecutor(platform.NewLocalPlatform(workspace), "test/test-model", "high", 0)
	result := exec.ExecuteTurn("create industry.py", "prover", &game.TurnState{Dir: filepath.Join(workspace, ".turn")})

	if result.Error != "" {
		t.Fatalf("error: got %q logs=%q", result.Error, result.Logs)
	}
	if result.Status != game.StatusConcede {
		t.Fatalf("status: got %s logs=%q", result.Status, result.Logs)
	}
}

func TestNativeExecutorResponsesRetriesUnproductiveFinal(t *testing.T) {
	workspace := t.TempDir()
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var req map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		requests++
		w.Header().Set("Content-Type", "application/json")
		if requests == 1 {
			_, _ = w.Write([]byte(`{
				"id":"resp_1",
				"output":[{"type":"message","content":[{"type":"output_text","text":"The workspace is currently empty. What would you like me to work on?"}]}],
				"usage":{"input_tokens":17,"output_tokens":11}
			}`))
			return
		}
		if req["previous_response_id"] != "resp_1" {
			t.Fatalf("second request previous_response_id: got %v", req["previous_response_id"])
		}
		input, _ := req["input"].([]interface{})
		if len(input) != 1 {
			t.Fatalf("second responses input length: got %d body=%s", len(input), mustJSON(req))
		}
		first, _ := input[0].(map[string]interface{})
		content, _ := first["content"].(string)
		if !strings.Contains(content, "Do not ask what to build") || !strings.Contains(content, "create industry.py") {
			t.Fatalf("second request missing correction content:\n%s", content)
		}
		_, _ = w.Write([]byte(`{
			"id":"resp_2",
			"output":[{"type":"message","content":[{"type":"output_text","text":"Created industry.py.\n<status>CONCEDE</status>\n"}]}],
			"usage":{"input_tokens":19,"output_tokens":7}
		}`))
	}))
	defer server.Close()

	t.Setenv("TELOS_API_BASE_URL", server.URL)
	t.Setenv("TELOS_API_KEY", "test-key")
	t.Setenv("TELOS_API_STYLE", "openai-responses")

	exec := NewNativeExecutor(platform.NewLocalPlatform(workspace), "test/test-model", "high", 0)
	result := exec.ExecuteTurn("create industry.py", "prover", &game.TurnState{Dir: filepath.Join(workspace, ".turn")})

	if result.Error != "" {
		t.Fatalf("error: got %q logs=%q", result.Error, result.Logs)
	}
	if result.Status != game.StatusConcede {
		t.Fatalf("status: got %s logs=%q", result.Status, result.Logs)
	}
	if requests != 2 {
		t.Fatalf("requests: got %d", requests)
	}
}

func TestNativeSystemPromptPreventsTaskDrift(t *testing.T) {
	prompt := nativeSystemPrompt("prover")
	for _, want := range []string{
		"Do not ask the operator what to build or what to do next.",
		"If the spec names a deliverable, implement that deliverable exactly.",
		"Ignore unrelated task ideas",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestShouldRetryUnproductiveFinalUsesAssignmentAnchors(t *testing.T) {
	task := "Create `industry.py` and run it."
	if !shouldRetryUnproductiveFinal("Your subscription is about to expire.", task) {
		t.Fatal("expected unrelated answer without assignment anchor to retry")
	}
	if shouldRetryUnproductiveFinal("Created industry.py and ran the checks.", task) {
		t.Fatal("expected answer mentioning assignment anchor to be accepted")
	}
	if anchors := assignmentFileAnchors("Create `industry.py`, update `src/app.ts`, and read `not-a-file`."); strings.Join(anchors, ",") != "industry.py,src/app.ts" {
		t.Fatalf("anchors: got %q", anchors)
	}
}

func TestNativeToolsPathResolution(t *testing.T) {
	workspace := t.TempDir()
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil)

	rejected := tools.execute(nativeToolCall{
		ID:        "call_1",
		Name:      "write",
		Arguments: `{"path":"../outside.txt","content":"bad"}`,
	})

	if !rejected.IsError {
		t.Fatalf("expected relative outside-workspace write to fail: %+v", rejected)
	}
	if _, err := os.Stat(filepath.Join(workspace, "..", "outside.txt")); !os.IsNotExist(err) {
		t.Fatalf("outside file should not exist, stat err=%v", err)
	}

	absolutePath := filepath.Join(t.TempDir(), "absolute.txt")
	written := tools.execute(nativeToolCall{
		ID:        "call_2",
		Name:      "write",
		Arguments: `{"path":` + mustJSON(absolutePath) + `,"content":"ok\n"}`,
	})
	if written.IsError {
		t.Fatalf("expected absolute container path write to succeed: %+v", written)
	}
	if !strings.Contains(written.Output, filepath.ToSlash(absolutePath)) {
		t.Fatalf("absolute path should be visible in tool result, got %q", written.Output)
	}
	data, err := os.ReadFile(absolutePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "ok\n" {
		t.Fatalf("absolute file content: got %q", data)
	}
}

func mustJSON(v interface{}) string {
	data, _ := json.Marshal(v)
	return string(data)
}

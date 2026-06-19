package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
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
		if r.URL.Path != "/responses" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("authorization header: got %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		var req map[string]interface{}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatal(err)
		}
		requests++
		if requests == 1 {
			if req["model"] != "test-model" {
				t.Fatalf("model: got %v", req["model"])
			}
			if got := int(req["max_output_tokens"].(float64)); got != defaultMaxOutputTokens {
				t.Fatalf("max_output_tokens: got %d", got)
			}
			if !strings.Contains(string(body), "create answer.txt") {
				t.Fatalf("first request should carry the task: %s", body)
			}
			writeResponsesStream(w, `{
				"id":"resp_1","status":"completed",
				"output":[{"type":"function_call","call_id":"call_1","name":"write","arguments":"{\"path\":\"answer.txt\",\"content\":\"done\\n\"}"}],
				"usage":{"input_tokens":11,"output_tokens":7,"input_tokens_details":{"cached_tokens":0}}
			}`)
			return
		}
		if req["previous_response_id"] != "resp_1" {
			t.Fatalf("second request previous_response_id: got %v", req["previous_response_id"])
		}
		if !strings.Contains(string(body), "wrote answer.txt") {
			t.Fatalf("second request should include tool result, got %s", body)
		}
		writeResponsesStream(w, `{
			"id":"resp_2","status":"completed",
			"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Created answer.txt.\n\n<status>CONCEDE</status>\n"}]}],
			"usage":{"input_tokens":13,"output_tokens":5,"input_tokens_details":{"cached_tokens":0}}
		}`)
	}))
	defer server.Close()

	t.Setenv("TELOS_API_BASE_URL", server.URL)
	t.Setenv("TELOS_API_KEY", "test-key")

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
	assertValidSessionLog(t, ts.SessionPath())
}

// assertValidSessionLog checks the session JSONL is well-formed: every line
// decodes and at least one assistant message is present.
func assertValidSessionLog(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("session log unreadable: %v", err)
	}
	var sawAssistant bool
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var event struct {
			Message struct {
				Role string `json:"role"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("session log line is not valid JSON: %q: %v", line, err)
		}
		if event.Message.Role == "assistant" {
			sawAssistant = true
		}
	}
	if !sawAssistant {
		t.Fatal("session log should contain an assistant message")
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
}

func TestNativeMaxToolLoopsCanBeOverridden(t *testing.T) {
	if got := nativeMaxToolLoops(); got != 160 {
		t.Fatalf("default max tool loops: got %d", got)
	}

	t.Setenv("TELOS_NATIVE_MAX_TOOL_LOOPS", "123")
	if got := nativeMaxToolLoops(); got != 123 {
		t.Fatalf("max tool loops override: got %d", got)
	}

	t.Setenv("TELOS_NATIVE_MAX_TOOL_LOOPS", "not-a-number")
	if got := nativeMaxToolLoops(); got != defaultMaxToolLoops {
		t.Fatalf("invalid max tool loops should use default: got %d", got)
	}
}

func TestNativeMaxOutputTokensCanBeOverridden(t *testing.T) {
	if got := nativeMaxOutputTokens(); got != 16384 {
		t.Fatalf("default max output tokens: got %d", got)
	}

	t.Setenv("TELOS_NATIVE_MAX_OUTPUT_TOKENS", "2048")
	if got := nativeMaxOutputTokens(); got != 2048 {
		t.Fatalf("max output tokens override: got %d", got)
	}

	t.Setenv("TELOS_NATIVE_MAX_OUTPUT_TOKENS", "not-a-number")
	if got := nativeMaxOutputTokens(); got != defaultMaxOutputTokens {
		t.Fatalf("invalid max output tokens should use default: got %d", got)
	}

	t.Setenv("TELOS_NATIVE_MAX_OUTPUT_TOKENS", "128")
	if got := nativeMaxOutputTokens(); got != defaultMaxOutputTokens {
		t.Fatalf("too-small max output tokens should use default: got %d", got)
	}
}

func TestNativeExecutorSendsHarnessInstructionsAndReasoning(t *testing.T) {
	workspace := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var req map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		reasoning, _ := req["reasoning"].(map[string]interface{})
		if reasoning["effort"] != "high" {
			t.Fatalf("reasoning effort: got %v body=%s", reasoning["effort"], mustJSON(req))
		}
		instructions, _ := req["instructions"].(string)
		for _, want := range []string{
			"do not ask the operator",
			"Your role for this turn is prover.",
		} {
			if !strings.Contains(instructions, want) {
				t.Fatalf("instructions missing %q:\n%s", want, instructions)
			}
		}
		writeResponsesStream(w, `{
			"id":"resp_1","status":"completed",
			"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done.\n<status>CONCEDE</status>\n"}]}],
			"usage":{"input_tokens":17,"output_tokens":5,"input_tokens_details":{"cached_tokens":0}}
		}`)
	}))
	defer server.Close()

	t.Setenv("TELOS_API_BASE_URL", server.URL)
	t.Setenv("TELOS_API_KEY", "test-key")

	exec := NewNativeExecutor(platform.NewLocalPlatform(workspace), "test/test-model", "high", 0)
	result := exec.ExecuteTurn("create industry.py", "prover", &game.TurnState{Dir: filepath.Join(workspace, ".turn")})

	if result.Error != "" {
		t.Fatalf("error: got %q logs=%q", result.Error, result.Logs)
	}
	if result.Status != game.StatusConcede {
		t.Fatalf("status: got %s logs=%q", result.Status, result.Logs)
	}
}

func TestNativeExecutorGateRetriesMissingDeliverableThenAccepts(t *testing.T) {
	workspace := t.TempDir()
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch requests {
		case 1:
			// Tool-less final that did no work for a file deliverable -> retry.
			writeResponsesStream(w, `{
				"id":"resp_1","status":"completed",
				"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"I have inspected the workspace and understand the task."}]}],
				"usage":{"input_tokens":5,"output_tokens":5,"input_tokens_details":{"cached_tokens":0}}
			}`)
		case 2:
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "already fully specified") {
				t.Fatalf("second request should carry the correction prompt, got %s", body)
			}
			writeResponsesStream(w, `{
				"id":"resp_2","status":"completed",
				"output":[{"type":"function_call","call_id":"c1","name":"write","arguments":"{\"path\":\"rig.py\",\"content\":\"print(1)\\n\"}"}],
				"usage":{"input_tokens":5,"output_tokens":5,"input_tokens_details":{"cached_tokens":0}}
			}`)
		default:
			writeResponsesStream(w, `{
				"id":"resp_3","status":"completed",
				"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Created rig.py.\n<status>CONCEDE</status>\n"}]}],
				"usage":{"input_tokens":5,"output_tokens":5,"input_tokens_details":{"cached_tokens":0}}
			}`)
		}
	}))
	defer server.Close()

	t.Setenv("TELOS_API_BASE_URL", server.URL)
	t.Setenv("TELOS_API_KEY", "test-key")

	exec := NewNativeExecutor(platform.NewLocalPlatform(workspace), "test/test-model", "high", 0)
	ts := &game.TurnState{Dir: filepath.Join(workspace, ".turn")}
	result := exec.ExecuteTurn("Create `rig.py` in the workspace.", "prover", ts)

	if result.Error != "" {
		t.Fatalf("error: got %q logs=%q", result.Error, result.Logs)
	}
	if result.Status != game.StatusConcede {
		t.Fatalf("status: got %s logs=%q", result.Status, result.Logs)
	}
	if requests != 3 {
		t.Fatalf("expected retry then write then accept (3 requests), got %d", requests)
	}
	if _, err := os.ReadFile(filepath.Join(workspace, "rig.py")); err != nil {
		t.Fatalf("rig.py should exist: %v", err)
	}
}

func TestNativeSystemPromptIsAutonomousAndNeutral(t *testing.T) {
	prompt := nativeSystemPrompt("prover")
	// Load-bearing: act autonomously and surface the role.
	for _, want := range []string{
		"do not ask the operator",
		"Your role for this turn is prover.",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, prompt)
		}
	}
	// Should not leak the eval/benchmark framing into a general coding agent.
	for _, banned := range []string{"benchmark", "sample task"} {
		if strings.Contains(strings.ToLower(prompt), banned) {
			t.Fatalf("system prompt should not mention %q:\n%s", banned, prompt)
		}
	}
}

func TestCompletionGateSignalDecisions(t *testing.T) {
	gate := completionGate{mode: gateSignal}
	cases := []struct {
		name string
		sig  completionSignals
		want string
	}{
		{"empty final", completionSignals{emptyText: true}, "empty_visible_final"},
		{"asks operator", completionSignals{askedOperator: true}, "asked_operator_for_direction"},
		{"bad no-generation", completionSignals{fileDeliverable: true}, "named_deliverable_absent_no_change"},
		{"good no-generation: deliverable exists", completionSignals{fileDeliverable: true, deliverablesMet: true}, ""},
		{"good no-generation: did real work", completionSignals{fileDeliverable: true, mutatedThisTurn: true}, ""},
		{"good no-generation: not a file task", completionSignals{}, ""},
	}
	for _, tc := range cases {
		if got := gate.retryReason(tc.sig); got != tc.want {
			t.Fatalf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

func TestCompletionGateOffNeverRetries(t *testing.T) {
	gate := completionGate{mode: gateOff}
	for _, sig := range []completionSignals{
		{emptyText: true},
		{askedOperator: true},
		{fileDeliverable: true},
	} {
		if reason := gate.retryReason(sig); reason != "" {
			t.Fatalf("off gate should never retry, got %q for %+v", reason, sig)
		}
	}
}

func TestNewCompletionGateReadsEnv(t *testing.T) {
	if newCompletionGate().mode != gateSignal {
		t.Fatal("default mode should be signal")
	}
	t.Setenv("TELOS_NATIVE_COMPLETION_GATE", "off")
	if newCompletionGate().mode != gateOff {
		t.Fatal("env override to off not honored")
	}
}

func TestDeliverablesPresentChecksWorkspace(t *testing.T) {
	workspace := t.TempDir()
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil)
	if tools.deliverablesPresent([]string{"rig.py"}) {
		t.Fatal("missing deliverable should report absent")
	}
	if err := os.WriteFile(filepath.Join(workspace, "rig.py"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !tools.deliverablesPresent([]string{"rig.py"}) {
		t.Fatal("present deliverable should report met")
	}
}

func TestAssignmentFileAnchorsExtractsNamedFiles(t *testing.T) {
	anchors := assignmentFileAnchors("Create `industry.py`, update `src/app.ts`, and read `not-a-file`.")
	if strings.Join(anchors, ",") != "industry.py,src/app.ts" {
		t.Fatalf("anchors: got %q", anchors)
	}
}

func TestAnyMutatingResult(t *testing.T) {
	if anyMutatingResult([]nativeToolResult{{Name: "read"}, {Name: "ls"}}) {
		t.Fatal("read-only tools should not count as mutation")
	}
	if anyMutatingResult([]nativeToolResult{{Name: "write", IsError: true}}) {
		t.Fatal("failed write should not count as mutation")
	}
	if !anyMutatingResult([]nativeToolResult{{Name: "read"}, {Name: "write"}}) {
		t.Fatal("successful write should count as mutation")
	}
}

func TestNativeToolsPathResolution(t *testing.T) {
	workspace := t.TempDir()
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil)

	rejected := tools.execute(context.Background(), nativeToolCall{
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
	written := tools.execute(context.Background(), nativeToolCall{
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

// writeResponsesStream emits a minimal OpenAI Responses SSE stream that
// terminates with a single response.completed event wrapping responseJSON. The
// payload is compacted onto one line so it forms a single SSE data field.
func writeResponsesStream(w http.ResponseWriter, responseJSON string) {
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(`{"type":"response.completed","response":`+responseJSON+"}")); err != nil {
		panic(err)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "data: "+buf.String()+"\n\n")
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/openai/openai-go/responses"
	"github.com/telos-org/telos/internal/game"
	"github.com/telos-org/telos/internal/platform"
)

func TestNativeExecutorRunsChatToolLoopAndWritesWorkspace(t *testing.T) {
	t.Setenv("TELOS_MODEL_STATE_MODE", "server_chain")
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
			if req["model"] != "test/test-model" {
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
		if !strings.Contains(string(body), "path: answer.txt") {
			t.Fatalf("second request should include tool result, got %s", body)
		}
		writeResponsesStream(w, `{
			"id":"resp_2","status":"completed",
			"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Created answer.txt.\n\n<progress_update>wrote answer.txt</progress_update>\n<status>CONCEDE</status>\n"}]}],
			"usage":{"input_tokens":13,"output_tokens":5,"input_tokens_details":{"cached_tokens":0}}
		}`)
	}))
	defer server.Close()

	t.Setenv("TELOS_API_BASE_URL", server.URL)
	t.Setenv("TELOS_API_KEY", "test-key")

	p := platform.NewLocalPlatform(workspace)
	exec := NewNativeExecutor(p, "test/test-model", "high", 0)
	ts := &game.TurnState{Role: "prover", Dir: filepath.Join(workspace, ".turn")}

	result := exec.ExecuteTurn("create answer.txt", ts)

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

func TestNativeExecutorStopsToolLoopWhenTurnTokenBudgetExhausted(t *testing.T) {
	workspace := t.TempDir()
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests > 1 {
			t.Fatalf("executor should stop before a second provider request")
		}
		writeResponsesStream(w, `{
			"id":"resp_1","status":"completed",
			"output":[{"type":"function_call","call_id":"call_1","name":"write","arguments":"{\"path\":\"answer.txt\",\"content\":\"done\\n\"}"}],
			"usage":{"input_tokens":10,"output_tokens":2,"input_tokens_details":{"cached_tokens":0}}
		}`)
	}))
	defer server.Close()

	t.Setenv("TELOS_API_BASE_URL", server.URL)
	t.Setenv("TELOS_API_KEY", "test-key")

	p := platform.NewLocalPlatform(workspace)
	exec := NewNativeExecutor(p, "test/test-model", "high", 0)
	ts := &game.TurnState{
		Role: "prover",
		Dir:  filepath.Join(workspace, ".turn"),
		Budget: game.TurnBudget{
			MaxInputTokens:       10,
			RemainingInputTokens: 10,
		},
	}

	result := exec.ExecuteTurn("create answer.txt", ts)

	if !result.Recoverable {
		t.Fatalf("expected recoverable budget exhaustion, got %+v", result)
	}
	if !strings.Contains(result.Error, "runtime_budget_exhausted:max_input_tokens") {
		t.Fatalf("error: got %q", result.Error)
	}
	if requests != 1 {
		t.Fatalf("requests: got %d", requests)
	}
	if _, err := os.Stat(filepath.Join(workspace, "answer.txt")); !os.IsNotExist(err) {
		t.Fatalf("tool should not run after budget exhaustion, stat err=%v", err)
	}
	errorEvents := sessionLogEventsByType(t, ts.SessionPath(), "error")
	if len(errorEvents) != 1 || errorEvents[0]["error_code"] != string(errRuntimeBudgetExhausted) {
		t.Fatalf("budget error events: %#v", errorEvents)
	}
}

func TestNativeExecutorCapsRequestOutputTokensToRemainingBudget(t *testing.T) {
	workspace := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if got := int(req["max_output_tokens"].(float64)); got != 9 {
			t.Fatalf("max_output_tokens: got %d", got)
		}
		writeResponsesStream(w, `{
			"id":"resp_1","status":"completed",
			"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done.\n<progress_update>reported</progress_update>\n"}]}],
			"usage":{"input_tokens":2,"output_tokens":4,"input_tokens_details":{"cached_tokens":0}}
		}`)
	}))
	defer server.Close()

	t.Setenv("TELOS_API_BASE_URL", server.URL)
	t.Setenv("TELOS_API_KEY", "test-key")

	exec := NewNativeExecutor(platform.NewLocalPlatform(workspace), "test/test-model", "high", 0)
	ts := &game.TurnState{
		Role: "prover",
		Dir:  filepath.Join(workspace, ".turn"),
		Budget: game.TurnBudget{
			MaxOutputTokens:       20,
			RemainingOutputTokens: 9,
		},
	}
	result := exec.ExecuteTurn("Report status.", ts)

	if result.Error != "" {
		t.Fatalf("error: got %q logs=%q", result.Error, result.Logs)
	}
}

func TestEffectiveTurnTimeoutUsesRemainingDurationBudget(t *testing.T) {
	tests := []struct {
		name       string
		configured int
		budget     game.TurnBudget
		want       int
	}{
		{name: "no duration budget keeps configured timeout", configured: 20, budget: game.TurnBudget{}, want: 20},
		{name: "remaining duration supplies disabled timeout", configured: 0, budget: game.TurnBudget{RemainingDurationSec: 12}, want: 12},
		{name: "remaining duration caps configured timeout", configured: 30, budget: game.TurnBudget{RemainingDurationSec: 10}, want: 10},
		{name: "configured timeout remains when smaller", configured: 8, budget: game.TurnBudget{RemainingDurationSec: 10}, want: 8},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := effectiveTurnTimeout(tt.configured, tt.budget); got != tt.want {
				t.Fatalf("timeout: got %d want %d", got, tt.want)
			}
		})
	}
}

func TestNativeExecutorLogsTurnTimeoutError(t *testing.T) {
	workspace := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		writeResponsesStream(w, `{
			"id":"resp_late","status":"completed",
			"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Too late.\n<progress_update>late</progress_update>"}]}],
			"usage":{"input_tokens":1,"output_tokens":1,"input_tokens_details":{"cached_tokens":0}}
		}`)
	}))
	defer server.Close()

	t.Setenv("TELOS_API_BASE_URL", server.URL)
	t.Setenv("TELOS_API_KEY", "test-key")

	exec := NewNativeExecutor(platform.NewLocalPlatform(workspace), "test/test-model", "high", 1)
	ts := &game.TurnState{Role: "prover", Dir: filepath.Join(workspace, ".turn")}
	result := exec.ExecuteTurn("Wait for timeout.", ts)

	if !result.Recoverable || !strings.Contains(result.Error, "provider_timeout:turn_timeout:1") {
		t.Fatalf("expected recoverable turn timeout, got %+v", result)
	}
	errorEvents := sessionLogEventsByType(t, ts.SessionPath(), "error")
	if len(errorEvents) != 1 || errorEvents[0]["error_code"] != string(errProviderTimeout) {
		t.Fatalf("timeout error events: %#v", errorEvents)
	}
}

func TestNativeExecutorLogsStopRequestedError(t *testing.T) {
	workspace := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		writeResponsesStream(w, `{
			"id":"resp_late","status":"completed",
			"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Too late.\n<progress_update>late</progress_update>"}]}],
			"usage":{"input_tokens":1,"output_tokens":1,"input_tokens_details":{"cached_tokens":0}}
		}`)
	}))
	defer server.Close()

	t.Setenv("TELOS_API_BASE_URL", server.URL)
	t.Setenv("TELOS_API_KEY", "test-key")

	exec := NewNativeExecutor(platform.NewLocalPlatform(workspace), "test/test-model", "high", 0)
	ts := &game.TurnState{
		Role:          "prover",
		Dir:           filepath.Join(workspace, ".turn"),
		StopRequested: func() bool { return true },
	}
	result := exec.ExecuteTurn("Wait for stop.", ts)

	if !result.Recoverable || !strings.Contains(result.Error, "stopped:stop_requested") {
		t.Fatalf("expected recoverable stop request, got %+v", result)
	}
	errorEvents := sessionLogEventsByType(t, ts.SessionPath(), "error")
	if len(errorEvents) != 1 || errorEvents[0]["error_code"] != string(errStopped) {
		t.Fatalf("stop error events: %#v", errorEvents)
	}
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
			Schema  string `json:"schema"`
			Version int    `json:"version"`
			Type    string `json:"type"`
			Message struct {
				Role string `json:"role"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("session log line is not valid JSON: %q: %v", line, err)
		}
		if event.Schema != agentSessionSchema || event.Version != 1 {
			t.Fatalf("session event missing schema/version: type=%q schema=%q version=%d", event.Type, event.Schema, event.Version)
		}
		if event.Message.Role == "assistant" {
			sawAssistant = true
		}
	}
	if !sawAssistant {
		t.Fatal("session log should contain an assistant message")
	}
}

func TestNativeExecutorUsesLiteLLMResponseCostHeader(t *testing.T) {
	workspace := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("x-litellm-response-cost", "0.0123")
		writeResponsesStream(w, `{
			"id":"resp_cost","status":"completed",
			"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done.\n\n<progress_update>No workspace changes needed.</progress_update>"}]}],
			"usage":{"input_tokens":31,"output_tokens":9,"input_tokens_details":{"cached_tokens":4}}
		}`)
	}))
	defer server.Close()

	t.Setenv("TELOS_LITELLM_BASE_URL", server.URL)
	t.Setenv("TELOS_LITELLM_API_KEY", "test-key")
	p := platform.NewLocalPlatform(workspace)
	exec := NewNativeExecutor(p, "test/test-model", "high", 0)
	ts := &game.TurnState{Role: "prover", Dir: filepath.Join(workspace, ".turn")}

	result := exec.ExecuteTurn("Report current state.", ts)
	if result.Status != game.StatusContinue {
		t.Fatalf("status: got %s logs=%q", result.Status, result.Logs)
	}
	if result.Stats.CostUnavailable {
		t.Fatalf("cost should be available: %+v", result.Stats)
	}
	if result.Stats.CostUSD != 0.0123 {
		t.Fatalf("cost: got %.4f", result.Stats.CostUSD)
	}
}

func TestNativeExecutorUsesLiteLLMResponseBodyCost(t *testing.T) {
	workspace := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		writeResponsesStream(w, `{
			"id":"resp_cost","status":"completed",
			"response_cost":0.0456,
			"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done.\n\n<progress_update>No workspace changes needed.</progress_update>"}]}],
			"usage":{"input_tokens":31,"output_tokens":9,"input_tokens_details":{"cached_tokens":4}}
		}`)
	}))
	defer server.Close()

	t.Setenv("TELOS_LITELLM_BASE_URL", server.URL)
	t.Setenv("TELOS_LITELLM_API_KEY", "test-key")
	t.Setenv("TELOS_MODEL_PRICING_TABLE", `{"test/test-model":{"input_usd_per_1m_tokens":100,"output_usd_per_1m_tokens":100}}`)
	exec := NewNativeExecutor(platform.NewLocalPlatform(workspace), "test/test-model", "high", 0)
	ts := &game.TurnState{Role: "prover", Dir: filepath.Join(workspace, ".turn")}

	result := exec.ExecuteTurn("Report current state.", ts)
	if result.Error != "" {
		t.Fatalf("error: got %q logs=%q", result.Error, result.Logs)
	}
	if result.Stats.CostUnavailable {
		t.Fatalf("cost should be available: %+v", result.Stats)
	}
	if result.Stats.CostUSD != 0.0456 {
		t.Fatalf("cost: got %.4f", result.Stats.CostUSD)
	}
}

func TestCostFromResponseBodyHandlesNestedLiteLLMMetadata(t *testing.T) {
	cost, ok := costFromResponseBody(`{"metadata":{"litellm_response_cost":"$0.0789"}}`)
	if !ok || cost != 0.0789 {
		t.Fatalf("nested cost: got %.4f ok=%t", cost, ok)
	}
	if cost, ok := costFromResponseBody(`{"metadata":{"response_cost":-1}}`); ok || cost != 0 {
		t.Fatalf("negative cost should be ignored: got %.4f ok=%t", cost, ok)
	}
	if cost, ok := costFromResponseBody(`{"metadata":{"other":1}}`); ok || cost != 0 {
		t.Fatalf("unrelated metadata should be ignored: got %.4f ok=%t", cost, ok)
	}
}

func TestStatsFromResponsesUsageUsesExactConfiguredPricingTable(t *testing.T) {
	t.Setenv("TELOS_MODEL_PRICING_TABLE", `{
		"alias/known": {"input_usd_per_1m_tokens": 2.0, "output_usd_per_1m_tokens": 10.0},
		"alias/other": {"input_usd_per_1m_tokens": 1.0, "output_usd_per_1m_tokens": 1.0}
	}`)

	stats := statsFromResponsesUsage("alias/known", responseUsageForTest(1_000_000, 250_000, 0))

	if stats.CostUnavailable {
		t.Fatalf("cost should be available: %+v", stats)
	}
	if stats.CostUSD != 4.5 {
		t.Fatalf("cost: got %.4f", stats.CostUSD)
	}
}

func TestStatsFromResponsesUsageLeavesUnknownPricingUnavailable(t *testing.T) {
	t.Setenv("TELOS_MODEL_PRICING_TABLE", `{
		"alias/known": {"input_usd_per_1m_tokens": 2.0, "output_usd_per_1m_tokens": 10.0}
	}`)

	stats := statsFromResponsesUsage("alias/missing", responseUsageForTest(1_000, 1_000, 0))

	if !stats.CostUnavailable {
		t.Fatalf("unknown model cost should remain unavailable: %+v", stats)
	}
	if stats.CostUSD != 0 {
		t.Fatalf("unknown model cost should be zero, got %.4f", stats.CostUSD)
	}
}

func responseUsageForTest(inputTokens, outputTokens, cachedTokens int64) responses.ResponseUsage {
	return responses.ResponseUsage{
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		InputTokensDetails: responses.ResponseUsageInputTokensDetails{
			CachedTokens: cachedTokens,
		},
	}
}

func TestNativeSessionLoggerSchemaGolden(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	logger := newNativeSessionLogger(path, dir)

	if err := logger.start(); err != nil {
		t.Fatal(err)
	}
	if err := logger.user("Task body"); err != nil {
		t.Fatal(err)
	}
	if err := logger.budget(10, 1200, game.TurnBudget{
		MaxDurationSec:       30,
		RemainingDurationSec: 24,
		AgentTimeoutSec:      5,
		MaxInputTokens:       100,
		RemainingInputTokens: 80,
	}); err != nil {
		t.Fatal(err)
	}
	if err := logger.modelRequest(modelRequestLogData{
		Sequence:        1,
		PreviousID:      "resp_prev",
		StateMode:       "server_chain",
		Model:           "test-model",
		MaxOutputTokens: 1200,
		ToolCount:       9,
		ReasoningEffort: "high",
	}); err != nil {
		t.Fatal(err)
	}
	if err := logger.toolCall(nativeToolCall{ID: "call_1", Name: "read_file", Arguments: `{"path":"main.go"}`}); err != nil {
		t.Fatal(err)
	}
	if err := logger.tool(nativeToolResult{CallID: "call_1", Name: "read_file", Output: "ok", DurationMS: 12}); err != nil {
		t.Fatal(err)
	}
	if err := logger.retry(1, 2, 250*time.Millisecond, retryableExecutorError(errProviderRateLimited, "rate limited")); err != nil {
		t.Fatal(err)
	}
	if err := logger.protocolCorrection("missing_progress_update", "Use progress_update."); err != nil {
		t.Fatal(err)
	}
	if err := logger.reasoningLeak("<thinking>hidden</thinking>"); err != nil {
		t.Fatal(err)
	}
	if err := logger.skillOpened("review-skill", "/skills/review/SKILL.md", false); err != nil {
		t.Fatal(err)
	}
	if err := logger.skillApplied("review-skill", "/skills/review/SKILL.md"); err != nil {
		t.Fatal(err)
	}
	if err := logger.errorEvent(2, newExecutorError(errAgentIncomplete, "max_output_tokens")); err != nil {
		t.Fatal(err)
	}
	stats := game.TurnStats{InputTokens: 12, OutputTokens: 7, CacheReadTokens: 3, CacheCreationTokens: 2, CostUSD: 0.01}
	if err := logger.modelResponse(1, "resp_1", "completed", stats); err != nil {
		t.Fatal(err)
	}
	if err := logger.assistant("Done.\n\n<progress_update>Updated.</progress_update>", "litellm", "test-model", "completed", stats); err != nil {
		t.Fatal(err)
	}

	got := normalizedSessionLogGolden(t, path)
	want := `[
  {
    "runtime": "telos-native",
    "schema": "telos.agent_session.v1",
    "type": "session",
    "version": 1
  },
  {
    "role": "user",
    "text": "Task body",
    "type": "message",
    "version": 1
  },
  {
    "data": {
      "agent_timeout_sec": 5,
      "max_duration_sec": 30,
      "max_input_tokens": 100,
      "max_output_tokens": 1200,
      "max_tool_loops": 10,
      "remaining_duration_sec": 24,
      "remaining_input_tokens": 80
    },
    "type": "budget",
    "version": 1
  },
  {
    "data": {
      "max_output_tokens": 1200,
      "model": "test-model",
      "previous_response_id": "resp_prev",
      "reasoning_effort": "high",
      "sequence": 1,
      "state_mode": "server_chain",
      "tool_count": 9,
      "tools_enabled": true
    },
    "type": "model_request",
    "version": 1
  },
  {
    "data": {
      "arguments": "{\"path\":\"main.go\"}",
      "tool_call_id": "call_1",
      "tool_name": "read_file"
    },
    "type": "tool_call",
    "version": 1
  },
  {
    "data": {
      "duration_ms": 12,
      "is_error": false,
      "output_bytes": 2,
      "tool_call_id": "call_1",
      "tool_name": "read_file",
      "truncated": false
    },
    "type": "tool_result",
    "version": 1
  },
  {
    "role": "toolResult",
    "text": "ok",
    "tool_call_id": "call_1",
    "tool_name": "read_file",
    "type": "message",
    "version": 1
  },
  {
    "data": {
      "attempt": 2,
      "delay_ms": 250,
      "error": "rate limited",
      "error_code": "provider_rate_limited",
      "sequence": 1
    },
    "type": "retry",
    "version": 1
  },
  {
    "data": {
      "kind": "missing_progress_update",
      "prompt": "Use progress_update."
    },
    "type": "protocol_correction",
    "version": 1
  },
  {
    "data": {
      "removed": "\u003cthinking\u003ehidden\u003c/thinking\u003e"
    },
    "type": "reasoning_sanitized",
    "version": 1
  },
  {
    "data": {
      "name": "review-skill",
      "path": "/skills/review/SKILL.md",
      "truncated": false
    },
    "type": "skill_opened",
    "version": 1
  },
  {
    "data": {
      "name": "review-skill",
      "path": "/skills/review/SKILL.md"
    },
    "type": "skill_applied",
    "version": 1
  },
  {
    "data": {
      "error": "agent_incomplete:max_output_tokens",
      "error_code": "agent_incomplete",
      "retryable": false,
      "sequence": 2
    },
    "type": "error",
    "version": 1
  },
  {
    "data": {
      "response_id": "resp_1",
      "sequence": 1,
      "stop_reason": "completed",
      "usage": {
        "cache_read": 3,
        "cache_write": 2,
        "cost_unavailable": false,
        "cost_usd": 0.01,
        "input": 12,
        "output": 7
      }
    },
    "type": "model_response",
    "version": 1
  },
  {
    "cost": 0.01,
    "model": "test-model",
    "provider": "litellm",
    "role": "assistant",
    "stop_reason": "completed",
    "text": "Done.\n\n\u003cprogress_update\u003eUpdated.\u003c/progress_update\u003e",
    "type": "message",
    "usage": {
      "input": 12,
      "output": 7,
      "cacheRead": 3,
      "cacheWrite": 2,
      "cost": {
        "total": 0.01
      }
    },
    "version": 1
  }
]`
	if got != want {
		t.Fatalf("session log golden mismatch\nwant:\n%s\n\ngot:\n%s", want, got)
	}
}

func TestNativeSessionLoggerRedactsSensitiveToolArguments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	logger := newNativeSessionLogger(path, dir)

	if err := logger.start(); err != nil {
		t.Fatal(err)
	}
	if err := logger.toolCall(nativeToolCall{
		ID:   "call_secret",
		Name: "bash",
		Arguments: `{
			"command":"echo ok",
			"max_tokens":4,
			"api_key":"sk-test",
			"env":{"TOKEN":"secret-token","SAFE":"visible"},
			"headers":{"Authorization":"Bearer secret"}
		}`,
	}); err != nil {
		t.Fatal(err)
	}

	events := sessionLogEventsByType(t, path, "tool_call")
	if len(events) != 1 {
		t.Fatalf("tool_call events: %#v", events)
	}
	args, _ := events[0]["arguments"].(string)
	for _, leaked := range []string{"sk-test", "secret-token", "Bearer secret"} {
		if strings.Contains(args, leaked) {
			t.Fatalf("arguments leaked %q: %s", leaked, args)
		}
	}
	for _, want := range []string{`"api_key":"[REDACTED]"`, `"TOKEN":"[REDACTED]"`, `"Authorization":"[REDACTED]"`, `"SAFE":"visible"`, `"max_tokens":4`} {
		if !strings.Contains(args, want) {
			t.Fatalf("arguments missing %q: %s", want, args)
		}
	}
}

func normalizedSessionLogGolden(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var projected []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var event sessionEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("parse session event: %v", err)
		}
		item := map[string]any{"type": event.Type}
		if event.Type == "session" && event.Schema != "" {
			item["schema"] = event.Schema
		}
		if event.Version != 0 {
			item["version"] = event.Version
		}
		if event.Runtime != "" {
			item["runtime"] = event.Runtime
		}
		if event.Data != nil {
			item["data"] = event.Data
		}
		if event.Message != nil {
			item["role"] = event.Message.Role
			if event.Message.Provider != "" {
				item["provider"] = event.Message.Provider
			}
			if event.Message.Model != "" {
				item["model"] = event.Message.Model
			}
			if event.Message.StopReason != "" {
				item["stop_reason"] = event.Message.StopReason
			}
			if event.Message.ToolCallID != "" {
				item["tool_call_id"] = event.Message.ToolCallID
			}
			if event.Message.ToolName != "" {
				item["tool_name"] = event.Message.ToolName
			}
			if event.Message.IsError {
				item["is_error"] = true
			}
			if text := messageText(event.Message); text != "" {
				item["text"] = text
			}
			if event.Message.Usage != nil {
				item["usage"] = event.Message.Usage
				if event.Message.Usage.Cost != nil {
					item["cost"] = event.Message.Usage.Cost.Total
				}
			}
		}
		projected = append(projected, item)
	}
	out, err := json.MarshalIndent(projected, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestResolveNativeProviderUsesLiteLLMExactModelPassThrough(t *testing.T) {
	t.Setenv("TELOS_LITELLM_BASE_URL", "https://litellm.example.com/v1/")
	t.Setenv("TELOS_LITELLM_API_KEY", "test-key")

	for _, model := range []string{
		"openai/gpt-5.1",
		"anthropic/claude-sonnet-4.5",
		"sail-research/foo/bar",
		"my-arbitrary-litellm-alias",
	} {
		cfg, err := resolveNativeProvider(model)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Provider != "litellm" {
			t.Fatalf("provider: got %q", cfg.Provider)
		}
		if cfg.Model != model {
			t.Fatalf("model should pass through unchanged: got %q want %q", cfg.Model, model)
		}
		if cfg.BaseURL != "https://litellm.example.com/v1" {
			t.Fatalf("base URL: got %q", cfg.BaseURL)
		}
	}
}

func TestResolveNativeProviderReportsClearConfigErrors(t *testing.T) {
	clearGatewayEnv := func(t *testing.T) {
		t.Helper()
		for _, name := range []string{
			"TELOS_LITELLM_BASE_URL",
			"TELOS_API_BASE_URL",
			"TELOS_BASE_URL",
			"TELOS_LITELLM_API_KEY",
			"TELOS_API_KEY",
		} {
			t.Setenv(name, "")
		}
	}

	t.Run("missing model", func(t *testing.T) {
		clearGatewayEnv(t)
		_, err := resolveNativeProvider(" ")
		if err == nil || err.Error() != "model is required" {
			t.Fatalf("error: %v", err)
		}
	})

	t.Run("missing base url", func(t *testing.T) {
		clearGatewayEnv(t)
		t.Setenv("TELOS_LITELLM_API_KEY", "test-key")
		_, err := resolveNativeProvider("test/model")
		if err == nil || !strings.Contains(err.Error(), "TELOS_LITELLM_BASE_URL is required") {
			t.Fatalf("error: %v", err)
		}
	})

	t.Run("missing api key", func(t *testing.T) {
		clearGatewayEnv(t)
		t.Setenv("TELOS_LITELLM_BASE_URL", "https://litellm.example.com/v1")
		_, err := resolveNativeProvider("test/model")
		if err == nil || !strings.Contains(err.Error(), "TELOS_LITELLM_API_KEY is required") {
			t.Fatalf("error: %v", err)
		}
	})
}

func TestNativeExecutorSurfacesProviderConfigError(t *testing.T) {
	for _, name := range []string{
		"TELOS_LITELLM_BASE_URL",
		"TELOS_API_BASE_URL",
		"TELOS_BASE_URL",
		"TELOS_LITELLM_API_KEY",
		"TELOS_API_KEY",
	} {
		t.Setenv(name, "")
	}

	workspace := t.TempDir()
	exec := NewNativeExecutor(platform.NewLocalPlatform(workspace), "test/model", "high", 0)
	ts := &game.TurnState{Role: "prover", Dir: filepath.Join(workspace, ".turn")}
	result := exec.ExecuteTurn("Say hello.", ts)

	if result.Recoverable {
		t.Fatalf("expected terminal config error, got %+v", result)
	}
	if !strings.Contains(result.Error, "config:TELOS_LITELLM_BASE_URL is required") {
		t.Fatalf("error: got %q", result.Error)
	}
	errorEvents := sessionLogEventsByType(t, ts.SessionPath(), "error")
	if len(errorEvents) != 1 || errorEvents[0]["error_code"] != string(errConfig) || errorEvents[0]["sequence"] != float64(0) {
		t.Fatalf("config error events: %#v", errorEvents)
	}
}

func TestResolveNativeProviderAppliesModelCapabilityProfile(t *testing.T) {
	t.Setenv("TELOS_LITELLM_BASE_URL", "https://litellm.example.com/v1/")
	t.Setenv("TELOS_LITELLM_API_KEY", "test-key")
	t.Setenv("TELOS_MODEL_CAPABILITY_PROFILE", `{"state_mode":"stateless_history","max_output_tokens":4096,"supports_reasoning":false,"supports_function_calling":false,"strict_protocol":true}`)

	cfg, err := resolveNativeProvider("custom/model")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Capability.StateMode != "stateless_history" {
		t.Fatalf("state mode: got %q", cfg.Capability.StateMode)
	}
	if cfg.Capability.MaxOutputTokens != 4096 {
		t.Fatalf("max output tokens: got %d", cfg.Capability.MaxOutputTokens)
	}
	if cfg.Capability.SupportsReasoning == nil || *cfg.Capability.SupportsReasoning {
		t.Fatalf("supports reasoning: got %#v", cfg.Capability.SupportsReasoning)
	}
	if cfg.Capability.SupportsFunctionCalling == nil || *cfg.Capability.SupportsFunctionCalling {
		t.Fatalf("supports function calling: got %#v", cfg.Capability.SupportsFunctionCalling)
	}
	if !cfg.Capability.StrictProtocol {
		t.Fatal("strict protocol should be true")
	}
}

func TestResolveNativeProviderCapabilityEnvOverridesProfile(t *testing.T) {
	t.Setenv("TELOS_LITELLM_BASE_URL", "https://litellm.example.com/v1/")
	t.Setenv("TELOS_LITELLM_API_KEY", "test-key")
	t.Setenv("TELOS_MODEL_CAPABILITY_PROFILE", `{"state_mode":"bad","max_output_tokens":4096,"supports_reasoning":true}`)
	t.Setenv("TELOS_MODEL_STATE_MODE", "stateless_history")
	t.Setenv("TELOS_MODEL_MAX_OUTPUT_TOKENS", "2048")
	t.Setenv("TELOS_MODEL_SUPPORTS_REASONING", "false")
	t.Setenv("TELOS_MODEL_SUPPORTS_FUNCTION_CALLING", "false")

	cfg, err := resolveNativeProvider("custom/model")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Capability.StateMode != "stateless_history" {
		t.Fatalf("state mode: got %q", cfg.Capability.StateMode)
	}
	if cfg.Capability.MaxOutputTokens != 2048 {
		t.Fatalf("max output tokens: got %d", cfg.Capability.MaxOutputTokens)
	}
	if cfg.Capability.SupportsReasoning == nil || *cfg.Capability.SupportsReasoning {
		t.Fatalf("supports reasoning: got %#v", cfg.Capability.SupportsReasoning)
	}
	if cfg.Capability.SupportsFunctionCalling == nil || *cfg.Capability.SupportsFunctionCalling {
		t.Fatalf("supports function calling: got %#v", cfg.Capability.SupportsFunctionCalling)
	}
}

func TestEffectiveMaxToolLoopsManifestWins(t *testing.T) {
	// No env override exists: the manifest budget is the sole source of truth
	// above the harness default.
	if got := effectiveMaxToolLoops(game.TurnBudget{}); got != defaultMaxToolLoops {
		t.Fatalf("default max tool loops: got %d want %d", got, defaultMaxToolLoops)
	}
	if got := effectiveMaxToolLoops(game.TurnBudget{MaxToolLoops: 7}); got != 7 {
		t.Fatalf("budget max tool loops should win over default: got %d", got)
	}
}

func TestEffectiveMaxOutputTokensPrecedence(t *testing.T) {
	// Precedence (most restrictive wins): manifest budget remaining, then model
	// capability max, then harness default. No env base ceiling.
	cfg := nativeProviderConfig{}
	if got := effectiveMaxOutputTokens(cfg, game.TurnBudget{}); got != defaultMaxOutputTokens {
		t.Fatalf("default effective max output tokens: got %d want %d", got, defaultMaxOutputTokens)
	}

	cfg.Capability.MaxOutputTokens = 8000
	if got := effectiveMaxOutputTokens(cfg, game.TurnBudget{}); got != 8000 {
		t.Fatalf("capability max output tokens should cap down: got %d", got)
	}

	if got := effectiveMaxOutputTokens(cfg, game.TurnBudget{RemainingOutputTokens: 3000}); got != 3000 {
		t.Fatalf("remaining budget tokens should cap below capability: got %d", got)
	}

	// With no capability cap, the budget caps down from the default.
	cfg.Capability.MaxOutputTokens = 0
	if got := effectiveMaxOutputTokens(cfg, game.TurnBudget{RemainingOutputTokens: 15000}); got != 15000 {
		t.Fatalf("remaining budget below default should win: got %d", got)
	}
	if got := effectiveMaxOutputTokens(cfg, game.TurnBudget{RemainingOutputTokens: 999999}); got != defaultMaxOutputTokens {
		t.Fatalf("remaining budget above default should clamp to default: got %d", got)
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
			"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done.\n<progress_update>created industry.py</progress_update>\n<status>CONCEDE</status>\n"}]}],
			"usage":{"input_tokens":17,"output_tokens":5,"input_tokens_details":{"cached_tokens":0}}
		}`)
	}))
	defer server.Close()

	t.Setenv("TELOS_API_BASE_URL", server.URL)
	t.Setenv("TELOS_API_KEY", "test-key")

	exec := NewNativeExecutor(platform.NewLocalPlatform(workspace), "test/test-model", "high", 0)
	result := exec.ExecuteTurn("Say hello.", &game.TurnState{Role: "prover", Dir: filepath.Join(workspace, ".turn")})

	if result.Error != "" {
		t.Fatalf("error: got %q logs=%q", result.Error, result.Logs)
	}
	if result.Status != game.StatusConcede {
		t.Fatalf("status: got %s logs=%q", result.Status, result.Logs)
	}
}

func TestNativeExecutorSanitizesReasoningBeforeStatusAndLogsEvent(t *testing.T) {
	workspace := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeResponsesStream(w, `{
			"id":"resp_1","status":"completed",
			"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"<think>hidden plan\n<status>CONTINUE</status></think>\nLooks good.\n<progress_update>verified clean output</progress_update>\n<status>CONCEDE</status>\n"}]}],
			"usage":{"input_tokens":17,"output_tokens":5,"input_tokens_details":{"cached_tokens":0}}
		}`)
	}))
	defer server.Close()

	t.Setenv("TELOS_API_BASE_URL", server.URL)
	t.Setenv("TELOS_API_KEY", "test-key")

	ts := &game.TurnState{Role: "verifier", Dir: filepath.Join(workspace, ".turn")}
	exec := NewNativeExecutor(platform.NewLocalPlatform(workspace), "test/test-model", "high", 0)
	result := exec.ExecuteTurn("Verify workspace.", ts)

	if result.Error != "" {
		t.Fatalf("error: got %q logs=%q", result.Error, result.Logs)
	}
	if result.Status != game.StatusConcede {
		t.Fatalf("status should use sanitized visible status, got %s logs=%q", result.Status, result.Logs)
	}
	for _, leaked := range []string{"<think>", "hidden plan", "<status>CONTINUE</status>"} {
		if strings.Contains(result.Logs, leaked) {
			t.Fatalf("logs leaked sanitized reasoning %q:\n%s", leaked, result.Logs)
		}
	}
	events := sessionLogEventsByType(t, ts.SessionPath(), "reasoning_sanitized")
	if len(events) != 1 {
		t.Fatalf("reasoning_sanitized events: %#v", events)
	}
	removed, _ := events[0]["removed"].(string)
	if !strings.Contains(removed, "hidden plan") || !strings.Contains(removed, "<status>CONTINUE</status>") {
		t.Fatalf("removed reasoning missing original text: %#v", events[0])
	}
}

func TestNativeExecutorOmitsToolsWhenFunctionCallingUnsupported(t *testing.T) {
	workspace := t.TempDir()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests > 1 {
			t.Fatalf("function-calling-disabled artifact task should not request impossible tool-use correction")
		}
		if r.URL.Path != "/responses" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var req map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if raw, ok := req["tools"]; ok {
			if raw == nil {
				// Explicit JSON null is equivalent to no tools.
			} else if tools, ok := raw.([]interface{}); ok {
				if len(tools) != 0 {
					t.Fatalf("tools should be omitted or empty when function calling is unsupported: %s", mustJSON(req))
				}
			} else {
				t.Fatalf("tools should be omitted or empty when function calling is unsupported: %s", mustJSON(req))
			}
		}
		writeResponsesStream(w, `{
			"id":"resp_1","status":"completed",
			"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done.\n<progress_update>reported</progress_update>\n"}]}],
			"usage":{"input_tokens":5,"output_tokens":5,"input_tokens_details":{"cached_tokens":0}}
		}`)
	}))
	defer server.Close()

	t.Setenv("TELOS_API_BASE_URL", server.URL)
	t.Setenv("TELOS_API_KEY", "test-key")
	t.Setenv("TELOS_MODEL_SUPPORTS_FUNCTION_CALLING", "false")

	exec := NewNativeExecutor(platform.NewLocalPlatform(workspace), "test/test-model", "high", 0)
	result := exec.ExecuteTurn("Inspect the workspace files and report status.", &game.TurnState{Role: "prover", Dir: filepath.Join(workspace, ".turn")})

	if result.Error != "" {
		t.Fatalf("error: got %q logs=%q", result.Error, result.Logs)
	}
	if requests != 1 {
		t.Fatalf("requests: got %d", requests)
	}
}

func TestNativeExecutorNudgesEmptyFinalOnce(t *testing.T) {
	workspace := t.TempDir()
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			// Tool-less final with no visible text -> nudge once.
			writeResponsesStream(w, `{
				"id":"resp_1","status":"completed",
				"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"   "}]}],
				"usage":{"input_tokens":5,"output_tokens":5,"input_tokens_details":{"cached_tokens":0}}
			}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "no visible result") {
			t.Fatalf("second request should carry the correction prompt, got %s", body)
		}
		writeResponsesStream(w, `{
			"id":"resp_2","status":"completed",
			"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done.\n<progress_update>summarized workspace</progress_update>\n<status>CONCEDE</status>\n"}]}],
			"usage":{"input_tokens":5,"output_tokens":5,"input_tokens_details":{"cached_tokens":0}}
		}`)
	}))
	defer server.Close()

	t.Setenv("TELOS_API_BASE_URL", server.URL)
	t.Setenv("TELOS_API_KEY", "test-key")

	exec := NewNativeExecutor(platform.NewLocalPlatform(workspace), "test/test-model", "high", 0)
	ts := &game.TurnState{Role: "prover", Dir: filepath.Join(workspace, ".turn")}
	result := exec.ExecuteTurn("Say hello.", ts)

	if result.Error != "" {
		t.Fatalf("error: got %q logs=%q", result.Error, result.Logs)
	}
	if result.Status != game.StatusConcede {
		t.Fatalf("status: got %s logs=%q", result.Status, result.Logs)
	}
	if requests != 2 {
		t.Fatalf("expected empty final then nudge then accept (2 requests), got %d", requests)
	}
}

func TestNativeExecutorStrictProtocolRejectsMalformedProgressUpdate(t *testing.T) {
	workspace := t.TempDir()
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch requests {
		case 1:
			writeResponsesStream(w, `{
				"id":"resp_1","status":"completed",
				"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done.\n<progress_update>first</progress_update>\n<progress_update>second</progress_update>"}]}],
				"usage":{"input_tokens":5,"output_tokens":5,"input_tokens_details":{"cached_tokens":0}}
			}`)
		case 2:
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "exactly one") || !strings.Contains(string(body), "progress_update") {
				t.Fatalf("correction request missing strict progress guidance: %s", body)
			}
			writeResponsesStream(w, `{
				"id":"resp_2","status":"completed",
				"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done.\n<progress_update>reported</progress_update>"}]}],
				"usage":{"input_tokens":5,"output_tokens":5,"input_tokens_details":{"cached_tokens":0}}
			}`)
		default:
			t.Fatalf("unexpected request %d", requests)
		}
	}))
	defer server.Close()

	t.Setenv("TELOS_API_BASE_URL", server.URL)
	t.Setenv("TELOS_API_KEY", "test-key")
	t.Setenv("TELOS_MODEL_STRICT_PROTOCOL", "true")

	exec := NewNativeExecutor(platform.NewLocalPlatform(workspace), "test/test-model", "high", 0)
	result := exec.ExecuteTurn("Report status.", &game.TurnState{Role: "prover", Dir: filepath.Join(workspace, ".turn")})

	if result.Error != "" {
		t.Fatalf("error: got %q logs=%q", result.Error, result.Logs)
	}
	if requests != 2 {
		t.Fatalf("requests: got %d", requests)
	}
}

func TestNativeExecutorCorrectsVerifierMissingStatusOnce(t *testing.T) {
	workspace := t.TempDir()
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch requests {
		case 1:
			writeResponsesStream(w, `{
				"id":"resp_1","status":"completed",
				"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Looks good."}]}],
				"usage":{"input_tokens":5,"output_tokens":5,"input_tokens_details":{"cached_tokens":0}}
			}`)
		case 2:
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "missing the required final") {
				t.Fatalf("correction request missing status guidance: %s", body)
			}
			writeResponsesStream(w, `{
				"id":"resp_2","status":"completed",
				"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Looks good.\n<status>CONCEDE</status>"}]}],
				"usage":{"input_tokens":5,"output_tokens":5,"input_tokens_details":{"cached_tokens":0}}
			}`)
		default:
			t.Fatalf("unexpected request %d", requests)
		}
	}))
	defer server.Close()

	t.Setenv("TELOS_API_BASE_URL", server.URL)
	t.Setenv("TELOS_API_KEY", "test-key")

	exec := NewNativeExecutor(platform.NewLocalPlatform(workspace), "test/test-model", "high", 0)
	result := exec.ExecuteTurn("Verify workspace.", &game.TurnState{Role: "verifier", Dir: filepath.Join(workspace, ".turn")})

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

func TestNativeExecutorCorrectsVerifierInvalidStatusOnce(t *testing.T) {
	workspace := t.TempDir()
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch requests {
		case 1:
			writeResponsesStream(w, `{
				"id":"resp_1","status":"completed",
				"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Unsure.\n<status>MAYBE</status>"}]}],
				"usage":{"input_tokens":5,"output_tokens":5,"input_tokens_details":{"cached_tokens":0}}
			}`)
		case 2:
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "CONTINUE") || !strings.Contains(string(body), "CONCEDE") {
				t.Fatalf("correction request missing valid status guidance: %s", body)
			}
			writeResponsesStream(w, `{
				"id":"resp_2","status":"completed",
				"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Looks good.\n<status>CONCEDE</status>"}]}],
				"usage":{"input_tokens":5,"output_tokens":5,"input_tokens_details":{"cached_tokens":0}}
			}`)
		default:
			t.Fatalf("unexpected request %d", requests)
		}
	}))
	defer server.Close()

	t.Setenv("TELOS_API_BASE_URL", server.URL)
	t.Setenv("TELOS_API_KEY", "test-key")

	exec := NewNativeExecutor(platform.NewLocalPlatform(workspace), "test/test-model", "high", 0)
	result := exec.ExecuteTurn("Verify workspace.", &game.TurnState{Role: "verifier", Dir: filepath.Join(workspace, ".turn")})

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

func TestNativeExecutorLogsTerminalProtocolError(t *testing.T) {
	workspace := t.TempDir()
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		writeResponsesStream(w, `{
			"id":"resp_bad","status":"completed",
			"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Still no status."}]}],
			"usage":{"input_tokens":5,"output_tokens":5,"input_tokens_details":{"cached_tokens":0}}
		}`)
	}))
	defer server.Close()

	t.Setenv("TELOS_API_BASE_URL", server.URL)
	t.Setenv("TELOS_API_KEY", "test-key")

	exec := NewNativeExecutor(platform.NewLocalPlatform(workspace), "test/test-model", "high", 0)
	ts := &game.TurnState{Role: "verifier", Dir: filepath.Join(workspace, ".turn")}
	result := exec.ExecuteTurn("Verify workspace.", ts)

	if !result.Recoverable || !strings.Contains(result.Error, "agent_protocol:missing_status") {
		t.Fatalf("expected recoverable protocol error, got %+v", result)
	}
	if requests != 2 {
		t.Fatalf("requests: got %d", requests)
	}
	errorEvents := sessionLogEventsByType(t, ts.SessionPath(), "error")
	if len(errorEvents) != 1 || errorEvents[0]["error_code"] != string(errAgentProtocol) || errorEvents[0]["error"] != "agent_protocol:missing_status" {
		t.Fatalf("protocol error events: %#v", errorEvents)
	}
}

func TestNativeExecutorCorrectsMalformedReviewModeBlocksOnce(t *testing.T) {
	workspace := t.TempDir()
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch requests {
		case 1:
			writeResponsesStream(w, `{
				"id":"resp_1","status":"completed",
				"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"<review>Looks good.</review>"}]}],
				"usage":{"input_tokens":5,"output_tokens":5,"input_tokens_details":{"cached_tokens":0}}
			}`)
		case 2:
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "review-mode response") || !strings.Contains(string(body), "summary") {
				t.Fatalf("correction request missing review-mode guidance: %s", body)
			}
			writeResponsesStream(w, `{
				"id":"resp_2","status":"completed",
				"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"<review>Looks good.</review>\n<summary>No issues.</summary>"}]}],
				"usage":{"input_tokens":5,"output_tokens":5,"input_tokens_details":{"cached_tokens":0}}
			}`)
		default:
			t.Fatalf("unexpected request %d", requests)
		}
	}))
	defer server.Close()

	t.Setenv("TELOS_API_BASE_URL", server.URL)
	t.Setenv("TELOS_API_KEY", "test-key")

	exec := NewNativeExecutor(platform.NewLocalPlatform(workspace), "test/test-model", "high", 0)
	task := "Review workspace."
	result := exec.ExecuteTurn(task, &game.TurnState{Role: "verifier", Dir: filepath.Join(workspace, ".turn"), ProtocolMode: "review"})

	if result.Error != "" {
		t.Fatalf("error: got %q logs=%q", result.Error, result.Logs)
	}
	if !strings.Contains(result.Logs, "<summary>No issues.</summary>") {
		t.Fatalf("review-mode final logs: %q", result.Logs)
	}
	if requests != 2 {
		t.Fatalf("requests: got %d", requests)
	}
}

func TestNativeExecutorRetriesMalformedReviewBlocksBeyondOnce(t *testing.T) {
	workspace := t.TempDir()
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch requests {
		case 1, 2:
			// Two consecutive malformed replies (review block only, no summary).
			writeResponsesStream(w, `{
				"id":"resp_x","status":"completed",
				"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"<review>criteria,score\nclarity,8.0/10</review>"}]}],
				"usage":{"input_tokens":5,"output_tokens":5,"input_tokens_details":{"cached_tokens":0}}
			}`)
		case 3:
			writeResponsesStream(w, `{
				"id":"resp_ok","status":"completed",
				"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"<review>criteria,score\nclarity,8.0/10</review>\n<summary>No issues.</summary>"}]}],
				"usage":{"input_tokens":5,"output_tokens":5,"input_tokens_details":{"cached_tokens":0}}
			}`)
		default:
			t.Fatalf("unexpected request %d", requests)
		}
	}))
	defer server.Close()

	t.Setenv("TELOS_API_BASE_URL", server.URL)
	t.Setenv("TELOS_API_KEY", "test-key")

	exec := NewNativeExecutor(platform.NewLocalPlatform(workspace), "test/test-model", "high", 0)
	result := exec.ExecuteTurn("Review workspace.", &game.TurnState{Role: "verifier", Dir: filepath.Join(workspace, ".turn"), ProtocolMode: "review"})

	if result.Error != "" {
		t.Fatalf("expected recovery after two malformed replies, got error %q logs=%q", result.Error, result.Logs)
	}
	if requests != 3 {
		t.Fatalf("expected 3 requests (2 corrections then success), got %d", requests)
	}
}

func TestProtocolCorrectionReviewAndProgressTolerance(t *testing.T) {
	cases := []struct {
		name         string
		role         string
		protocolMode string
		text         string
		wantKey      string
	}{
		{
			name:         "review valid lowercase",
			role:         "verifier",
			protocolMode: "review",
			text:         "<review>criteria,score\nclarity,8.0/10</review>\n<summary>fine</summary>",
			wantKey:      "",
		},
		{
			name:         "review tolerates case and attributes",
			role:         "verifier",
			protocolMode: "review",
			text:         "<Review type=\"rubric\">criteria,score\nclarity,8.0/10</Review>\n<SUMMARY id=\"s1\">fine</SUMMARY>",
			wantKey:      "",
		},
		{
			name:         "review tolerates a stray tag mention",
			role:         "verifier",
			protocolMode: "review",
			text:         "I will now emit the review block.\n<review>criteria,score\nclarity,8.0/10</review>\n<summary>fine</summary>",
			wantKey:      "",
		},
		{
			name:         "review rejects duplicate blocks",
			role:         "verifier",
			protocolMode: "review",
			text:         "<review>criteria,score\na,1.0/10</review>\n<review>criteria,score\nb,2.0/10</review>\n<summary>fine</summary>",
			wantKey:      "malformed_review_blocks",
		},
		{
			name:         "review rejects missing summary",
			role:         "verifier",
			protocolMode: "review",
			text:         "<review>criteria,score\nclarity,8.0/10</review>",
			wantKey:      "malformed_review_blocks",
		},
		{
			name:    "progress tolerates case and attributes",
			role:    "prover",
			text:    "Done.\n<Progress_Update note=\"final\">changed main.go</Progress_Update>",
			wantKey: "",
		},
		{
			name:    "progress missing block",
			role:    "prover",
			text:    "Done, nothing else to add.",
			wantKey: "missing_progress_update",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, key := protocolCorrectionForStrict(tc.role, tc.protocolMode, "Review workspace.", tc.text, true, false, true)
			if key != tc.wantKey {
				t.Fatalf("key: got %q want %q", key, tc.wantKey)
			}
		})
	}
}

func TestNativeExecutorRequiresVerifierToOpenRequiredRubricBeforeConceding(t *testing.T) {
	workspace := t.TempDir()
	skillDir := filepath.Join(t.TempDir(), "review-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skillPath := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillPath, []byte("---\nname: review-skill\ndescription: Review rubric\n---\n# Rubric\n\nCheck evidence.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch requests {
		case 1:
			writeResponsesStream(w, `{
				"id":"resp_1","status":"completed",
				"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Looks good.\n<status>CONCEDE</status>"}]}],
				"usage":{"input_tokens":5,"output_tokens":5,"input_tokens_details":{"cached_tokens":0}}
			}`)
		case 2:
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "Before conceding") || !strings.Contains(string(body), "review-skill") {
				t.Fatalf("correction request missing rubric guidance: %s", body)
			}
			writeResponsesStream(w, `{
				"id":"resp_2","status":"completed",
				"output":[{"type":"function_call","call_id":"call_1","name":"skill","arguments":"{\"action\":\"read\",\"name\":\"review-skill\"}"}],
				"usage":{"input_tokens":5,"output_tokens":5,"input_tokens_details":{"cached_tokens":0}}
			}`)
		case 3:
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "# Rubric") || !strings.Contains(string(body), "Check evidence.") {
				t.Fatalf("third request should include rubric tool result: %s", body)
			}
			writeResponsesStream(w, `{
				"id":"resp_3","status":"completed",
				"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Rubric passes.\n<status>CONCEDE</status>"}]}],
				"usage":{"input_tokens":5,"output_tokens":5,"input_tokens_details":{"cached_tokens":0}}
			}`)
		default:
			t.Fatalf("unexpected request %d", requests)
		}
	}))
	defer server.Close()

	t.Setenv("TELOS_API_BASE_URL", server.URL)
	t.Setenv("TELOS_API_KEY", "test-key")

	task := "Verify workspace."
	exec := NewNativeExecutor(platform.NewLocalPlatform(workspace), "test/test-model", "high", 0)
	result := exec.ExecuteTurn(task, &game.TurnState{
		Role:         "verifier",
		Dir:          filepath.Join(workspace, ".turn"),
		ProtocolMode: "pvg",
		Skills: []game.TurnSkill{{
			Name:        "review-skill",
			Description: "Review rubric",
			SkillPath:   filepath.ToSlash(skillPath),
			Required:    true,
		}},
	})

	if result.Error != "" {
		t.Fatalf("error: got %q logs=%q", result.Error, result.Logs)
	}
	if result.Status != game.StatusConcede {
		t.Fatalf("status: got %s logs=%q", result.Status, result.Logs)
	}
	if requests != 3 {
		t.Fatalf("requests: got %d", requests)
	}
}

func TestNativeExecutorRejectsIncompleteFinal(t *testing.T) {
	workspace := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeResponsesIncompleteStream(w, `{
			"id":"resp_1","status":"incomplete",
			"incomplete_details":{"reason":"max_output_tokens"},
			"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"partial"}]}],
			"usage":{"input_tokens":5,"output_tokens":5,"input_tokens_details":{"cached_tokens":0}}
		}`)
	}))
	defer server.Close()

	t.Setenv("TELOS_API_BASE_URL", server.URL)
	t.Setenv("TELOS_API_KEY", "test-key")

	exec := NewNativeExecutor(platform.NewLocalPlatform(workspace), "test/test-model", "high", 0)
	result := exec.ExecuteTurn("Say hello.", &game.TurnState{Role: "prover", Dir: filepath.Join(workspace, ".turn")})

	if !result.Recoverable {
		t.Fatalf("expected recoverable incomplete response, got %+v", result)
	}
	if !strings.Contains(result.Error, "agent_incomplete:max_output_tokens") {
		t.Fatalf("error: got %q", result.Error)
	}
	if result.Stats.InputTokens != 5 || result.Stats.OutputTokens != 5 {
		t.Fatalf("incomplete usage should be preserved: %+v", result.Stats)
	}
	modelEvents := sessionLogEventsByType(t, filepath.Join(workspace, ".turn", "session.jsonl"), "model_response")
	if len(modelEvents) != 1 || modelEvents[0]["stop_reason"] != "max_output_tokens" {
		t.Fatalf("model_response events: %#v", modelEvents)
	}
	errorEvents := sessionLogEventsByType(t, filepath.Join(workspace, ".turn", "session.jsonl"), "error")
	if len(errorEvents) != 1 || errorEvents[0]["error_code"] != string(errAgentIncomplete) {
		t.Fatalf("error events: %#v", errorEvents)
	}
}

func TestNativeExecutorContinuesIncompleteToolCallResponse(t *testing.T) {
	workspace := t.TempDir()
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch requests {
		case 1:
			writeResponsesIncompleteStream(w, `{
				"id":"resp_1","status":"incomplete",
				"incomplete_details":{"reason":"max_output_tokens"},
				"output":[{"type":"function_call","call_id":"call_1","name":"write","arguments":"{\"path\":\"answer.txt\",\"content\":\"done\\n\"}"}],
				"usage":{"input_tokens":5,"output_tokens":5,"input_tokens_details":{"cached_tokens":0}}
			}`)
		case 2:
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "path: answer.txt") {
				t.Fatalf("second request should include tool result after incomplete tool call response, got %s", body)
			}
			writeResponsesStream(w, `{
				"id":"resp_2","status":"completed",
				"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Created answer.txt.\n<progress_update>wrote answer</progress_update>"}]}],
				"usage":{"input_tokens":5,"output_tokens":5,"input_tokens_details":{"cached_tokens":0}}
			}`)
		default:
			t.Fatalf("unexpected request %d", requests)
		}
	}))
	defer server.Close()

	t.Setenv("TELOS_API_BASE_URL", server.URL)
	t.Setenv("TELOS_API_KEY", "test-key")

	exec := NewNativeExecutor(platform.NewLocalPlatform(workspace), "test/test-model", "high", 0)
	result := exec.ExecuteTurn("create answer.txt", &game.TurnState{Role: "prover", Dir: filepath.Join(workspace, ".turn")})

	if result.Error != "" {
		t.Fatalf("error: got %q logs=%q", result.Error, result.Logs)
	}
	data, err := os.ReadFile(filepath.Join(workspace, "answer.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "done\n" {
		t.Fatalf("answer.txt: got %q", string(data))
	}
	if requests != 2 {
		t.Fatalf("requests: got %d", requests)
	}
}

func TestNativeExecutorRejectsIncompleteToolCallWithPartialArguments(t *testing.T) {
	workspace := t.TempDir()
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		writeResponsesIncompleteStream(w, `{
			"id":"resp_1","status":"incomplete",
			"incomplete_details":{"reason":"max_output_tokens"},
			"output":[{"type":"function_call","call_id":"call_1","name":"write","arguments":"{\"path\":\"answer.txt\",\"content\":\"done\\n\""}],
			"usage":{"input_tokens":5,"output_tokens":5,"input_tokens_details":{"cached_tokens":0}}
		}`)
	}))
	defer server.Close()

	t.Setenv("TELOS_API_BASE_URL", server.URL)
	t.Setenv("TELOS_API_KEY", "test-key")

	exec := NewNativeExecutor(platform.NewLocalPlatform(workspace), "test/test-model", "high", 0)
	result := exec.ExecuteTurn("create answer.txt", &game.TurnState{Role: "prover", Dir: filepath.Join(workspace, ".turn")})

	if !result.Recoverable {
		t.Fatalf("expected recoverable incomplete response, got %+v", result)
	}
	if !strings.Contains(result.Error, "agent_incomplete:max_output_tokens:incomplete_tool_arguments") {
		t.Fatalf("error: got %q", result.Error)
	}
	if _, err := os.Stat(filepath.Join(workspace, "answer.txt")); !os.IsNotExist(err) {
		t.Fatalf("partial tool call should not write answer.txt, stat err=%v", err)
	}
	if requests != 1 {
		t.Fatalf("requests: got %d", requests)
	}
	errorEvents := sessionLogEventsByType(t, filepath.Join(workspace, ".turn", "session.jsonl"), "error")
	if len(errorEvents) != 1 || errorEvents[0]["error_code"] != string(errAgentIncomplete) {
		t.Fatalf("error events: %#v", errorEvents)
	}
}

func TestNativeExecutorSendsTruncatedHugeToolOutput(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("TELOS_NATIVE_TOOL_MAX_BYTES", "128")
	if err := os.WriteFile(filepath.Join(workspace, "huge.txt"), []byte(strings.Repeat("x", 4096)), 0o644); err != nil {
		t.Fatal(err)
	}
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch requests {
		case 1:
			writeResponsesStream(w, `{
				"id":"resp_1","status":"completed",
				"output":[{"type":"function_call","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"huge.txt\"}"}],
				"usage":{"input_tokens":5,"output_tokens":5,"input_tokens_details":{"cached_tokens":0}}
			}`)
		case 2:
			body, _ := io.ReadAll(r.Body)
			bodyText := string(body)
			if !strings.Contains(bodyText, "truncated: true") || !strings.Contains(bodyText, "size_bytes: 4096") {
				t.Fatalf("second request should include truncation metadata, body length=%d", len(bodyText))
			}
			if strings.Contains(bodyText, strings.Repeat("x", 1024)) {
				t.Fatalf("second request included unbounded tool output, body length=%d", len(bodyText))
			}
			writeResponsesStream(w, `{
				"id":"resp_2","status":"completed",
				"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Observed bounded output.\n<progress_update>checked truncated output</progress_update>"}]}],
				"usage":{"input_tokens":5,"output_tokens":5,"input_tokens_details":{"cached_tokens":0}}
			}`)
		default:
			t.Fatalf("unexpected request %d", requests)
		}
	}))
	defer server.Close()

	t.Setenv("TELOS_API_BASE_URL", server.URL)
	t.Setenv("TELOS_API_KEY", "test-key")

	exec := NewNativeExecutor(platform.NewLocalPlatform(workspace), "test/test-model", "high", 0)
	result := exec.ExecuteTurn("run noisy command", &game.TurnState{Role: "prover", Dir: filepath.Join(workspace, ".turn")})

	if result.Error != "" {
		t.Fatalf("error: got %q logs=%q", result.Error, result.Logs)
	}
	if requests != 2 {
		t.Fatalf("requests: got %d", requests)
	}
}

func TestNativeExecutorRejectsIncompleteFinalAfterToolResults(t *testing.T) {
	workspace := t.TempDir()
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch requests {
		case 1:
			writeResponsesStream(w, `{
				"id":"resp_1","status":"completed",
				"output":[{"type":"function_call","call_id":"call_1","name":"write","arguments":"{\"path\":\"answer.txt\",\"content\":\"done\\n\"}"}],
				"usage":{"input_tokens":5,"output_tokens":5,"input_tokens_details":{"cached_tokens":0}}
			}`)
		case 2:
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "path: answer.txt") {
				t.Fatalf("second request should include tool result, got %s", body)
			}
			writeResponsesIncompleteStream(w, `{
				"id":"resp_2","status":"incomplete",
				"incomplete_details":{"reason":"max_output_tokens"},
				"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Created answer"}]}],
				"usage":{"input_tokens":5,"output_tokens":5,"input_tokens_details":{"cached_tokens":0}}
			}`)
		default:
			t.Fatalf("unexpected request %d", requests)
		}
	}))
	defer server.Close()

	t.Setenv("TELOS_API_BASE_URL", server.URL)
	t.Setenv("TELOS_API_KEY", "test-key")

	exec := NewNativeExecutor(platform.NewLocalPlatform(workspace), "test/test-model", "high", 0)
	result := exec.ExecuteTurn("create answer.txt", &game.TurnState{Role: "prover", Dir: filepath.Join(workspace, ".turn")})

	if !result.Recoverable {
		t.Fatalf("expected recoverable incomplete response, got %+v", result)
	}
	if !strings.Contains(result.Error, "agent_incomplete:max_output_tokens") {
		t.Fatalf("error: got %q", result.Error)
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
}

func TestNativeExecutorRetriesTransientProviderError(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		errorCode  executorErrorCode
	}{
		{
			name:       "rate limit",
			statusCode: http.StatusTooManyRequests,
			body:       `{"error":{"message":"rate limited"}}`,
			errorCode:  errProviderRateLimited,
		},
		{
			name:       "transient 5xx",
			statusCode: http.StatusServiceUnavailable,
			body:       `{"error":{"message":"gateway unavailable"}}`,
			errorCode:  errProviderUnavailable,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspace := t.TempDir()
			var requests int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if requests == 1 {
					w.WriteHeader(tt.statusCode)
					_, _ = io.WriteString(w, tt.body)
					return
				}
				writeResponsesStream(w, `{
					"id":"resp_1","status":"completed",
					"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done.\n<progress_update>retried</progress_update>\n"}]}],
					"usage":{"input_tokens":5,"output_tokens":5,"input_tokens_details":{"cached_tokens":0}}
				}`)
			}))
			defer server.Close()

			t.Setenv("TELOS_API_BASE_URL", server.URL)
			t.Setenv("TELOS_API_KEY", "test-key")

			exec := NewNativeExecutor(platform.NewLocalPlatform(workspace), "test/test-model", "high", 0)
			ts := &game.TurnState{Role: "prover", Dir: filepath.Join(workspace, ".turn")}
			result := exec.ExecuteTurn("Say hello.", ts)

			if result.Error != "" {
				t.Fatalf("error: got %q logs=%q", result.Error, result.Logs)
			}
			if requests != 2 {
				t.Fatalf("requests: got %d", requests)
			}
			events := sessionLogEventsByType(t, ts.SessionPath(), "retry")
			if len(events) != 1 {
				t.Fatalf("retry events: %#v", events)
			}
			if events[0]["error_code"] != string(tt.errorCode) || events[0]["provider_status_code"] != float64(tt.statusCode) {
				t.Fatalf("retry event missing provider status: %#v", events[0])
			}
		})
	}
}

func TestNativeExecutorDoesNotRetryProviderInvalidRequest(t *testing.T) {
	workspace := t.TempDir()
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests > 1 {
			t.Fatalf("invalid request should not be retried")
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"invalid request: unsupported parameter"}}`)
	}))
	defer server.Close()

	t.Setenv("TELOS_API_BASE_URL", server.URL)
	t.Setenv("TELOS_API_KEY", "test-key")

	exec := NewNativeExecutor(platform.NewLocalPlatform(workspace), "test/test-model", "high", 0)
	result := exec.ExecuteTurn("Say hello.", &game.TurnState{Role: "prover", Dir: filepath.Join(workspace, ".turn")})

	if !result.Recoverable {
		t.Fatalf("expected recoverable provider error, got %+v", result)
	}
	if !strings.Contains(result.Error, "provider_invalid_request") {
		t.Fatalf("error: got %q", result.Error)
	}
	if requests != 1 {
		t.Fatalf("requests: got %d", requests)
	}
}

func TestClassifyProviderMessageDistinguishesContextLimit(t *testing.T) {
	err := classifyProviderMessage("context length exceeded maximum context", http.StatusBadRequest)
	execErr, ok := err.(*executorError)
	if !ok {
		t.Fatalf("error type: %T %v", err, err)
	}
	if execErr.Code != errProviderContextLimit || execErr.Retryable {
		t.Fatalf("context-limit classification: %+v", execErr)
	}
}

func TestNativeExecutorFallsBackToStatelessHistoryWhenResponseChainBreaks(t *testing.T) {
	t.Setenv("TELOS_MODEL_STATE_MODE", "server_chain")
	workspace := t.TempDir()
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		body, _ := io.ReadAll(r.Body)
		var req map[string]interface{}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatal(err)
		}
		switch requests {
		case 1:
			writeResponsesStream(w, `{
				"id":"resp_1","status":"completed",
				"output":[{"type":"function_call","call_id":"call_1","name":"write","arguments":"{\"path\":\"answer.txt\",\"content\":\"done\\n\"}"}],
				"usage":{"input_tokens":5,"output_tokens":5,"input_tokens_details":{"cached_tokens":0}}
			}`)
		case 2:
			if req["previous_response_id"] != "resp_1" {
				t.Fatalf("expected server chain request, got %s", body)
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"previous_response_id not found"}}`)
		case 3:
			if _, ok := req["previous_response_id"]; ok {
				t.Fatalf("stateless fallback should omit previous_response_id: %s", body)
			}
			if !strings.Contains(string(body), "function_call") || !strings.Contains(string(body), "function_call_output") {
				t.Fatalf("stateless fallback should replay function call and output history: %s", body)
			}
			writeResponsesStream(w, `{
				"id":"resp_2","status":"completed",
				"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done.\n<progress_update>wrote answer</progress_update>\n"}]}],
				"usage":{"input_tokens":5,"output_tokens":5,"input_tokens_details":{"cached_tokens":0}}
			}`)
		default:
			t.Fatalf("unexpected request %d: %s", requests, body)
		}
	}))
	defer server.Close()

	t.Setenv("TELOS_API_BASE_URL", server.URL)
	t.Setenv("TELOS_API_KEY", "test-key")

	exec := NewNativeExecutor(platform.NewLocalPlatform(workspace), "test/test-model", "high", 0)
	result := exec.ExecuteTurn("create answer.txt", &game.TurnState{Role: "prover", Dir: filepath.Join(workspace, ".turn")})

	if result.Error != "" {
		t.Fatalf("error: got %q logs=%q", result.Error, result.Logs)
	}
	if requests != 3 {
		t.Fatalf("requests: got %d", requests)
	}
}

func TestNativeToolsBoundFileReadsAndBinary(t *testing.T) {
	workspace := t.TempDir()
	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, fmt.Sprintf("line-%02d", i+1))
	}
	if err := os.WriteFile(filepath.Join(workspace, "big.txt"), []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "bin.dat"), []byte{'a', 0, 'b'}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "invalid.txt"), []byte{0xff, 'x'}, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TELOS_NATIVE_TOOL_MAX_LINES", "5")
	t.Setenv("TELOS_NATIVE_TOOL_MAX_BYTES", "64")
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil, nil, nil, resolveEnvKnobs())

	read := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_1",
		Name:      "read_file",
		Arguments: `{"path":"big.txt","start_line":3,"limit_lines":50}`,
	})
	if read.IsError {
		t.Fatalf("read failed: %+v", read)
	}
	if !strings.Contains(read.Output, "lines_returned: 3-7") || !strings.Contains(read.Output, "truncated: true") {
		t.Fatalf("bounded read output missing metadata:\n%s", read.Output)
	}

	binary := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_2",
		Name:      "read_file",
		Arguments: `{"path":"bin.dat"}`,
	})
	if binary.IsError || !strings.Contains(binary.Output, "binary: true") {
		t.Fatalf("binary output: %+v", binary)
	}

	invalid := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_3",
		Name:      "read_file",
		Arguments: `{"path":"invalid.txt"}`,
	})
	if invalid.IsError || !strings.Contains(invalid.Output, "binary: true") {
		t.Fatalf("invalid UTF-8 output: %+v", invalid)
	}

	truncatedText, ok := truncateText("aaébb", 3)
	if !ok || !utf8.ValidString(truncatedText) || strings.ContainsRune(truncatedText, utf8.RuneError) {
		t.Fatalf("truncateText should preserve valid UTF-8, got %q truncated=%t", truncatedText, ok)
	}
}

func TestNativeEditingToolsPreserveExistingFileMode(t *testing.T) {
	workspace := t.TempDir()
	scriptPath := filepath.Join(workspace, "script.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\necho old\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil, nil, nil, resolveEnvKnobs())

	written := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_write",
		Name:      "write_file",
		Arguments: `{"path":"script.sh","content":"#!/bin/sh\necho new\n"}`,
	})
	if written.IsError {
		t.Fatalf("write_file failed:\n%s", written.Output)
	}
	if !strings.Contains(written.Output, "created: false") || !strings.Contains(written.Output, "mode: -rwxr-xr-x") {
		t.Fatalf("write_file output missing mode metadata:\n%s", written.Output)
	}
	if info, err := os.Stat(scriptPath); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o755 {
		t.Fatalf("write_file should preserve mode, got %o", info.Mode().Perm())
	}

	replaced := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_replace",
		Name:      "replace_text",
		Arguments: `{"path":"script.sh","old_string":"echo new","new_string":"echo newer","expected_count":1}`,
	})
	if replaced.IsError {
		t.Fatalf("replace_text failed:\n%s", replaced.Output)
	}
	if !strings.Contains(replaced.Output, "replacement_count: 1") || !strings.Contains(replaced.Output, "mode: -rwxr-xr-x") {
		t.Fatalf("replace_text output missing mode metadata:\n%s", replaced.Output)
	}
	if info, err := os.Stat(scriptPath); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o755 {
		t.Fatalf("replace_text should preserve mode, got %o", info.Mode().Perm())
	}
}

func TestNativeToolsReplaceTextRejectsBinaryAndInvalidUTF8(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "nul.bin"), []byte{'o', 'l', 'd', 0, 'x'}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "invalid.txt"), []byte{0xff, 'o', 'l', 'd'}, 0o644); err != nil {
		t.Fatal(err)
	}
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil, nil, nil, resolveEnvKnobs())

	for _, path := range []string{"nul.bin", "invalid.txt"} {
		result := tools.execute(context.Background(), nativeToolCall{
			ID:        "call_" + path,
			Name:      "replace_text",
			Arguments: `{"path":"` + path + `","old_string":"old","new_string":"new"}`,
		})
		if !result.IsError {
			t.Fatalf("expected replace_text to reject %s:\n%s", path, result.Output)
		}
		if !strings.Contains(result.Output, "not a UTF-8 text file") {
			t.Fatalf("unexpected replace_text output for %s:\n%s", path, result.Output)
		}
	}
}

func TestNativeEditingToolsRejectNULTextInputs(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "plain.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil, nil, nil, resolveEnvKnobs())

	written := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_write",
		Name:      "write_file",
		Arguments: "{\"path\":\"nul.txt\",\"content\":\"a\\u0000b\"}",
	})
	if !written.IsError || !strings.Contains(written.Output, "contains NUL byte") {
		t.Fatalf("expected write_file to reject NUL content:\n%s", written.Output)
	}

	replaced := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_replace",
		Name:      "replace_text",
		Arguments: "{\"path\":\"plain.txt\",\"old_string\":\"old\",\"new_string\":\"new\\u0000\"}",
	})
	if !replaced.IsError || !strings.Contains(replaced.Output, "must not contain NUL bytes") {
		t.Fatalf("expected replace_text to reject NUL content:\n%s", replaced.Output)
	}
}

func TestNativeToolsBashReportsOriginalOutputCounts(t *testing.T) {
	workspace := t.TempDir()
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil, nil, nil, resolveEnvKnobs())

	result := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_1",
		Name:      "bash",
		Arguments: `{"command":"python3 - <<'PY'\nimport sys\nprint('x' * (300 * 1024))\nprint('y' * (300 * 1024), file=sys.stderr)\nPY"}`,
	})
	if result.IsError {
		t.Fatalf("bash failed:\n%s", result.Output)
	}
	if !result.Truncated {
		t.Fatalf("expected bash result metadata to mark truncation: %+v", result)
	}
	if !result.HasExitCode || result.ExitCode != 0 {
		t.Fatalf("expected bash exit-code metadata, got %+v", result)
	}
	if result.Metadata["stdout_truncated"] != true || result.Metadata["stderr_truncated"] != true {
		t.Fatalf("expected structured truncation metadata: %#v", result.Metadata)
	}
	if _, ok := result.Metadata["stdout_original_bytes"].(int); !ok {
		t.Fatalf("expected stdout_original_bytes metadata: %#v", result.Metadata)
	}
	for _, want := range []string{
		"stdout_original_bytes:",
		"stdout_original_lines: 1",
		"stdout_truncated: true",
		"stderr_original_bytes:",
		"stderr_original_lines: 1",
		"stderr_truncated: true",
		"signal: none",
		"started_at:",
		"ended_at:",
		"timed_out: false",
		"interrupted: false",
	} {
		if !strings.Contains(result.Output, want) {
			t.Fatalf("bash output missing %q:\n%s", want, result.Output)
		}
	}
}

func TestNativeToolsSearchTextStopsAtMatchLimit(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "a-first.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "z-later.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil, nil, nil, resolveEnvKnobs())

	result := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_1",
		Name:      "search_text",
		Arguments: `{"pattern":"needle","max_matches":1}`,
	})

	if result.IsError {
		t.Fatalf("search_text failed:\n%s", result.Output)
	}
	if !strings.Contains(result.Output, "match_count: 1") || !strings.Contains(result.Output, "truncated: true") {
		t.Fatalf("search output missing cap metadata:\n%s", result.Output)
	}
	if !strings.Contains(result.Output, "a-first.txt") {
		t.Fatalf("expected first file match:\n%s", result.Output)
	}
	if strings.Contains(result.Output, "z-later.txt") {
		t.Fatalf("search should stop after max_matches:\n%s", result.Output)
	}
}

func TestNativeToolsListDirReturnsBoundedEntriesWithTotalCount(t *testing.T) {
	workspace := t.TempDir()
	for i := 0; i < 12; i++ {
		name := fmt.Sprintf("file-%02d.txt", i)
		if err := os.WriteFile(filepath.Join(workspace, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TELOS_NATIVE_TOOL_MAX_LINES", "5")
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil, nil, nil, resolveEnvKnobs())

	result := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_1",
		Name:      "list_dir",
		Arguments: `{}`,
	})

	if result.IsError {
		t.Fatalf("list_dir failed:\n%s", result.Output)
	}
	for _, want := range []string{
		"entry_count: 12",
		"entries_returned: 5",
		"truncated: true",
	} {
		if !strings.Contains(result.Output, want) {
			t.Fatalf("list_dir output missing %q:\n%s", want, result.Output)
		}
	}
	if result.Metadata["entries_returned"] != 5 || result.Metadata["entry_count"] != 12 || result.Metadata["truncated"] != true {
		t.Fatalf("list_dir metadata: %#v", result.Metadata)
	}
}

func TestNativeToolsListDirAndFindFilesApplyByteCaps(t *testing.T) {
	workspace := t.TempDir()
	longNames := make([]string, 0, 6)
	for i := 0; i < 6; i++ {
		name := fmt.Sprintf("long-file-%02d-%s.txt", i, strings.Repeat("x", 60))
		longNames = append(longNames, name)
		if err := os.WriteFile(filepath.Join(workspace, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TELOS_NATIVE_TOOL_MAX_BYTES", "128")
	t.Setenv("TELOS_NATIVE_TOOL_MAX_LINES", "20")
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil, nil, nil, resolveEnvKnobs())

	listed := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_list",
		Name:      "list_dir",
		Arguments: `{}`,
	})
	if listed.IsError {
		t.Fatalf("list_dir failed:\n%s", listed.Output)
	}
	if !strings.Contains(listed.Output, "entries_returned: 6") || !strings.Contains(listed.Output, "truncated: true") {
		t.Fatalf("list_dir should report byte truncation without entry truncation:\n%s", listed.Output)
	}
	if strings.Contains(listed.Output, longNames[len(longNames)-1]) {
		t.Fatalf("list_dir output should be byte capped:\n%s", listed.Output)
	}

	found := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_find",
		Name:      "find_files",
		Arguments: `{"pattern":"*.txt","max_matches":20}`,
	})
	if found.IsError {
		t.Fatalf("find_files failed:\n%s", found.Output)
	}
	if !strings.Contains(found.Output, "match_count: 6") || !strings.Contains(found.Output, "truncated: true") {
		t.Fatalf("find_files should report byte truncation without match truncation:\n%s", found.Output)
	}
	if strings.Contains(found.Output, longNames[len(longNames)-1]) {
		t.Fatalf("find_files output should be byte capped:\n%s", found.Output)
	}
}

func TestNativeToolsFindFilesSupportsRecursiveGlobstar(t *testing.T) {
	workspace := t.TempDir()
	// Build a nested tree so we can exercise `**` across directory boundaries.
	files := map[string]string{
		"a.go":                        "x",
		"pkg/b.go":                    "x",
		"pkg/sub/c.go":                "x",
		"pkg/sub/deep/d.go":           "x",
		"pkg/sub/deep/e.txt":          "x",
		"other/also.go":               "x",
		"node_modules/dep/ignored.go": "x", // shouldSkipDir drops node_modules
	}
	for rel, content := range files {
		full := filepath.Join(workspace, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil, nil, nil, resolveEnvKnobs())

	// `**/*.go` must match .go files at every depth, including the root.
	r1 := tools.execute(context.Background(), nativeToolCall{
		ID: "call_1", Name: "find_files",
		Arguments: `{"pattern":"**/*.go","max_matches":50}`,
	})
	if r1.IsError {
		t.Fatalf("find_files **/*.go failed:\n%s", r1.Output)
	}
	want := []string{"a.go", "pkg/b.go", "pkg/sub/c.go", "pkg/sub/deep/d.go", "other/also.go"}
	if !strings.Contains(r1.Output, "match_count: 5") {
		t.Fatalf("expected 5 .go matches (node_modules skipped), got:\n%s", r1.Output)
	}
	for _, w := range want {
		if !strings.Contains(r1.Output, w) {
			t.Fatalf("expected %q in **/*.go results:\n%s", w, r1.Output)
		}
	}
	if strings.Contains(r1.Output, "ignored.go") {
		t.Fatalf("node_modules should be skipped:\n%s", r1.Output)
	}

	// A scoped recursive pattern under a specific directory.
	r2 := tools.execute(context.Background(), nativeToolCall{
		ID: "call_2", Name: "find_files",
		Arguments: `{"pattern":"pkg/**/*.go","max_matches":50}`,
	})
	if r2.IsError {
		t.Fatalf("find_files pkg/**/*.go failed:\n%s", r2.Output)
	}
	if !strings.Contains(r2.Output, "match_count: 3") {
		t.Fatalf("expected 3 matches under pkg/, got:\n%s", r2.Output)
	}
	for _, w := range []string{"pkg/b.go", "pkg/sub/c.go", "pkg/sub/deep/d.go"} {
		if !strings.Contains(r2.Output, w) {
			t.Fatalf("expected %q in pkg/**/*.go results:\n%s", w, r2.Output)
		}
	}

	// A bare `*.go` still matches at any depth (basename fallback).
	r3 := tools.execute(context.Background(), nativeToolCall{
		ID: "call_3", Name: "find_files",
		Arguments: `{"pattern":"*.go","max_matches":50}`,
	})
	if r3.IsError {
		t.Fatalf("find_files *.go failed:\n%s", r3.Output)
	}
	if !strings.Contains(r3.Output, "match_count: 5") {
		t.Fatalf("bare *.go should match at any depth, got:\n%s", r3.Output)
	}

	// An invalid pattern is rejected up front.
	r4 := tools.execute(context.Background(), nativeToolCall{
		ID: "call_4", Name: "find_files",
		Arguments: `{"pattern":"[unclosed","max_matches":50}`,
	})
	if !r4.IsError {
		t.Fatalf("invalid glob pattern should be rejected, got:\n%s", r4.Output)
	}
}

func TestNativeToolsBashAcceptsBoundedEnv(t *testing.T) {
	workspace := t.TempDir()
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil, nil, nil, resolveEnvKnobs())

	result := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_1",
		Name:      "bash",
		Arguments: `{"command":"printf '%s' \"$TELOS_TEST_VALUE\"","env":{"TELOS_TEST_VALUE":"from-tool"}}`,
	})
	if result.IsError {
		t.Fatalf("bash failed:\n%s", result.Output)
	}
	if !strings.Contains(result.Output, "from-tool") {
		t.Fatalf("bash output missing env value:\n%s", result.Output)
	}
}

func TestNativeToolsBashRejectsInvalidEnvName(t *testing.T) {
	workspace := t.TempDir()
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil, nil, nil, resolveEnvKnobs())

	result := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_1",
		Name:      "bash",
		Arguments: `{"command":"true","env":{"BAD-NAME":"value"}}`,
	})
	if !result.IsError {
		t.Fatalf("expected invalid env error, got:\n%s", result.Output)
	}
	if !strings.Contains(result.Output, "invalid environment variable name") {
		t.Fatalf("unexpected output:\n%s", result.Output)
	}
}

func TestNativeToolsClassifiesMalformedToolCallAsAgentProtocol(t *testing.T) {
	workspace := t.TempDir()
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil, nil, nil, resolveEnvKnobs())

	invalidJSON := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_invalid",
		Name:      "bash",
		Arguments: `{"command":`,
	})
	if !invalidJSON.IsError || invalidJSON.ErrorCode != errAgentProtocol {
		t.Fatalf("invalid JSON classification: %+v\n%s", invalidJSON, invalidJSON.Output)
	}

	unknown := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_unknown",
		Name:      "does_not_exist",
		Arguments: `{}`,
	})
	if !unknown.IsError || unknown.ErrorCode != errAgentProtocol {
		t.Fatalf("unknown tool classification: %+v\n%s", unknown, unknown.Output)
	}
}

func TestNativeToolsBashMarksNonzeroExitAsError(t *testing.T) {
	workspace := t.TempDir()
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil, nil, nil, resolveEnvKnobs())

	result := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_1",
		Name:      "bash",
		Arguments: `{"command":"printf out; printf err >&2; exit 7"}`,
	})

	if !result.IsError {
		t.Fatalf("expected nonzero bash exit to be an error:\n%s", result.Output)
	}
	if !result.HasExitCode || result.ExitCode != 7 {
		t.Fatalf("exit-code metadata: %+v\n%s", result, result.Output)
	}
	if result.ErrorCode != "" {
		t.Fatalf("nonzero command exit should not be classified as tool infra: %+v", result)
	}
	for _, want := range []string{"ok: false", "exit_code: 7", "stdout:\nout", "stderr:\nerr"} {
		if !strings.Contains(result.Output, want) {
			t.Fatalf("bash output missing %q:\n%s", want, result.Output)
		}
	}
}

func TestNativeToolsBashAppliesLineCaps(t *testing.T) {
	t.Setenv("TELOS_NATIVE_TOOL_MAX_LINES", "3")
	workspace := t.TempDir()
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil, nil, nil, resolveEnvKnobs())

	result := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_lines",
		Name:      "bash",
		Arguments: `{"command":"for i in 1 2 3 4 5; do echo out-$i; echo err-$i >&2; done"}`,
	})

	if result.IsError {
		t.Fatalf("bash failed:\n%s", result.Output)
	}
	for _, want := range []string{
		"stdout_original_lines: 5",
		"stderr_original_lines: 5",
		"stdout_truncated: true",
		"stderr_truncated: true",
		"... stdout truncated at 3 lines of 5 ...",
		"... stderr truncated at 3 lines of 5 ...",
	} {
		if !strings.Contains(result.Output, want) {
			t.Fatalf("bash line-cap output missing %q:\n%s", want, result.Output)
		}
	}
	if strings.Contains(result.Output, "out-4") || strings.Contains(result.Output, "err-4") {
		t.Fatalf("line-capped output leaked lines beyond cap:\n%s", result.Output)
	}
	if result.Metadata["stdout_truncated"] != true || result.Metadata["stderr_truncated"] != true {
		t.Fatalf("truncation metadata: %#v", result.Metadata)
	}
}

func TestNativeToolsBashClassifiesTimeoutAsToolTimeout(t *testing.T) {
	workspace := t.TempDir()
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil, nil, nil, resolveEnvKnobs())

	result := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_timeout",
		Name:      "bash",
		Arguments: `{"command":"sleep 60","timeout_seconds":1}`,
	})

	if !result.IsError {
		t.Fatalf("expected timeout to be an error:\n%s", result.Output)
	}
	if result.ErrorCode != errToolTimeout {
		t.Fatalf("timeout error code: got %q\n%s", result.ErrorCode, result.Output)
	}
	for _, want := range []string{"timed_out: true", "error:\nlocal_timeout:1"} {
		if !strings.Contains(result.Output, want) {
			t.Fatalf("timeout output missing %q:\n%s", want, result.Output)
		}
	}
}

func TestNativeSessionLoggerIncludesToolErrorCode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	logger := newNativeSessionLogger(path, dir)
	if err := logger.start(); err != nil {
		t.Fatal(err)
	}
	if err := logger.tool(nativeToolResult{CallID: "call_timeout", Name: "bash", Output: "timeout", IsError: true, ErrorCode: errToolTimeout}); err != nil {
		t.Fatal(err)
	}

	events := sessionLogEventsByType(t, path, "tool_result")
	if len(events) != 1 {
		t.Fatalf("tool_result events: %#v", events)
	}
	if events[0]["error_code"] != string(errToolTimeout) {
		t.Fatalf("tool_result error_code: %#v", events[0])
	}
}

func TestNativeSessionLoggerIncludesToolMetadata(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	logger := newNativeSessionLogger(path, dir)
	if err := logger.start(); err != nil {
		t.Fatal(err)
	}
	result := nativeToolResult{
		CallID:     "call_1",
		Name:       "bash",
		Output:     "tool: bash\nok: true\nstdout_original_bytes: 300000\nstdout_truncated: true\n",
		DurationMS: 12,
	}
	result.applyMetadataFromOutput()
	if err := logger.tool(result); err != nil {
		t.Fatal(err)
	}

	events := sessionLogEventsByType(t, path, "tool_result")
	if len(events) != 1 {
		t.Fatalf("tool_result events: %#v", events)
	}
	metadata, ok := events[0]["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("metadata missing: %#v", events[0])
	}
	if metadata["stdout_truncated"] != true || metadata["stdout_original_bytes"] != float64(300000) {
		t.Fatalf("unexpected metadata: %#v", metadata)
	}
}

func TestNativeToolsApplyPatchReportsStructuredMetadata(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "old.txt"), []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil, nil, nil, resolveEnvKnobs())
	patch := strings.Join([]string{
		"diff --git a/old.txt b/old.txt",
		"--- a/old.txt",
		"+++ b/old.txt",
		"@@ -1 +1 @@",
		"-before",
		"+after",
		"diff --git a/new.txt b/new.txt",
		"new file mode 100644",
		"index 0000000..8d1c8b2",
		"--- /dev/null",
		"+++ b/new.txt",
		"@@ -0,0 +1 @@",
		"+created",
		"",
	}, "\n")

	result := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_1",
		Name:      "apply_patch",
		Arguments: `{"patch":` + mustJSON(patch) + `}`,
	})

	if result.IsError {
		t.Fatalf("apply_patch failed:\n%s", result.Output)
	}
	for _, want := range []string{
		"patch_bytes:",
		"changed_path_count: 2",
		"changed_paths: new.txt, old.txt",
		"created_paths: new.txt",
		"hunk_count: 2",
		"files:",
		"- path: new.txt",
		"created: true",
		"bytes_written: 8",
		"- path: old.txt",
		"created: false",
		"bytes_written: 6",
		"mode: -rw-r--r--",
	} {
		if !strings.Contains(result.Output, want) {
			t.Fatalf("apply_patch output missing %q:\n%s", want, result.Output)
		}
	}
	updated, err := os.ReadFile(filepath.Join(workspace, "old.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(updated) != "after\n" {
		t.Fatalf("old.txt: got %q", updated)
	}
	created, err := os.ReadFile(filepath.Join(workspace, "new.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(created) != "created\n" {
		t.Fatalf("new.txt: got %q", created)
	}
}

func TestNativeToolsApplyPatchRejectsUnsafePaths(t *testing.T) {
	workspace := t.TempDir()
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil, nil, nil, resolveEnvKnobs())
	cases := []struct {
		name  string
		patch string
	}{
		{
			name: "hunk path",
			patch: strings.Join([]string{
				"diff --git a/../escape.txt b/../escape.txt",
				"new file mode 100644",
				"index 0000000..8d1c8b2",
				"--- /dev/null",
				"+++ b/../escape.txt",
				"@@ -0,0 +1 @@",
				"+created",
				"",
			}, "\n"),
		},
		{
			name: "diff header",
			patch: strings.Join([]string{
				"diff --git a/safe.txt b/../escape.txt",
				"--- a/safe.txt",
				"+++ b/safe.txt",
				"@@ -1 +1 @@",
				"-before",
				"+after",
				"",
			}, "\n"),
		},
		{
			name: "rename header",
			patch: strings.Join([]string{
				"diff --git a/safe.txt b/safe2.txt",
				"similarity index 100%",
				"rename from safe.txt",
				"rename to ../escape.txt",
				"",
			}, "\n"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := tools.execute(context.Background(), nativeToolCall{
				ID:        "call_1",
				Name:      "apply_patch",
				Arguments: `{"patch":` + mustJSON(tc.patch) + `}`,
			})

			if !result.IsError {
				t.Fatalf("expected unsafe patch path to fail:\n%s", result.Output)
			}
			if !strings.Contains(result.Output, "outside workspace") {
				t.Fatalf("unexpected output:\n%s", result.Output)
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(workspace), "escape.txt")); !os.IsNotExist(err) {
				t.Fatalf("unsafe path was created: %v", err)
			}
		})
	}
}

func TestNativeSkillToolListsAndReadsSkillBodies(t *testing.T) {
	workspace := t.TempDir()
	skillDir := filepath.Join(t.TempDir(), "review-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	refDir := filepath.Join(skillDir, "references")
	if err := os.MkdirAll(refDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skillPath := filepath.Join(skillDir, "SKILL.md")
	body := "---\nname: review-skill\ndescription: Review rubric\n---\n# Rubric\n\nCheck evidence.\nSee [details](references/details.md).\n"
	if err := os.WriteFile(skillPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(refDir, "details.md"), []byte("# Details\n\nInspect logs.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	skills := []game.TurnSkill{{Name: "review-skill", Description: "Review rubric", SkillPath: filepath.ToSlash(skillPath), Required: true}}
	logPath := filepath.Join(workspace, ".turn", "session.jsonl")
	logger := newNativeSessionLogger(logPath, workspace)
	if err := logger.start(); err != nil {
		t.Fatal(err)
	}
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil, skills, logger, resolveEnvKnobs())

	list := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_1",
		Name:      "skill",
		Arguments: `{"action":"list"}`,
	})
	if list.IsError || !strings.Contains(list.Output, "name: review-skill") || !strings.Contains(list.Output, "required: true") {
		t.Fatalf("skill list output: %+v", list)
	}

	read := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_2",
		Name:      "skill",
		Arguments: `{"action":"read","name":"review-skill"}`,
	})
	if read.IsError || !strings.Contains(read.Output, "# Rubric") || !strings.Contains(read.Output, "Check evidence.") {
		t.Fatalf("skill read output: %+v", read)
	}
	if missing := tools.missingRequiredSkills(); len(missing) != 0 {
		t.Fatalf("required skill should be satisfied after complete read: %v", missing)
	}
	if events := sessionLogEventsByType(t, logPath, "skill_opened"); len(events) != 1 || events[0]["name"] != "review-skill" || events[0]["truncated"] != false {
		t.Fatalf("skill_opened events: %#v", events)
	}
	if events := sessionLogEventsByType(t, logPath, "skill_applied"); len(events) != 1 || events[0]["name"] != "review-skill" {
		t.Fatalf("skill_applied events: %#v", events)
	}

	refRead := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_3",
		Name:      "skill",
		Arguments: `{"action":"read_ref","name":"review-skill","path":"references/details.md"}`,
	})
	if refRead.IsError || !strings.Contains(refRead.Output, "# Details") || !strings.Contains(refRead.Output, "Inspect logs.") {
		t.Fatalf("skill reference read output: %+v", refRead)
	}

	escape := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_4",
		Name:      "skill",
		Arguments: `{"action":"read_ref","name":"review-skill","path":"../outside.md"}`,
	})
	if !escape.IsError || !strings.Contains(escape.Output, "outside skill directory") {
		t.Fatalf("skill reference escape output: %+v", escape)
	}
}

func TestNativeSkillToolRequiresCompleteRequiredRubricRead(t *testing.T) {
	workspace := t.TempDir()
	skillDir := filepath.Join(t.TempDir(), "review-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skillPath := filepath.Join(skillDir, "SKILL.md")
	body := "# Rubric\n\n" + strings.Repeat("criterion must be read\n", 20)
	if err := os.WriteFile(skillPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TELOS_NATIVE_TOOL_MAX_BYTES", "64")
	skills := []game.TurnSkill{{Name: "review-skill", Description: "Review rubric", SkillPath: filepath.ToSlash(skillPath), Required: true}}
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil, skills, nil, resolveEnvKnobs())

	read := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_1",
		Name:      "skill",
		Arguments: `{"action":"read","name":"review-skill"}`,
	})
	if read.IsError || !strings.Contains(read.Output, "truncated: true") {
		t.Fatalf("skill read output: %+v", read)
	}
	if missing := tools.missingRequiredSkills(); len(missing) != 1 || missing[0] != "review-skill" {
		t.Fatalf("required skill should remain missing after truncated read: %v", missing)
	}
}

func TestNativeSkillToolPaginatedRequiredRubricRead(t *testing.T) {
	workspace := t.TempDir()
	skillDir := filepath.Join(t.TempDir(), "review-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skillPath := filepath.Join(skillDir, "SKILL.md")
	// A rubric larger than a single read window: it can only be read in pages.
	body := "# Rubric\n\n" + strings.Repeat("criterion must be read\n", 250)
	if err := os.WriteFile(skillPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TELOS_NATIVE_TOOL_MAX_LINES", "100")
	skills := []game.TurnSkill{{Name: "review-skill", Description: "Review rubric", SkillPath: filepath.ToSlash(skillPath), Required: true}}
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil, skills, nil, resolveEnvKnobs())

	read := func(start int) nativeToolResult {
		return tools.execute(context.Background(), nativeToolCall{
			ID:        fmt.Sprintf("call_%d", start),
			Name:      "skill",
			Arguments: fmt.Sprintf(`{"action":"read","name":"review-skill","start_line":%d}`, start),
		})
	}

	// First page leaves the rubric still partially read.
	if r := read(1); r.IsError {
		t.Fatalf("page 1 read: %+v", r)
	}
	if missing := tools.missingRequiredSkills(); len(missing) != 1 {
		t.Fatalf("rubric should still be unread after first page: %v", missing)
	}
	// Walking start_line to EOF completes the read across pages.
	read(101)
	read(201)
	if missing := tools.missingRequiredSkills(); len(missing) != 0 {
		t.Fatalf("paginated read to EOF should satisfy the required rubric: %v", missing)
	}
}

func TestReplaySessionLogValidatesProtocolWithoutModel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	logger := newNativeSessionLogger(path, dir)
	if err := logger.start(); err != nil {
		t.Fatal(err)
	}
	if err := logger.user("Implement code changes in the workspace."); err != nil {
		t.Fatal(err)
	}
	if err := logger.toolCall(nativeToolCall{ID: "call_1", Name: "read_file", Arguments: `{"path":"main.go"}`}); err != nil {
		t.Fatal(err)
	}
	if err := logger.tool(nativeToolResult{CallID: "call_1", Name: "read_file", Output: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := logger.assistant("Done.\n\n<progress_update>Updated main.go.</progress_update>", "litellm", "test-model", "stop", game.TurnStats{}); err != nil {
		t.Fatal(err)
	}

	report, err := ReplaySessionLog(path, "prover")
	if err != nil {
		t.Fatalf("ReplaySessionLog: %v", err)
	}
	if !report.ProtocolOK {
		t.Fatalf("expected protocol ok, got %q", report.ProtocolError)
	}
	if report.ToolCalls != 1 || report.ToolResults != 1 {
		t.Fatalf("tool counts: calls=%d results=%d", report.ToolCalls, report.ToolResults)
	}

	badPath := filepath.Join(dir, "bad-session.jsonl")
	badLogger := newNativeSessionLogger(badPath, dir)
	if err := badLogger.start(); err != nil {
		t.Fatal(err)
	}
	if err := badLogger.user("Implement code changes in the workspace."); err != nil {
		t.Fatal(err)
	}
	if err := badLogger.assistant("Done.", "litellm", "test-model", "stop", game.TurnStats{}); err != nil {
		t.Fatal(err)
	}
	badReport, err := ReplaySessionLog(badPath, "prover")
	if err != nil {
		t.Fatalf("ReplaySessionLog bad: %v", err)
	}
	if badReport.ProtocolOK || badReport.ProtocolError != "missing_progress_update" {
		t.Fatalf("expected missing progress update, got ok=%t err=%q", badReport.ProtocolOK, badReport.ProtocolError)
	}
}

func TestSanitizeVisibleTextRemovesReasoningTags(t *testing.T) {
	tests := []struct {
		name        string
		raw         string
		wantClean   string
		wantRemoved []string
	}{
		{
			name:        "balanced think block",
			raw:         "<think>hidden plan</think>\nanswer",
			wantClean:   "answer",
			wantRemoved: []string{"hidden plan", "<think>"},
		},
		{
			name:        "stray close keeps answer tail",
			raw:         "hidden chain of thought</think>\nanswer",
			wantClean:   "answer",
			wantRemoved: []string{"hidden chain of thought</think>"},
		},
		{
			name:        "stray open drops truncated tail",
			raw:         "answer\n<think>unfinished hidden tail",
			wantClean:   "answer",
			wantRemoved: []string{"unfinished hidden tail", "<think>"},
		},
		{
			name:        "multiple mixed case blocks",
			raw:         "visible\n<THINKING>hidden plan</THINKING>\n<reasoning>x</reasoning>\ndone",
			wantClean:   "visible\n\n\ndone",
			wantRemoved: []string{"hidden plan", "<reasoning>x</reasoning>"},
		},
		{
			name:      "no tags unchanged",
			raw:       "visible answer",
			wantClean: "visible answer",
		},
		{
			name:      "stray close after protocol blocks keeps them",
			raw:       "<review>criteria,score\nclarity,8.0/10</review>\n<summary>fine</summary>\nleaked tail</think>",
			wantClean: "<review>criteria,score\nclarity,8.0/10</review>\n<summary>fine</summary>\nleaked tail</think>",
		},
		{
			name:      "stray open before protocol block keeps it",
			raw:       "<think>leaked plan\n<progress_update>changed main.go</progress_update>",
			wantClean: "<think>leaked plan\n<progress_update>changed main.go</progress_update>",
		},
		{
			name:      "incidental angle bracket unchanged",
			raw:       "keep math a < b and c > d",
			wantClean: "keep math a < b and c > d",
		},
		{
			name:        "all reasoning becomes empty",
			raw:         "<thinking>only hidden</thinking>",
			wantClean:   "",
			wantRemoved: []string{"only hidden"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sanitized, removed := sanitizeVisibleText(tt.raw, false)
			if sanitized != tt.wantClean {
				t.Fatalf("sanitized: got %q want %q removed=%q", sanitized, tt.wantClean, removed)
			}
			if len(tt.wantRemoved) == 0 && removed != "" {
				t.Fatalf("removed should be empty, got %q", removed)
			}
			for _, want := range tt.wantRemoved {
				if !strings.Contains(removed, want) {
					t.Fatalf("removed missing %q: %q", want, removed)
				}
			}
		})
	}
}

func TestSanitizeVisibleTextKeepReasoningFlag(t *testing.T) {
	raw := "<thinking>hidden</thinking>\nanswer"
	sanitized, removed := sanitizeVisibleText(raw, true)
	if sanitized != raw || removed != "" {
		t.Fatalf("keep-reasoning should leave content untouched, got sanitized=%q removed=%q", sanitized, removed)
	}

	// With the flag off, the same input is stripped.
	sanitized, removed = sanitizeVisibleText(raw, false)
	if sanitized != "answer" || removed == "" {
		t.Fatalf("strip should remove reasoning, got sanitized=%q removed=%q", sanitized, removed)
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

func TestNativeToolsPathResolution(t *testing.T) {
	workspace := t.TempDir()
	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil, nil, nil, resolveEnvKnobs())

	// Relative paths still resolve against the workspace, so a relative escape is
	// rejected as malformed (not as a security boundary — see the package's YOLO
	// security model: absolute paths and bash bypass this).
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

	// Absolute paths are unrestricted: writes land wherever the process can write.
	absolutePath := filepath.Join(t.TempDir(), "absolute.txt")
	written := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_2",
		Name:      "write",
		Arguments: `{"path":` + mustJSON(absolutePath) + `,"content":"ok\n"}`,
	})
	if written.IsError {
		t.Fatalf("expected absolute path write to succeed: %+v", written)
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

func TestNativeToolsLogsOutsideWorkspaceAccess(t *testing.T) {
	workspace := t.TempDir()
	logPath := filepath.Join(workspace, ".turn", "session.jsonl")
	logger := newNativeSessionLogger(logPath, workspace)
	if err := logger.start(); err != nil {
		t.Fatal(err)
	}

	outsideDir := t.TempDir()
	outsideReadPath := filepath.Join(outsideDir, "outside.txt")
	if err := os.WriteFile(outsideReadPath, []byte("outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tools := newNativeTools(platform.NewLocalPlatform(workspace), nil, nil, logger, resolveEnvKnobs())
	read := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_read",
		Name:      "read_file",
		Arguments: `{"path":` + mustJSON(outsideReadPath) + `}`,
	})
	if read.IsError {
		t.Fatalf("absolute read should succeed: %+v", read)
	}
	writePath := filepath.Join(t.TempDir(), "out.txt")
	write := tools.execute(context.Background(), nativeToolCall{
		ID:        "call_write",
		Name:      "write_file",
		Arguments: `{"path":` + mustJSON(writePath) + `,"content":"ok\n"}`,
	})
	if write.IsError {
		t.Fatalf("safe absolute write should succeed: %+v", write)
	}

	events := sessionLogEventsByType(t, logPath, "outside_workspace_access")
	if len(events) != 2 {
		t.Fatalf("outside access events: got %d events=%#v", len(events), events)
	}
	if events[0]["action"] != "read_file" || events[0]["write"] != false || events[0]["path"] != filepath.ToSlash(outsideReadPath) {
		t.Fatalf("read event: %#v", events[0])
	}
	if events[1]["action"] != "write_file" || events[1]["write"] != true || events[1]["path"] != filepath.ToSlash(writePath) {
		t.Fatalf("write event: %#v", events[1])
	}
}

func sessionLogEventsByType(t *testing.T, path string, eventType string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var event sessionEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("parse session event: %v", err)
		}
		if event.Type == eventType {
			out = append(out, event.Data)
		}
	}
	return out
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

func writeResponsesIncompleteStream(w http.ResponseWriter, responseJSON string) {
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(`{"type":"response.incomplete","response":`+responseJSON+"}")); err != nil {
		panic(err)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "data: "+buf.String()+"\n\n")
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openai/openai-go/responses"
)

func TestResponsesClientUnderBudgetSkipsCompaction(t *testing.T) {
	t.Setenv("TELOS_AUTOCOMPACT_CONTEXT_WINDOW", "100000")
	t.Setenv("TELOS_AUTOCOMPACT_TRIGGER_RATIO", "1")
	t.Setenv("TELOS_AUTOCOMPACT_KEEP_RECENT_TOKENS", "100")

	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(requestInputText(t, body), compactionCommand) {
			t.Fatalf("under-budget send must not request compaction:\n%s", body)
		}
		writeResponsesStream(w, responseWithText("resp_normal", "Done."))
	}))
	defer server.Close()

	client := newTestResponsesClient(t, server.URL, "task")
	turn, err := client.send(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if turn.text != "Done." {
		t.Fatalf("turn text: %q", turn.text)
	}
	if requests != 1 {
		t.Fatalf("requests: got %d want 1", requests)
	}
}

func TestResponsesClientCompactsBeforeNormalRequest(t *testing.T) {
	t.Setenv("TELOS_AUTOCOMPACT_CONTEXT_WINDOW", "500")
	t.Setenv("TELOS_AUTOCOMPACT_TRIGGER_RATIO", "0.5")
	t.Setenv("TELOS_AUTOCOMPACT_KEEP_RECENT_TOKENS", "20")

	summary := validCompactionSummary("from llm")
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, string(body))
		switch len(requests) {
		case 1:
			if !strings.Contains(string(body), compactionCommand) {
				t.Fatalf("first request should be compaction:\n%s", body)
			}
			if !strings.Contains(string(body), "old fact") || !strings.Contains(string(body), `"tools"`) {
				t.Fatalf("compaction request should include old history and tools:\n%s", body)
			}
			if instructions := requestStringField(t, body, "instructions"); !strings.Contains(instructions, "COMPACT_SESSION_STATE") {
				t.Fatalf("instructions should include compaction mode:\n%s", instructions)
			}
			writeResponsesStream(w, responseWithText("resp_compact", summary))
		case 2:
			inputText := requestInputText(t, body)
			if strings.Contains(inputText, compactionCommand) {
				t.Fatalf("second request should be normal agent request:\n%s", body)
			}
			for _, want := range []string{"Compacted prior session state", "from llm"} {
				if !strings.Contains(inputText, want) {
					t.Fatalf("normal request missing %q:\n%s", want, body)
				}
			}
			if strings.Contains(inputText, "old fact") {
				t.Fatalf("normal request should not include summarized raw history:\n%s", body)
			}
			writeResponsesStream(w, responseWithText("resp_normal", "Continued."))
		default:
			t.Fatalf("unexpected extra request %d", len(requests))
		}
	}))
	defer server.Close()

	client := newTestResponsesClient(t, server.URL, "task")
	client.state.history = responses.ResponseInputParam{
		messageItem("task"),
		messageItem("old fact " + strings.Repeat("x", 2000)),
		messageItem("recent fact"),
	}
	turn, err := client.send(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if turn.text != "Continued." {
		t.Fatalf("turn text: %q", turn.text)
	}
	if len(requests) != 2 {
		t.Fatalf("requests: got %d want 2", len(requests))
	}

	events := sessionLogEventsByType(t, client.logger.path, "compaction")
	if len(events) != 1 {
		t.Fatalf("compaction events: %#v", events)
	}
	if events[0]["first_kept_index"] == nil || events[0]["response_id"] != "resp_compact" {
		t.Fatalf("compaction metadata missing: %#v", events[0])
	}
}

func TestResponsesClientInvalidCompactionFallsBackToTruncate(t *testing.T) {
	t.Setenv("TELOS_AUTOCOMPACT_CONTEXT_WINDOW", "500")
	t.Setenv("TELOS_AUTOCOMPACT_TRIGGER_RATIO", "0.5")
	t.Setenv("TELOS_AUTOCOMPACT_KEEP_RECENT_TOKENS", "20")

	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			// Compaction attempt returns a malformed summary.
			writeResponsesStream(w, responseWithText("resp_bad", "not a valid summary"))
			return
		}
		writeResponsesStream(w, responseWithText("resp_normal", "Continued."))
	}))
	defer server.Close()

	client := newTestResponsesClient(t, server.URL, "task")
	client.state.history = responses.ResponseInputParam{
		messageItem("task"),
		messageItem("old " + strings.Repeat("x", 2000)),
		messageItem("recent"),
	}
	turn, err := client.send(context.Background())
	if err != nil {
		t.Fatalf("malformed summary should degrade to truncate, not fail the turn: %v", err)
	}
	if turn.text != "Continued." {
		t.Fatalf("turn text: %q", turn.text)
	}
	if requests != 2 {
		t.Fatalf("expected compaction attempt + normal request, requests=%d", requests)
	}
	// The wasted compaction call's tokens are still charged to the turn: the
	// malformed attempt (11/7/3) plus the normal response (11/7/3).
	if turn.stats.InputTokens != 22 || turn.stats.OutputTokens != 14 || turn.stats.CacheReadTokens != 6 {
		t.Fatalf("compaction spend not folded into turn stats: %+v", turn.stats)
	}
	events := sessionLogEventsByType(t, client.logger.path, "compaction")
	// One failure event for the malformed summary, one for the truncate fallback.
	if len(events) != 2 {
		t.Fatalf("expected failure + truncate compaction events: %#v", events)
	}
	if events[0]["error"] == nil {
		t.Fatalf("first compaction event should record the summary failure: %#v", events[0])
	}
	if events[1]["reason"] != "token_budget_naive_cutoff" {
		t.Fatalf("second compaction event should be the truncate fallback: %#v", events[1])
	}
}

func TestResponsesClientFoldsCompactionSpendIntoTurnStats(t *testing.T) {
	t.Setenv("TELOS_AUTOCOMPACT_CONTEXT_WINDOW", "500")
	t.Setenv("TELOS_AUTOCOMPACT_TRIGGER_RATIO", "0.5")
	t.Setenv("TELOS_AUTOCOMPACT_KEEP_RECENT_TOKENS", "20")

	summary := validCompactionSummary("from llm")
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			writeResponsesStream(w, responseWithText("resp_compact", summary))
			return
		}
		writeResponsesStream(w, responseWithText("resp_normal", "Continued."))
	}))
	defer server.Close()

	client := newTestResponsesClient(t, server.URL, "task")
	client.state.history = responses.ResponseInputParam{
		messageItem("task"),
		messageItem("old fact " + strings.Repeat("x", 2000)),
		messageItem("recent fact"),
	}
	turn, err := client.send(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("requests: got %d want 2", requests)
	}
	// The successful summary call (11/7/3) plus the normal response (11/7/3) must
	// both be metered against the turn, not just logged to session.jsonl.
	if turn.stats.InputTokens != 22 || turn.stats.OutputTokens != 14 || turn.stats.CacheReadTokens != 6 {
		t.Fatalf("compaction spend not folded into turn stats: %+v", turn.stats)
	}
}

func TestResponsesClientNaiveCutoffDropsOldHistoryWithoutLLMCall(t *testing.T) {
	t.Setenv("TELOS_AUTOCOMPACT_CONTEXT_WINDOW", "500")
	t.Setenv("TELOS_AUTOCOMPACT_TRIGGER_RATIO", "0.5")
	t.Setenv("TELOS_AUTOCOMPACT_KEEP_RECENT_TOKENS", "20")
	t.Setenv("TELOS_AUTOCOMPACT_STRATEGY", "truncate")

	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, string(body))
		inputText := requestInputText(t, body)
		if strings.Contains(inputText, compactionCommand) || strings.Contains(inputText, "old fact") {
			t.Fatalf("truncate strategy should send task + recent history only:\n%s", body)
		}
		writeResponsesStream(w, responseWithText("resp_normal", "Continued."))
	}))
	defer server.Close()

	client := newTestResponsesClient(t, server.URL, "task")
	client.state.history = responses.ResponseInputParam{
		messageItem("task"),
		messageItem("old fact " + strings.Repeat("x", 2000)),
		messageItem("recent fact"),
	}
	turn, err := client.send(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if turn.text != "Continued." {
		t.Fatalf("turn text: %q", turn.text)
	}
	if len(requests) != 1 {
		t.Fatalf("truncate strategy should not call the LLM summarizer separately, requests=%d", len(requests))
	}
	events := sessionLogEventsByType(t, client.logger.path, "compaction")
	if len(events) != 1 || events[0]["reason"] != "token_budget_naive_cutoff" {
		t.Fatalf("naive compaction event missing: %#v", events)
	}
}

func TestResponsesClientLLMCompactionPreservesAnchorNaiveLoses(t *testing.T) {
	const anchor = "ZEUS-314159"
	ledger := "ledger fact: ANCHOR_CODE = " + anchor + "\n" + strings.Repeat("filler only\n", 2000)
	recent := "recent constraints mention answer.json but not the old anchor"

	run := func(t *testing.T, strategy string) string {
		t.Helper()
		t.Setenv("TELOS_AUTOCOMPACT_CONTEXT_WINDOW", "700")
		t.Setenv("TELOS_AUTOCOMPACT_TRIGGER_RATIO", "0.5")
		t.Setenv("TELOS_AUTOCOMPACT_KEEP_RECENT_TOKENS", "40")
		t.Setenv("TELOS_AUTOCOMPACT_STRATEGY", strategy)

		var normalRequest string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			inputText := requestInputText(t, body)
			if strings.Contains(inputText, compactionCommand) {
				if !strings.Contains(inputText, anchor) {
					t.Fatalf("compaction request lost the raw anchor:\n%s", body)
				}
				writeResponsesStream(w, responseWithText("resp_compact", validCompactionSummary("anchor "+anchor)))
				return
			}
			normalRequest = inputText
			if strings.Contains(inputText, anchor) {
				writeResponsesStream(w, responseWithText("resp_normal", "success: "+anchor))
				return
			}
			writeResponsesStream(w, responseWithText("resp_normal", "failure: missing anchor"))
		}))
		defer server.Close()

		client := newTestResponsesClient(t, server.URL, "task with no anchor")
		client.state.history = responses.ResponseInputParam{
			messageItem("task with no anchor"),
			messageItem(ledger),
			messageItem(recent),
		}
		turn, err := client.send(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if strategy == compactionStrategyLLM && !strings.Contains(normalRequest, "Compacted prior session state") {
			t.Fatalf("LLM strategy normal request missing compaction summary:\n%s", normalRequest)
		}
		if strategy == compactionStrategyTruncate && strings.Contains(normalRequest, "Compacted prior session state") {
			t.Fatalf("truncate strategy should not inject an LLM summary:\n%s", normalRequest)
		}
		return turn.text
	}

	llm := run(t, compactionStrategyLLM)
	naive := run(t, compactionStrategyTruncate)
	if !strings.Contains(llm, "success") {
		t.Fatalf("LLM compaction should preserve anchor, got %q", llm)
	}
	if !strings.Contains(naive, "failure") {
		t.Fatalf("naive cutoff should lose old-only anchor, got %q", naive)
	}
}

func TestResponsesClientServerChainIgnoresCompaction(t *testing.T) {
	t.Setenv("TELOS_MODEL_STATE_MODE", "server_chain")
	t.Setenv("TELOS_AUTOCOMPACT_CONTEXT_WINDOW", "500")
	t.Setenv("TELOS_AUTOCOMPACT_TRIGGER_RATIO", "0.5")
	t.Setenv("TELOS_AUTOCOMPACT_KEEP_RECENT_TOKENS", "20")

	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(requestInputText(t, body), compactionCommand) {
			t.Fatalf("server_chain must not compact:\n%s", body)
		}
		writeResponsesStream(w, responseWithText("resp_normal", "Done."))
	}))
	defer server.Close()

	client := newTestResponsesClient(t, server.URL, "task")
	client.state.mode = conversationStateServerChain
	client.state.history = append(client.state.history, messageItem(strings.Repeat("x", 900)))
	if _, err := client.send(context.Background()); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("requests: got %d want 1", requests)
	}
}

func TestResponsesClientRetriesTransientCompactionError(t *testing.T) {
	t.Setenv("TELOS_AUTOCOMPACT_CONTEXT_WINDOW", "500")
	t.Setenv("TELOS_AUTOCOMPACT_TRIGGER_RATIO", "0.5")
	t.Setenv("TELOS_AUTOCOMPACT_KEEP_RECENT_TOKENS", "20")

	summary := validCompactionSummary("from llm")
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch requests {
		case 1:
			// First compaction attempt fails with a retryable upstream error.
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `data: {"type":"response.failed","response":{"id":"resp_fail","status":"failed","error":{"message":"transient upstream error"}}}`+"\n\n")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		case 2:
			writeResponsesStream(w, responseWithText("resp_compact", summary))
		default:
			writeResponsesStream(w, responseWithText("resp_normal", "Continued."))
		}
	}))
	defer server.Close()

	client := newTestResponsesClient(t, server.URL, "task")
	client.state.history = responses.ResponseInputParam{
		messageItem("task"),
		messageItem("old fact " + strings.Repeat("x", 2000)),
		messageItem("recent fact"),
	}
	turn, err := client.send(context.Background())
	if err != nil {
		t.Fatalf("transient compaction error should be retried, not fail the turn: %v", err)
	}
	if turn.text != "Continued." {
		t.Fatalf("turn text: %q", turn.text)
	}
	if requests != 3 {
		t.Fatalf("expected failed attempt + retry + normal request, requests=%d", requests)
	}
	if len(sessionLogEventsByType(t, client.logger.path, "retry")) == 0 {
		t.Fatal("expected a retry event for the transient compaction failure")
	}
}

func newTestResponsesClient(t *testing.T, baseURL, task string) *responsesClient {
	t.Helper()
	logger := newNativeSessionLogger(filepath.Join(t.TempDir(), "session.jsonl"), t.TempDir())
	if err := logger.start(); err != nil {
		t.Fatal(err)
	}
	cfg := nativeProviderConfig{
		Model:   "test/test-model",
		BaseURL: baseURL,
		APIKey:  "test-key",
		Capability: modelCapabilityProfile{
			StateMode: "stateless_history",
		},
	}
	return newResponsesClient(nil, cfg, "high", 0, task, "prover", logger)
}

func responseWithText(id, text string) string {
	data, _ := json.Marshal(text)
	return `{"id":"` + id + `","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":` + string(data) + `}]}],"usage":{"input_tokens":11,"output_tokens":7,"input_tokens_details":{"cached_tokens":3}}}`
}

func requestInputText(t *testing.T, body []byte) string {
	t.Helper()
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(req["input"])
	return string(data)
}

func requestStringField(t *testing.T, body []byte, key string) string {
	t.Helper()
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	value, _ := req[key].(string)
	return value
}

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
	"testing"

	"github.com/telos-org/telos/internal/config"
	"github.com/telos-org/telos/internal/executor/providercore"
	"github.com/telos-org/telos/internal/gatewaycred"
	"github.com/telos-org/telos/internal/oauthcred"
)

func TestAnthropicAdapterRequestAndNormalize(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "anthropic-key" || r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Fatalf("headers: %v", r.Header)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["system"] != "system prompt" || body["model"] != "claude-test" {
			t.Fatalf("body=%v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":          "msg_1",
			"stop_reason": "tool_use",
			"content": []map[string]any{
				{"type": "text", "text": "hello"},
				{"type": "tool_use", "id": "tool_1", "name": "read_file", "input": map[string]any{"path": "a.txt"}},
			},
			"usage": map[string]any{"input_tokens": 12, "output_tokens": 5},
		})
	}))
	defer server.Close()
	adapter := newProviderCoreAdapter(server.Client(), nativeProviderConfig{
		Protocol: gatewaycred.ProviderAnthropic,
		BaseURL:  server.URL,
		APIKey:   "anthropic-key",
	}, 64, nil)
	got, err := adapter.Complete(context.Background(), coreTestRequest("claude-test"), 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "hello" || got.ID != "msg_1" || got.Usage.InputTokens != 12 || len(got.ToolCalls) != 1 {
		t.Fatalf("response=%#v", got)
	}
	if got.ToolCalls[0].Name != "read_file" || got.ToolCalls[0].Arguments != `{"path":"a.txt"}` {
		t.Fatalf("tool call=%#v", got.ToolCalls[0])
	}
}

func TestGeminiAdapterRequestAndNormalize(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/gemini-test:generateContent" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		if r.Header.Get("x-goog-api-key") != "gemini-key" {
			t.Fatalf("headers: %v", r.Header)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if _, ok := body["systemInstruction"]; !ok {
			t.Fatalf("missing systemInstruction: %v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"candidates": []map[string]any{{
				"finishReason": "STOP",
				"content": map[string]any{"parts": []map[string]any{
					{"text": "hello"},
					{"functionCall": map[string]any{"name": "read_file", "args": map[string]any{"path": "a.txt"}}},
				}},
			}},
			"usageMetadata": map[string]any{"promptTokenCount": 10, "candidatesTokenCount": 4, "cachedContentTokenCount": 2},
		})
	}))
	defer server.Close()
	adapter := newProviderCoreAdapter(server.Client(), nativeProviderConfig{
		Protocol: gatewaycred.ProviderGemini,
		BaseURL:  server.URL,
		APIKey:   "gemini-key",
	}, 64, nil)
	got, err := adapter.Complete(context.Background(), coreTestRequest("gemini-test"), 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "hello" || got.Usage.CachedInputTokens != 2 || len(got.ToolCalls) != 1 {
		t.Fatalf("response=%#v", got)
	}
	if got.ToolCalls[0].ID == "" || got.ToolCalls[0].Arguments != `{"path":"a.txt"}` {
		t.Fatalf("tool call=%#v", got.ToolCalls[0])
	}
}

func TestCodexAdapterRequestHeadersAndRefresh(t *testing.T) {
	temp := t.TempDir()
	t.Setenv(config.ConfigPathEnv, filepath.Join(temp, "config.yaml"))
	if err := oauthcred.Save(oauthcred.StorePath(config.ConfigPath()), oauthcred.Token{AccessToken: "old-access", RefreshToken: "refresh", AccountID: "acct_1"}); err != nil {
		t.Fatal(err)
	}
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("refresh_token") != "refresh" {
			t.Fatalf("refresh form=%v", r.Form)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "new-access", "expires_in": 60})
	}))
	defer refreshServer.Close()
	oldTokenURL := oauthcred.TokenURL
	oauthcred.TokenURL = refreshServer.URL
	defer func() { oauthcred.TokenURL = oldTokenURL }()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/backend-api/codex/responses" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		if r.Header.Get("chatgpt-account-id") != "acct_1" || r.Header.Get("session_id") == "" {
			t.Fatalf("headers: %v", r.Header)
		}
		if calls == 1 {
			if r.Header.Get("Authorization") != "Bearer old-access" {
				t.Fatalf("first auth=%q", r.Header.Get("Authorization"))
			}
			http.Error(w, "expired old-access", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Authorization") != "Bearer new-access" {
			t.Fatalf("second auth=%q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":2}}}\n\n")
	}))
	defer server.Close()
	adapter := newProviderCoreAdapter(server.Client(), nativeProviderConfig{
		Protocol: gatewaycred.ProviderCodex,
		BaseURL:  server.URL,
		APIKey:   "old-access",
		Headers:  map[string]string{"chatgpt-account-id": "acct_1"},
	}, 64, nil)
	got, err := adapter.Complete(context.Background(), coreTestRequest("gpt-5"), 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "ok" || got.ID != "resp_1" || calls != 2 {
		t.Fatalf("response=%#v calls=%d", got, calls)
	}
}

func TestProviderContextLimitClassification(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider gatewaycred.Provider
		path     string
	}{
		{"anthropic", gatewaycred.ProviderAnthropic, "/v1/messages"},
		{"gemini", gatewaycred.ProviderGemini, "/v1beta/models/model:generateContent"},
		{"codex", gatewaycred.ProviderCodex, "/backend-api/codex/responses"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.path {
					t.Fatalf("path=%s want %s", r.URL.Path, tc.path)
				}
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "prompt is too long"}})
			}))
			defer server.Close()
			adapter := newProviderCoreAdapter(server.Client(), nativeProviderConfig{
				Protocol: tc.provider,
				BaseURL:  server.URL,
				APIKey:   "secret-token",
			}, 64, nil)
			_, err := adapter.Complete(context.Background(), coreTestRequest("model"), 1)
			var execErr *executorError
			if !errors.As(err, &execErr) || execErr.Code != errProviderContextLimit {
				t.Fatalf("err=%v", err)
			}
			if execErr.Message == "secret-token" {
				t.Fatalf("secret leaked in error: %v", execErr)
			}
		})
	}
}

func TestReplayFixtureNormalizesEquivalentProviderResponses(t *testing.T) {
	anthropicFixture := anthropicResponse{
		ID:         "a1",
		StopReason: "tool_use",
		Content: []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}{
			{Type: "text", Text: "hello"},
			{Type: "tool_use", ID: "call_1", Name: "read_file", Input: json.RawMessage(`{"path":"a.txt"}`)},
		},
	}
	anthropicFixture.Usage.InputTokens = 1
	anthropicFixture.Usage.OutputTokens = 2
	geminiFixture := geminiResponse{
		Candidates: []struct {
			FinishReason string `json:"finishReason"`
			Content      struct {
				Parts []struct {
					Text         string `json:"text"`
					FunctionCall *struct {
						ID   string          `json:"id"`
						Name string          `json:"name"`
						Args json.RawMessage `json:"args"`
					} `json:"functionCall"`
				} `json:"parts"`
			} `json:"content"`
		}{{
			FinishReason: "STOP",
			Content: struct {
				Parts []struct {
					Text         string `json:"text"`
					FunctionCall *struct {
						ID   string          `json:"id"`
						Name string          `json:"name"`
						Args json.RawMessage `json:"args"`
					} `json:"functionCall"`
				} `json:"parts"`
			}{Parts: []struct {
				Text         string `json:"text"`
				FunctionCall *struct {
					ID   string          `json:"id"`
					Name string          `json:"name"`
					Args json.RawMessage `json:"args"`
				} `json:"functionCall"`
			}{
				{Text: "hello"},
				{FunctionCall: &struct {
					ID   string          `json:"id"`
					Name string          `json:"name"`
					Args json.RawMessage `json:"args"`
				}{ID: "call_1", Name: "read_file", Args: json.RawMessage(`{"path":"a.txt"}`)}},
			}},
		}},
	}
	gotA := normalizeAnthropicResponse(anthropicFixture)
	gotG := normalizeGeminiResponse(geminiFixture)
	if gotA.Text != gotG.Text || len(gotA.ToolCalls) != 1 || len(gotG.ToolCalls) != 1 || gotA.ToolCalls[0] != gotG.ToolCalls[0] {
		t.Fatalf("normalized mismatch:\nanthropic=%#v\ngemini=%#v", gotA, gotG)
	}
}

func coreTestRequest(model string) providercore.Request {
	return providercore.Request{
		Model:           model,
		System:          "system prompt",
		MaxOutputTokens: 64,
		Messages:        []providercore.Message{{Role: providercore.RoleUser, Content: "hello"}},
		Tools: []providercore.ToolDefinition{{
			Name:        "read_file",
			Description: "read",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}, "additionalProperties": false},
		}},
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

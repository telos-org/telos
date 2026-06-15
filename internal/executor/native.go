package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/telos-org/telos/internal/game"
	"github.com/telos-org/telos/internal/platform"
)

const (
	defaultMaxToolLoops    = 80
	defaultMaxOutputTokens = 4096
	defaultToolTimeoutSec  = 120
	httpMaxAttempts        = 3
)

// httpRetryBackoff is the base delay between transient HTTP retries. It is a
// var so tests can shrink it.
var httpRetryBackoff = 500 * time.Millisecond

// NativeExecutor runs one PVG turn with Telos' built-in coding harness.
type NativeExecutor struct {
	Platform *platform.LocalPlatform
	Model    string
	Thinking string
	Timeout  int
	Client   *http.Client
}

func nativeMaxToolLoops() int {
	raw := strings.TrimSpace(os.Getenv("TELOS_NATIVE_MAX_TOOL_LOOPS"))
	if raw == "" {
		return defaultMaxToolLoops
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return defaultMaxToolLoops
	}
	return n
}

func nativeMaxOutputTokens() int {
	raw := strings.TrimSpace(os.Getenv("TELOS_NATIVE_MAX_OUTPUT_TOKENS"))
	if raw == "" {
		return defaultMaxOutputTokens
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 256 {
		return defaultMaxOutputTokens
	}
	return n
}

// NewNativeExecutor creates a native Go coding-agent executor.
func NewNativeExecutor(p *platform.LocalPlatform, model, thinking string, timeout int) *NativeExecutor {
	if thinking == "" {
		thinking = "medium"
	}
	return &NativeExecutor{
		Platform: p,
		Model:    model,
		Thinking: thinking,
		Timeout:  timeout,
		Client:   http.DefaultClient,
	}
}

// ExecuteTurn runs one Telos-native agent turn.
func (ne *NativeExecutor) ExecuteTurn(task string, role string, turnState *game.TurnState) game.TurnResult {
	started := time.Now()
	stats := game.TurnStats{Model: ne.Model}
	sessionPath := ""
	var stopRequested func() bool
	if turnState != nil {
		sessionPath = turnState.SessionPath()
		stopRequested = turnState.StopRequested
	}

	ctx, cancel := turnContext(ne.Timeout, stopRequested)
	defer cancel()

	cfg, err := resolveNativeProvider(ne.Model)
	if err != nil {
		return recoverableTurn(role, stats, "agent_config_error:"+err.Error())
	}
	logger := newNativeSessionLogger(sessionPath, ne.Platform.Workspace)
	if err := logger.start(); err != nil {
		return recoverableTurn(role, stats, "native_session_unavailable:"+err.Error())
	}
	_ = logger.user(task)

	tools := newNativeTools(ne.Platform, stopRequested)
	loop := newAgentLoop(httpPoster{http: ne.Client, cfg: cfg}, cfg, ne.Thinking, tools, logger, task, role)

	logs, extraStats, err := loop.run(ctx)
	stats = mergeTurnStats(stats, extraStats)
	stats.DurationMS = int(time.Since(started).Milliseconds())
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return recoverableTurn(role, stats, fmt.Sprintf("native_timeout:%d", ne.Timeout))
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return recoverableTurn(role, stats, "native_interrupted:stop_requested")
		}
		return recoverableTurn(role, stats, err.Error())
	}
	if strings.TrimSpace(logs) == "" {
		return recoverableTurn(role, stats, "agent_no_output")
	}
	return game.TurnResult{
		Role:   role,
		Status: game.ExtractStatus(logs),
		Logs:   logs,
		Stats:  stats,
	}
}

// turnContext builds the turn context, applying the optional timeout and
// wiring stop requests into the same cancellation source the tools observe.
func turnContext(timeout int, stopRequested func() bool) (context.Context, context.CancelFunc) {
	ctx := context.Background()
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	if stopRequested != nil {
		go func() {
			ticker := time.NewTicker(200 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if stopRequested() {
						cancel()
						return
					}
				}
			}
		}()
	}
	return ctx, cancel
}

// WorkspaceState returns the workspace state from the platform.
func (ne *NativeExecutor) WorkspaceState() string {
	return ne.Platform.WorkspaceState()
}

// CheckpointWorkspace creates a workspace checkpoint.
func (ne *NativeExecutor) CheckpointWorkspace(dest string) bool {
	return ne.Platform.CheckpointWorkspace(dest)
}

func recoverableTurn(role string, stats game.TurnStats, reason string) game.TurnResult {
	return game.TurnResult{
		Role:        role,
		Status:      game.StatusContinue,
		Logs:        reason,
		Stats:       stats,
		Error:       reason,
		Recoverable: true,
	}
}

// -- Provider resolution -----------------------------------------------------

type providerStyle string

const (
	providerChat      providerStyle = "openai-chat"
	providerResponses providerStyle = "openai-responses"
	providerAnthropic providerStyle = "anthropic"
)

type nativeProviderConfig struct {
	Provider string
	Model    string
	BaseURL  string
	APIKey   string
	Style    providerStyle
}

// providerDefaults describes a known provider's baked-in endpoint conventions.
type providerDefaults struct {
	baseURL string
	keyEnv  string
	style   providerStyle
}

func nativeProviderRegistry() map[string]providerDefaults {
	return map[string]providerDefaults{
		"silares":       {"https://api.silares.com/v1", "SILARES_API_KEY", providerChat},
		"sail-research": {"https://api.sailresearch.com/v1", "SAIL_API_KEY", providerChat},
		"moonshot":      {"https://api.moonshot.ai/v1", "MOONSHOT_API_KEY", providerChat},
		"xai":           {"https://api.x.ai/v1", "XAI_API_KEY", providerChat},
		"openai":        {"https://api.openai.com/v1", "OPENAI_API_KEY", providerChat},
		"openai-codex":  {"https://api.openai.com/v1", "OPENAI_API_KEY", providerChat},
		"anthropic":     {"https://api.anthropic.com/v1", "ANTHROPIC_API_KEY", providerAnthropic},
	}
}

func resolveNativeProvider(model string) (nativeProviderConfig, error) {
	if model == "" {
		return nativeProviderConfig{}, fmt.Errorf("model is required")
	}
	if base := firstEnv("TELOS_API_BASE_URL", "TELOS_BASE_URL"); base != "" {
		key := firstEnv("TELOS_API_KEY")
		if key == "" {
			return nativeProviderConfig{}, fmt.Errorf("TELOS_API_KEY is required when TELOS_API_BASE_URL is set")
		}
		return nativeProviderConfig{
			Provider: "telos",
			Model:    stripProviderPrefix(model),
			BaseURL:  strings.TrimRight(base, "/"),
			APIKey:   key,
			Style:    providerStyle(orDefault(firstEnv("TELOS_API_STYLE"), string(providerChat))),
		}, nil
	}

	provider, providerModel := splitProviderModel(model)
	registry := nativeProviderRegistry()
	if def, ok := registry[provider]; ok {
		return providerFromDefaults(provider, providerModel, def)
	}
	if strings.HasPrefix(model, "claude-") {
		return providerFromDefaults("anthropic", model, registry["anthropic"])
	}
	if provider != "" {
		envPrefix := providerEnvPrefix(provider)
		keyName := envPrefix + "_API_KEY"
		base := firstEnv(envPrefix + "_BASE_URL")
		key := firstEnv(keyName)
		if base == "" || key == "" {
			return nativeProviderConfig{}, fmt.Errorf("unknown provider %q; set %s_BASE_URL and %s", provider, envPrefix, keyName)
		}
		return nativeProviderConfig{
			Provider: provider,
			Model:    providerModel,
			BaseURL:  strings.TrimRight(base, "/"),
			APIKey:   key,
			Style:    providerStyle(orDefault(firstEnv(envPrefix+"_API_STYLE"), string(providerChat))),
		}, nil
	}
	return providerFromDefaults("openai", model, registry["openai"])
}

func providerFromDefaults(provider, model string, def providerDefaults) (nativeProviderConfig, error) {
	envPrefix := providerEnvPrefix(provider)
	base := firstEnv(envPrefix + "_BASE_URL")
	if base == "" && provider == "openai-codex" {
		// openai-codex shares OpenAI's base URL when no codex-specific override is set.
		base = firstEnv("OPENAI_BASE_URL")
	}
	if base == "" {
		base = def.baseURL
	}
	key := firstEnv(def.keyEnv)
	if key == "" {
		return nativeProviderConfig{}, fmt.Errorf("%s is required for model provider %q", def.keyEnv, provider)
	}
	style := def.style
	if override := firstEnv(envPrefix + "_API_STYLE"); override != "" {
		style = providerStyle(override)
	}
	return nativeProviderConfig{
		Provider: provider,
		Model:    model,
		BaseURL:  strings.TrimRight(base, "/"),
		APIKey:   key,
		Style:    style,
	}, nil
}

func splitProviderModel(model string) (string, string) {
	parts := strings.SplitN(model, "/", 2)
	if len(parts) != 2 {
		return "", model
	}
	return parts[0], parts[1]
}

func stripProviderPrefix(model string) string {
	_, m := splitProviderModel(model)
	return m
}

func providerEnvPrefix(provider string) string {
	s := strings.ToUpper(provider)
	return strings.NewReplacer("-", "_", ".", "_").Replace(s)
}

func firstEnv(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

// -- Agent loop --------------------------------------------------------------

// agentTurn is one model response, normalized across providers.
type agentTurn struct {
	text       string
	calls      []nativeToolCall
	stopReason string
	stats      game.TurnStats
}

// transport owns the provider-specific wire format and conversation state. The
// agent loop drives it without knowing which provider is underneath.
type transport interface {
	// send issues one model call against the accumulated conversation state.
	send(ctx context.Context) (agentTurn, error)
	// recordToolResults threads tool output back into the conversation.
	recordToolResults(results []nativeToolResult)
	// recordCorrection appends a retry prompt for an unproductive final.
	recordCorrection(prompt string)
}

type agentLoop struct {
	transport transport
	tools     *nativeTools
	logger    *nativeSessionLogger
	task      string
	provider  string
	model     string
}

func newAgentLoop(poster httpPoster, cfg nativeProviderConfig, thinking string, tools *nativeTools, logger *nativeSessionLogger, task, role string) *agentLoop {
	maxOut := nativeMaxOutputTokens()
	var tr transport
	switch cfg.Style {
	case providerResponses:
		tr = newResponsesTransport(poster, cfg.Model, thinking, maxOut, task, role)
	case providerAnthropic:
		tr = newAnthropicTransport(poster, cfg.Model, maxOut, task, role)
	default:
		tr = newChatTransport(poster, cfg.Model, maxOut, task, role)
	}
	return &agentLoop{
		transport: tr,
		tools:     tools,
		logger:    logger,
		task:      task,
		provider:  cfg.Provider,
		model:     cfg.Model,
	}
}

func (l *agentLoop) run(ctx context.Context) (string, game.TurnStats, error) {
	maxLoops := nativeMaxToolLoops()
	stats := game.TurnStats{Model: l.model}
	usedTools := false
	for i := 0; i < maxLoops; i++ {
		turn, err := l.transport.send(ctx)
		if err != nil {
			return "", stats, err
		}
		stats = mergeTurnStats(stats, turn.stats)
		_ = l.logger.assistant(turn.text, l.provider, l.model, turn.stopReason, turn.stats)

		if len(turn.calls) == 0 {
			if i+1 < maxLoops && shouldRetryNativeFinal(turn.text, l.task, turn.stopReason, usedTools) {
				l.transport.recordCorrection(nativeCorrectionPrompt(l.task))
				continue
			}
			return turn.text, stats, nil
		}

		results := l.tools.executeAll(ctx, turn.calls)
		usedTools = true
		stats.NumTurns += len(results)
		for _, result := range results {
			_ = l.logger.tool(result)
		}
		l.transport.recordToolResults(results)
	}
	return "", stats, fmt.Errorf("agent_tool_loop_exceeded:%d", maxLoops)
}

// -- Prompts -----------------------------------------------------------------

func nativeSystemPrompt(role string) string {
	return strings.Join([]string{
		"You are Telos' built-in coding harness running inside the benchmark workspace.",
		"The user message is the complete assignment for this turn. Do not ask the operator what to build or what to do next.",
		"If the spec names a deliverable, implement that deliverable exactly. An empty or minimal workspace means you should create the required files, not switch to a generic sample task.",
		"Keep your work anchored to the current spec text and live files. Ignore unrelated task ideas, default assistant personas, and prior benchmark examples that are not present in the current spec.",
		"Use the available tools directly. Do not ask for permission before inspecting or changing files required by the task.",
		"Prefer the first useful concrete workspace mutation over long planning. For file-producing tasks, create or edit the required files before summarizing.",
		fmt.Sprintf("Available tool names are %s. Do not call unavailable tool names such as write_file, ReadFile, Edit, apply_patch, or shell.", oxfordList(nativeToolNames())),
		"Keep all actionable content in visible assistant text. Do not put the final answer only in hidden reasoning.",
		"After tool work, end with a concise visible final response listing changed files and checks run. Include any XML tags required by the Telos turn instructions.",
		fmt.Sprintf("Current Telos role: %s.", role),
	}, "\n")
}

func nativeCorrectionPrompt(task string) string {
	return strings.Join([]string{
		"The assignment is already fully specified above. Do not ask what to build or what to do next.",
		"Implement the deliverable named in the assignment now. If the workspace is empty, create the required files directly.",
		"",
		"# Assignment",
		"",
		task,
	}, "\n")
}

func shouldRetryNativeFinal(text, task, stopReason string, usedTools bool) bool {
	if shouldRetryUnproductiveFinal(text, task, usedTools) {
		return true
	}
	if !usedTools && isLengthStop(stopReason) && len(assignmentFileAnchors(task)) > 0 {
		return true
	}
	return false
}

func isLengthStop(stopReason string) bool {
	switch strings.ToLower(strings.TrimSpace(stopReason)) {
	case "length", "max_tokens", "max_output_tokens":
		return true
	default:
		return false
	}
}

func shouldRetryUnproductiveFinal(text, task string, usedTools bool) bool {
	normalized := strings.ToLower(strings.TrimSpace(text))
	if normalized == "" {
		return true
	}
	if looksLikePendingWorkFinal(normalized) {
		return true
	}
	for _, marker := range []string{
		"what would you like me to work on",
		"what would you like me to do",
		"what would you like me to build",
		"how can i help you",
		"just describe what you need",
		"ready to help",
		"workspace is currently empty",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	anchors := assignmentFileAnchors(task)
	if !usedTools && len(anchors) > 0 {
		for _, anchor := range anchors {
			if strings.Contains(normalized, strings.ToLower(anchor)) {
				return false
			}
		}
		return true
	}
	return false
}

func looksLikePendingWorkFinal(normalized string) bool {
	for _, marker := range []string{
		"will now implement",
		"will now extend",
		"will now add",
		"will now write",
		"i will now implement",
		"i will now add",
		"i will now write",
		"i will implement",
		"i will add",
		"i will write",
		"i'll now implement",
		"i'll now add",
		"i'll now write",
		"i'll implement",
		"i'll add",
		"i'll write",
		"let's implement",
		"let's extend",
		"let's add",
		"let's write",
		"let me now implement",
		"let me now add",
		"let me now write",
		"let me implement",
		"let me add",
		"let me write",
		"let me code this up",
		"now implement the",
		"need to add",
		"need to implement",
		"need to write",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func assignmentFileAnchors(task string) []string {
	re := regexp.MustCompile("`([^`]+\\.(?:py|js|ts|tsx|jsx|go|rs|java|rb|php|sh|bash|md|json|yaml|yml|toml|xml|html|css|sql|txt))`")
	seen := map[string]bool{}
	var anchors []string
	for _, match := range re.FindAllStringSubmatch(task, -1) {
		anchor := strings.TrimSpace(match[1])
		if anchor == "" || seen[anchor] {
			continue
		}
		seen[anchor] = true
		anchors = append(anchors, anchor)
	}
	return anchors
}

// -- HTTP poster -------------------------------------------------------------

type apiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

type httpPoster struct {
	http *http.Client
	cfg  nativeProviderConfig
}

func (p httpPoster) client() *http.Client {
	if p.http != nil {
		return p.http
	}
	return http.DefaultClient
}

func (p httpPoster) postJSON(ctx context.Context, endpoint string, body, out interface{}) error {
	return p.post(ctx, endpoint, body, out, map[string]string{"Authorization": "Bearer " + p.cfg.APIKey})
}

func (p httpPoster) postAnthropic(ctx context.Context, endpoint string, body, out interface{}) error {
	return p.post(ctx, endpoint, body, out, map[string]string{
		"x-api-key":         p.cfg.APIKey,
		"anthropic-version": "2023-06-01",
	})
}

func (p httpPoster) post(ctx context.Context, endpoint string, body, out interface{}, headers map[string]string) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("provider_request_encode:%w", err)
	}
	url := p.cfg.BaseURL + endpoint
	var lastErr error
	for attempt := 0; attempt < httpMaxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(httpRetryBackoff * time.Duration(1<<(attempt-1))):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("provider_request_create:%w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		resp, err := p.client().Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			lastErr = fmt.Errorf("provider_request_failed:%w", err)
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if readErr != nil {
			lastErr = fmt.Errorf("provider_response_read:%w", readErr)
			continue
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if err := json.Unmarshal(data, out); err != nil {
				return fmt.Errorf("provider_response_decode:%w", err)
			}
			return nil
		}
		lastErr = fmt.Errorf("provider_http_%d:%s", resp.StatusCode, strings.TrimSpace(string(data)))
		if !isRetryableStatus(resp.StatusCode) {
			return lastErr
		}
	}
	return lastErr
}

func isRetryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

// -- OpenAI Chat Completions transport ---------------------------------------

type chatMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type chatToolCall struct {
	ID       string           `json:"id,omitempty"`
	Type     string           `json:"type,omitempty"`
	Function chatToolFunction `json:"function"`
}

type chatToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatUsage struct {
	PromptTokens       int `json:"prompt_tokens"`
	CompletionTokens   int `json:"completion_tokens"`
	PromptTokenDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

func statsFromChatUsage(model string, usage chatUsage) game.TurnStats {
	return game.TurnStats{
		Model:           model,
		InputTokens:     usage.PromptTokens,
		OutputTokens:    usage.CompletionTokens,
		CacheReadTokens: usage.PromptTokenDetails.CachedTokens,
	}
}

type chatTransport struct {
	poster          httpPoster
	model           string
	maxOutputTokens int
	schemas         []map[string]interface{}
	messages        []chatMessage
	last            chatMessage
}

func newChatTransport(poster httpPoster, model string, maxOutputTokens int, task, role string) *chatTransport {
	return &chatTransport{
		poster:          poster,
		model:           model,
		maxOutputTokens: maxOutputTokens,
		schemas:         toolSchemasForChat(),
		messages: []chatMessage{
			{Role: "system", Content: nativeSystemPrompt(role)},
			{Role: "user", Content: task},
		},
	}
}

func (t *chatTransport) send(ctx context.Context) (agentTurn, error) {
	var response struct {
		Choices []struct {
			Message      chatMessage `json:"message"`
			FinishReason string      `json:"finish_reason"`
		} `json:"choices"`
		Usage chatUsage `json:"usage"`
		Error apiError  `json:"error"`
	}
	req := map[string]interface{}{
		"model":       t.model,
		"messages":    t.messages,
		"tools":       t.schemas,
		"tool_choice": "auto",
		"max_tokens":  t.maxOutputTokens,
	}
	if err := t.poster.postJSON(ctx, "/chat/completions", req, &response); err != nil {
		return agentTurn{}, err
	}
	if response.Error.Message != "" {
		return agentTurn{}, fmt.Errorf("provider_error:%s", response.Error.Message)
	}
	if len(response.Choices) == 0 {
		return agentTurn{}, fmt.Errorf("provider_error:no_choices")
	}
	msg := response.Choices[0].Message
	if msg.Role == "" {
		msg.Role = "assistant"
	}
	t.last = msg
	return agentTurn{
		text:       msg.Content,
		calls:      chatToolCallsToNative(msg.ToolCalls),
		stopReason: response.Choices[0].FinishReason,
		stats:      statsFromChatUsage(t.model, response.Usage),
	}, nil
}

func (t *chatTransport) recordToolResults(results []nativeToolResult) {
	t.messages = append(t.messages, t.last)
	for _, result := range results {
		t.messages = append(t.messages, chatMessage{
			Role:       "tool",
			ToolCallID: result.CallID,
			Content:    result.Output,
		})
	}
}

func (t *chatTransport) recordCorrection(prompt string) {
	t.messages = append(t.messages, t.last, chatMessage{Role: "user", Content: prompt})
}

func chatToolCallsToNative(calls []chatToolCall) []nativeToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]nativeToolCall, 0, len(calls))
	for _, call := range calls {
		out = append(out, nativeToolCall{
			ID:        call.ID,
			Name:      call.Function.Name,
			Arguments: call.Function.Arguments,
		})
	}
	return out
}

// -- OpenAI Responses transport ----------------------------------------------

type responseItem struct {
	Type      string                   `json:"type"`
	ID        string                   `json:"id,omitempty"`
	CallID    string                   `json:"call_id,omitempty"`
	Name      string                   `json:"name,omitempty"`
	Arguments string                   `json:"arguments,omitempty"`
	Content   []map[string]interface{} `json:"content,omitempty"`
}

type responsesUsage struct {
	InputTokens       int `json:"input_tokens"`
	OutputTokens      int `json:"output_tokens"`
	InputTokenDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
}

func statsFromResponsesUsage(model string, usage responsesUsage) game.TurnStats {
	return game.TurnStats{
		Model:           model,
		InputTokens:     usage.InputTokens,
		OutputTokens:    usage.OutputTokens,
		CacheReadTokens: usage.InputTokenDetails.CachedTokens,
	}
}

func parseResponseOutput(output []responseItem) (string, []nativeToolCall) {
	var textParts []string
	var calls []nativeToolCall
	for _, item := range output {
		switch item.Type {
		case "message":
			for _, content := range item.Content {
				if text, _ := content["text"].(string); strings.TrimSpace(text) != "" {
					textParts = append(textParts, text)
				}
			}
		case "function_call":
			calls = append(calls, nativeToolCall{
				ID:        item.CallID,
				Name:      item.Name,
				Arguments: item.Arguments,
			})
		}
	}
	return strings.Join(textParts, ""), calls
}

type responsesTransport struct {
	poster          httpPoster
	model           string
	thinking        string
	maxOutputTokens int
	schemas         []map[string]interface{}
	input           []interface{}
	previousID      string
}

func newResponsesTransport(poster httpPoster, model, thinking string, maxOutputTokens int, task, role string) *responsesTransport {
	return &responsesTransport{
		poster:          poster,
		model:           model,
		thinking:        thinking,
		maxOutputTokens: maxOutputTokens,
		schemas:         toolSchemasForResponses(),
		input: []interface{}{
			map[string]interface{}{"role": "user", "content": nativeSystemPrompt(role) + "\n\n# Assignment\n\n" + task},
		},
	}
}

func (t *responsesTransport) send(ctx context.Context) (agentTurn, error) {
	var response struct {
		ID                string `json:"id"`
		Status            string `json:"status"`
		IncompleteDetails struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Output []responseItem `json:"output"`
		Usage  responsesUsage `json:"usage"`
		Error  apiError       `json:"error"`
	}
	req := map[string]interface{}{
		"model":             t.model,
		"input":             t.input,
		"tools":             t.schemas,
		"max_output_tokens": t.maxOutputTokens,
	}
	if t.previousID != "" {
		req["previous_response_id"] = t.previousID
	}
	if t.thinking != "" {
		req["reasoning"] = map[string]interface{}{"effort": t.thinking}
	}
	if err := t.poster.postJSON(ctx, "/responses", req, &response); err != nil {
		return agentTurn{}, err
	}
	if response.Error.Message != "" {
		return agentTurn{}, fmt.Errorf("provider_error:%s", response.Error.Message)
	}
	t.previousID = response.ID
	text, calls := parseResponseOutput(response.Output)
	stopReason := "stop"
	if response.Status == "incomplete" && response.IncompleteDetails.Reason != "" {
		stopReason = response.IncompleteDetails.Reason
	}
	return agentTurn{
		text:       text,
		calls:      calls,
		stopReason: stopReason,
		stats:      statsFromResponsesUsage(t.model, response.Usage),
	}, nil
}

func (t *responsesTransport) recordToolResults(results []nativeToolResult) {
	t.input = nil
	for _, result := range results {
		t.input = append(t.input, map[string]interface{}{
			"type":    "function_call_output",
			"call_id": result.CallID,
			"output":  result.Output,
		})
	}
}

func (t *responsesTransport) recordCorrection(prompt string) {
	t.input = []interface{}{
		map[string]interface{}{"role": "user", "content": prompt},
	}
}

// -- Anthropic Messages transport --------------------------------------------

type anthropicBlock struct {
	Type      string      `json:"type"`
	Text      string      `json:"text,omitempty"`
	ID        string      `json:"id,omitempty"`
	Name      string      `json:"name,omitempty"`
	Input     interface{} `json:"input,omitempty"`
	ToolUseID string      `json:"tool_use_id,omitempty"`
	Content   string      `json:"content,omitempty"`
	IsError   bool        `json:"is_error,omitempty"`
}

type anthropicMessage struct {
	Role    string           `json:"role"`
	Content []anthropicBlock `json:"content"`
}

func parseAnthropicOutput(blocks []anthropicBlock) (string, []nativeToolCall) {
	var textParts []string
	var calls []nativeToolCall
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if strings.TrimSpace(block.Text) != "" {
				textParts = append(textParts, block.Text)
			}
		case "tool_use":
			args, _ := json.Marshal(block.Input)
			calls = append(calls, nativeToolCall{ID: block.ID, Name: block.Name, Arguments: string(args)})
		}
	}
	return strings.Join(textParts, ""), calls
}

type anthropicTransport struct {
	poster          httpPoster
	model           string
	system          string
	maxOutputTokens int
	schemas         []map[string]interface{}
	messages        []anthropicMessage
	last            []anthropicBlock
}

func newAnthropicTransport(poster httpPoster, model string, maxOutputTokens int, task, role string) *anthropicTransport {
	return &anthropicTransport{
		poster:          poster,
		model:           model,
		system:          nativeSystemPrompt(role),
		maxOutputTokens: maxOutputTokens,
		schemas:         toolSchemasForAnthropic(),
		messages:        []anthropicMessage{{Role: "user", Content: []anthropicBlock{{Type: "text", Text: task}}}},
	}
}

func (t *anthropicTransport) send(ctx context.Context) (agentTurn, error) {
	var response struct {
		ID         string           `json:"id"`
		Content    []anthropicBlock `json:"content"`
		StopReason string           `json:"stop_reason"`
		Usage      struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
		Error apiError `json:"error"`
	}
	req := map[string]interface{}{
		"model":      t.model,
		"system":     t.system,
		"messages":   t.messages,
		"tools":      t.schemas,
		"max_tokens": t.maxOutputTokens,
	}
	if err := t.poster.postAnthropic(ctx, "/messages", req, &response); err != nil {
		return agentTurn{}, err
	}
	if response.Error.Message != "" {
		return agentTurn{}, fmt.Errorf("provider_error:%s", response.Error.Message)
	}
	t.last = response.Content
	text, calls := parseAnthropicOutput(response.Content)
	return agentTurn{
		text:       text,
		calls:      calls,
		stopReason: response.StopReason,
		stats: game.TurnStats{
			Model:               t.model,
			InputTokens:         response.Usage.InputTokens,
			OutputTokens:        response.Usage.OutputTokens,
			CacheReadTokens:     response.Usage.CacheReadInputTokens,
			CacheCreationTokens: response.Usage.CacheCreationInputTokens,
		},
	}, nil
}

func (t *anthropicTransport) recordToolResults(results []nativeToolResult) {
	t.messages = append(t.messages, anthropicMessage{Role: "assistant", Content: t.last})
	blocks := make([]anthropicBlock, 0, len(results))
	for _, result := range results {
		blocks = append(blocks, anthropicBlock{
			Type:      "tool_result",
			ToolUseID: result.CallID,
			Content:   result.Output,
			IsError:   result.IsError,
		})
	}
	t.messages = append(t.messages, anthropicMessage{Role: "user", Content: blocks})
}

func (t *anthropicTransport) recordCorrection(prompt string) {
	t.messages = append(t.messages,
		anthropicMessage{Role: "assistant", Content: t.last},
		anthropicMessage{Role: "user", Content: []anthropicBlock{{Type: "text", Text: prompt}}},
	)
}

// -- Tools -------------------------------------------------------------------

type nativeToolCall struct {
	ID        string
	Name      string
	Arguments string
}

type nativeToolResult struct {
	CallID  string
	Name    string
	Output  string
	IsError bool
}

// nativeTool is the single source of truth for a tool: its schema and its
// handler. Schema generation, dispatch, and the system-prompt name list all
// derive from the same table.
type nativeTool struct {
	name        string
	description string
	parameters  map[string]interface{}
	run         func(t *nativeTools, ctx context.Context, args map[string]interface{}) (string, error)
}

func nativeToolTable() []nativeTool {
	str := func(desc string) map[string]interface{} {
		return map[string]interface{}{"type": "string", "description": desc}
	}
	boolean := func(desc string) map[string]interface{} {
		return map[string]interface{}{"type": "boolean", "description": desc}
	}
	integer := func(desc string) map[string]interface{} {
		return map[string]interface{}{"type": "integer", "description": desc}
	}
	obj := func(required []string, props map[string]interface{}) map[string]interface{} {
		return map[string]interface{}{
			"type":                 "object",
			"required":             required,
			"properties":           props,
			"additionalProperties": false,
		}
	}
	return []nativeTool{
		{
			name:        "read",
			description: "Read a UTF-8 file. Relative paths resolve inside the workspace; absolute paths are used as-is.",
			parameters:  obj([]string{"path"}, map[string]interface{}{"path": str("Relative workspace path or absolute container path.")}),
			run: func(t *nativeTools, _ context.Context, args map[string]interface{}) (string, error) {
				return t.read(argString(args, "path"))
			},
		},
		{
			name:        "write",
			description: "Create or overwrite a UTF-8 file. Relative paths resolve inside the workspace; absolute paths are used as-is.",
			parameters:  obj([]string{"path", "content"}, map[string]interface{}{"path": str("Relative workspace path or absolute container path."), "content": str("Complete file content to write.")}),
			run: func(t *nativeTools, _ context.Context, args map[string]interface{}) (string, error) {
				return t.write(argString(args, "path"), argString(args, "content"))
			},
		},
		{
			name:        "edit",
			description: "Replace text in an existing UTF-8 file. Relative paths resolve inside the workspace; absolute paths are used as-is.",
			parameters:  obj([]string{"path", "old_string", "new_string"}, map[string]interface{}{"path": str("Relative workspace path or absolute container path."), "old_string": str("Exact text to replace."), "new_string": str("Replacement text."), "replace_all": boolean("Replace every occurrence instead of only the first.")}),
			run: func(t *nativeTools, _ context.Context, args map[string]interface{}) (string, error) {
				return t.edit(argString(args, "path"), argString(args, "old_string"), argString(args, "new_string"), argBool(args, "replace_all"))
			},
		},
		{
			name:        "bash",
			description: "Run a shell command in the workspace.",
			parameters:  obj([]string{"command"}, map[string]interface{}{"command": str("Command to run with bash -lc."), "timeout_seconds": integer("Optional timeout, capped by Telos.")}),
			run: func(t *nativeTools, ctx context.Context, args map[string]interface{}) (string, error) {
				return t.bash(ctx, argString(args, "command"), argInt(args, "timeout_seconds"))
			},
		},
		{
			name:        "ls",
			description: "List files in a directory. Relative paths resolve inside the workspace; absolute paths are used as-is.",
			parameters:  obj([]string{}, map[string]interface{}{"path": str("Directory path, defaults to workspace root.")}),
			run: func(t *nativeTools, _ context.Context, args map[string]interface{}) (string, error) {
				return t.ls(argString(args, "path"))
			},
		},
		{
			name:        "grep",
			description: "Search text files with a regular expression. Relative paths resolve inside the workspace; absolute paths are used as-is.",
			parameters:  obj([]string{"pattern"}, map[string]interface{}{"pattern": str("Go regular expression."), "path": str("Directory or file path, defaults to workspace root."), "max_matches": integer("Maximum matches to return.")}),
			run: func(t *nativeTools, _ context.Context, args map[string]interface{}) (string, error) {
				return t.grep(argString(args, "pattern"), argString(args, "path"), argInt(args, "max_matches"))
			},
		},
		{
			name:        "find",
			description: "Find files by glob pattern. Relative paths resolve inside the workspace; absolute paths are used as-is.",
			parameters:  obj([]string{"pattern"}, map[string]interface{}{"pattern": str("Glob pattern matched against relative paths and basenames."), "path": str("Directory path, defaults to workspace root."), "max_matches": integer("Maximum paths to return.")}),
			run: func(t *nativeTools, _ context.Context, args map[string]interface{}) (string, error) {
				return t.find(argString(args, "pattern"), argString(args, "path"), argInt(args, "max_matches"))
			},
		},
	}
}

func nativeToolNames() []string {
	table := nativeToolTable()
	names := make([]string, len(table))
	for i, def := range table {
		names[i] = def.name
	}
	return names
}

func toolSchemasForChat() []map[string]interface{} {
	defs := nativeToolTable()
	out := make([]map[string]interface{}, 0, len(defs))
	for _, def := range defs {
		out = append(out, map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name":        def.name,
				"description": def.description,
				"parameters":  def.parameters,
			},
		})
	}
	return out
}

func toolSchemasForResponses() []map[string]interface{} {
	defs := nativeToolTable()
	out := make([]map[string]interface{}, 0, len(defs))
	for _, def := range defs {
		out = append(out, map[string]interface{}{
			"type":        "function",
			"name":        def.name,
			"description": def.description,
			"parameters":  def.parameters,
		})
	}
	return out
}

func toolSchemasForAnthropic() []map[string]interface{} {
	defs := nativeToolTable()
	out := make([]map[string]interface{}, 0, len(defs))
	for _, def := range defs {
		out = append(out, map[string]interface{}{
			"name":         def.name,
			"description":  def.description,
			"input_schema": def.parameters,
		})
	}
	return out
}

type nativeTools struct {
	platform      *platform.LocalPlatform
	stopRequested func() bool
	byName        map[string]nativeTool
}

func newNativeTools(p *platform.LocalPlatform, stopRequested func() bool) *nativeTools {
	t := &nativeTools{platform: p, stopRequested: stopRequested, byName: map[string]nativeTool{}}
	for _, tool := range nativeToolTable() {
		t.byName[tool.name] = tool
	}
	return t
}

func (t *nativeTools) executeAll(ctx context.Context, calls []nativeToolCall) []nativeToolResult {
	results := make([]nativeToolResult, 0, len(calls))
	for _, call := range calls {
		results = append(results, t.execute(ctx, call))
	}
	return results
}

func (t *nativeTools) execute(ctx context.Context, call nativeToolCall) nativeToolResult {
	if call.ID == "" {
		call.ID = call.Name
	}
	args := map[string]interface{}{}
	if strings.TrimSpace(call.Arguments) != "" {
		if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
			return nativeToolResult{CallID: call.ID, Name: call.Name, Output: "invalid tool arguments: " + err.Error(), IsError: true}
		}
	}
	tool, ok := t.byName[call.Name]
	if !ok {
		return nativeToolResult{CallID: call.ID, Name: call.Name, Output: fmt.Sprintf("unknown tool %q; available tools are %s", call.Name, oxfordList(nativeToolNames())), IsError: true}
	}
	output, err := tool.run(t, ctx, args)
	if err != nil {
		return nativeToolResult{CallID: call.ID, Name: call.Name, Output: err.Error(), IsError: true}
	}
	return nativeToolResult{CallID: call.ID, Name: call.Name, Output: output}
}

func (t *nativeTools) read(p string) (string, error) {
	full, err := t.resolvePath(p)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (t *nativeTools) write(p, content string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("path is required")
	}
	full, err := t.resolvePath(p)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		return "", err
	}
	return "wrote " + t.displayPath(full), nil
}

func (t *nativeTools) edit(p, oldString, newString string, replaceAll bool) (string, error) {
	if oldString == "" {
		return "", fmt.Errorf("old_string is required")
	}
	full, err := t.resolvePath(p)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	text := string(data)
	count := strings.Count(text, oldString)
	if count == 0 {
		return "", fmt.Errorf("old_string not found in %s", p)
	}
	n := 1
	if replaceAll {
		n = -1
	}
	updated := strings.Replace(text, oldString, newString, n)
	if err := os.WriteFile(full, []byte(updated), 0o644); err != nil {
		return "", err
	}
	if !replaceAll {
		count = 1
	}
	return fmt.Sprintf("edited %s (%d replacement%s)", p, count, plural(count)), nil
}

func (t *nativeTools) bash(ctx context.Context, command string, timeout int) (string, error) {
	if strings.TrimSpace(command) == "" {
		return "", fmt.Errorf("command is required")
	}
	if timeout <= 0 || timeout > defaultToolTimeoutSec {
		timeout = defaultToolTimeoutSec
	}
	// Honor both the explicit stop request and the turn deadline carried by ctx.
	interrupt := func() bool {
		if ctx.Err() != nil {
			return true
		}
		return t.stopRequested != nil && t.stopRequested()
	}
	result := t.platform.Run([]string{"bash", "-lc", command}, "", nil, timeout, interrupt, nil)
	var parts []string
	if stdout := strings.Join(result.RawLines, "\n"); stdout != "" {
		parts = append(parts, "[stdout]\n"+stdout)
	}
	if stderr := strings.TrimSpace(result.Stderr); stderr != "" {
		parts = append(parts, "[stderr]\n"+stderr)
	}
	if result.InfraError != "" {
		parts = append(parts, "[error]\n"+result.InfraError)
		return strings.Join(parts, "\n"), errors.New(strings.Join(parts, "\n"))
	}
	if result.ReturnCode != 0 {
		parts = append(parts, fmt.Sprintf("[exit_code]\n%d", result.ReturnCode))
	}
	if len(parts) == 0 {
		return "command completed with no output", nil
	}
	return strings.Join(parts, "\n"), nil
}

func (t *nativeTools) ls(p string) (string, error) {
	full, err := t.resolvePath(defaultString(p, "."))
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		return "", err
	}
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}
		lines = append(lines, name)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n"), nil
}

func (t *nativeTools) grep(pattern, p string, maxMatches int) (string, error) {
	if pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	if maxMatches <= 0 || maxMatches > 500 {
		maxMatches = 100
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", err
	}
	root, err := t.resolvePath(defaultString(p, "."))
	if err != nil {
		return "", err
	}
	var matches []string
	visit := func(file string) {
		if len(matches) >= maxMatches {
			return
		}
		info, err := os.Stat(file)
		if err != nil || info.IsDir() || info.Size() > 2<<20 {
			return
		}
		data, err := os.ReadFile(file)
		if err != nil || bytes.IndexByte(data, 0) >= 0 {
			return
		}
		rel := t.displayPath(file)
		for i, line := range strings.Split(string(data), "\n") {
			if re.MatchString(line) {
				matches = append(matches, fmt.Sprintf("%s:%d:%s", rel, i+1, line))
				if len(matches) >= maxMatches {
					break
				}
			}
		}
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		visit(root)
	} else {
		_ = filepath.WalkDir(root, func(file string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if shouldSkipDir(d.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			visit(file)
			return nil
		})
	}
	if len(matches) == 0 {
		return "no matches", nil
	}
	return strings.Join(matches, "\n"), nil
}

func (t *nativeTools) find(pattern, p string, maxMatches int) (string, error) {
	if pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	if maxMatches <= 0 || maxMatches > 1000 {
		maxMatches = 200
	}
	root, err := t.resolvePath(defaultString(p, "."))
	if err != nil {
		return "", err
	}
	var matches []string
	_ = filepath.WalkDir(root, func(file string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if shouldSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		rel := t.displayPath(file)
		base := path.Base(rel)
		if ok, _ := path.Match(pattern, rel); ok {
			matches = append(matches, rel)
		} else if ok, _ := path.Match(pattern, base); ok {
			matches = append(matches, rel)
		}
		return nil
	})
	sort.Strings(matches)
	if len(matches) > maxMatches {
		matches = matches[:maxMatches]
	}
	if len(matches) == 0 {
		return "no matches", nil
	}
	return strings.Join(matches, "\n"), nil
}

func (t *nativeTools) resolvePath(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("path is required")
	}
	workspace, err := filepath.Abs(t.platform.Workspace)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(p) {
		return filepath.Abs(filepath.Clean(p))
	}
	full, err := filepath.Abs(filepath.Join(workspace, p))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(workspace, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside workspace %q", p, workspace)
	}
	return full, nil
}

func (t *nativeTools) displayPath(full string) string {
	workspace, err := filepath.Abs(t.platform.Workspace)
	if err == nil {
		if rel, relErr := filepath.Rel(workspace, full); relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(full)
}

func shouldSkipDir(name string) bool {
	switch name {
	case ".git", ".telos", "__pycache__", "node_modules", ".venv", "venv":
		return true
	default:
		return false
	}
}

func argString(args map[string]interface{}, key string) string {
	value, _ := args[key].(string)
	return value
}

func argBool(args map[string]interface{}, key string) bool {
	value, _ := args[key].(bool)
	return value
}

func argInt(args map[string]interface{}, key string) int {
	switch value := args[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	default:
		return 0
	}
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func plural(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}

func oxfordList(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " and " + items[1]
	default:
		return strings.Join(items[:len(items)-1], ", ") + ", and " + items[len(items)-1]
	}
}

// -- Session logging ---------------------------------------------------------

type nativeSessionLogger struct {
	path      string
	workspace string
}

func newNativeSessionLogger(path, workspace string) *nativeSessionLogger {
	return &nativeSessionLogger{path: path, workspace: workspace}
}

func (l *nativeSessionLogger) start() error {
	if l.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return err
	}
	return l.append(sessionEvent{
		Type:      "session",
		Version:   1,
		ID:        fmt.Sprintf("native-%d", time.Now().UnixNano()),
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		CWD:       l.workspace,
		Runtime:   "telos-native",
	})
}

func (l *nativeSessionLogger) user(text string) error {
	return l.message(&sessionMessage{
		Role:    "user",
		Content: []sessionContent{{Type: "text", Text: text}},
	})
}

func (l *nativeSessionLogger) assistant(text, provider, model, stopReason string, stats game.TurnStats) error {
	return l.message(&sessionMessage{
		Role:       "assistant",
		Provider:   provider,
		Model:      model,
		StopReason: stopReason,
		Content:    []sessionContent{{Type: "text", Text: text}},
		Usage: &sessionUsage{
			Input:      stats.InputTokens,
			Output:     stats.OutputTokens,
			CacheRead:  stats.CacheReadTokens,
			CacheWrite: stats.CacheCreationTokens,
			Cost:       &sessionCost{Total: stats.CostUSD},
		},
	})
}

func (l *nativeSessionLogger) tool(result nativeToolResult) error {
	return l.message(&sessionMessage{
		Role:       "toolResult",
		ToolCallID: result.CallID,
		ToolName:   result.Name,
		IsError:    result.IsError,
		Content:    []sessionContent{{Type: "text", Text: result.Output}},
	})
}

func (l *nativeSessionLogger) message(msg *sessionMessage) error {
	if l.path == "" {
		return nil
	}
	msg.Timestamp = time.Now().UnixMilli()
	return l.append(sessionEvent{
		Type:      "message",
		ID:        fmt.Sprintf("%s-%d", msg.Role, time.Now().UnixNano()),
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Message:   msg,
	})
}

func (l *nativeSessionLogger) append(event sessionEvent) error {
	if l.path == "" {
		return nil
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(event)
}

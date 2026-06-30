package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/responses"
	"github.com/telos-org/telos/internal/agentsession"
	"github.com/telos-org/telos/internal/game"
)

// -- Gateway Responses client (openai-go Responses API) ---------------------

// responsesClient drives the agent loop against an OpenAI-compatible Responses
// gateway using the official openai-go SDK's Responses API. Transport mechanics
// are hidden behind responseRunner so the loop acts only on final responses.
type responsesClient struct {
	client          openai.Client
	runner          responseRunner
	provider        string
	transport       responseTransport
	model           string
	instructions    string
	reasoning       openai.ReasoningEffort
	maxOutputTokens int
	tools           []responses.ToolUnionParam
	state           *conversationState
	compactor       *compactor
	logger          *nativeSessionLogger
	sequence        int
	activeSequence  int
	lastCostUSD     float64
	lastCostKnown   bool
}

type completionResult struct {
	Response   responses.Response
	AsyncJobID string
}

type responseRunner interface {
	Complete(ctx context.Context, params responses.ResponseNewParams) (completionResult, error)
}

type openAISyncRunner struct {
	client openai.Client
}

type bifrostAsyncRunner struct {
	client    openai.Client
	poll      asyncPollConfig
	logger    *nativeSessionLogger
	sequence  func() int
	provider  string
	model     string
	transport responseTransport
}

func newResponsesClient(httpClient *http.Client, cfg nativeProviderConfig, thinking string, maxOutputTokens int, task, role string, logger *nativeSessionLogger) *responsesClient {
	initial := responses.ResponseInputParam{
		messageInputItem(task, responses.EasyInputMessageRoleUser),
	}
	reasoning := reasoningEffort(thinking)
	if cfg.Capability.SupportsReasoning != nil && !*cfg.Capability.SupportsReasoning {
		reasoning = ""
	}
	stateMode := cfg.Capability.StateMode
	if stateMode == "" {
		stateMode = conversationStateStatelessHistory
	}
	tools := nativeToolsForOpenAI()
	if cfg.Capability.SupportsFunctionCalling != nil && !*cfg.Capability.SupportsFunctionCalling {
		tools = nil
	}
	comp := newCompactor(compactionConfigFromEnv(maxOutputTokens, cfg.Capability.effectiveContextWindow(cfg.Model)))
	t := &responsesClient{
		provider:        cfg.Provider,
		transport:       cfg.Transport,
		model:           cfg.Model,
		instructions:    nativeSystemPrompt(role),
		reasoning:       reasoning,
		maxOutputTokens: maxOutputTokens,
		tools:           tools,
		state:           newConversationState(initial, stateMode),
		compactor:       comp,
		logger:          logger,
	}
	opts := []option.RequestOption{
		option.WithAPIKey(cfg.APIKey),
		option.WithBaseURL(cfg.BaseURL),
		option.WithMaxRetries(0),
		option.WithMiddleware(t.captureResponseHeaders),
	}
	for _, key := range sortedMapKeys(cfg.Headers) {
		opts = append(opts, option.WithHeader(key, cfg.Headers[key]))
	}
	if httpClient != nil {
		opts = append(opts, option.WithHTTPClient(httpClient))
	}
	t.client = openai.NewClient(opts...)
	t.runner = newResponseRunner(cfg, t.client, logger, func() int {
		if t.activeSequence > 0 {
			return t.activeSequence
		}
		return t.sequence
	})
	return t
}

func newResponseRunner(cfg nativeProviderConfig, client openai.Client, logger *nativeSessionLogger, sequence func() int) responseRunner {
	switch cfg.Transport {
	case transportBifrostAsync:
		return &bifrostAsyncRunner{
			client:    client,
			poll:      cfg.AsyncPoll,
			logger:    logger,
			sequence:  sequence,
			provider:  cfg.Provider,
			model:     cfg.Model,
			transport: cfg.Transport,
		}
	default:
		return &openAISyncRunner{client: client}
	}
}

func sortedMapKeys(values map[string]string) []string {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (r *openAISyncRunner) Complete(ctx context.Context, params responses.ResponseNewParams) (completionResult, error) {
	resp, err := r.client.Responses.New(ctx, params)
	if err != nil {
		return completionResult{}, classifyProviderError(err)
	}
	if resp == nil {
		return completionResult{}, retryableExecutorError(errProviderUnavailable, "empty_response")
	}
	return completionResult{Response: *resp}, nil
}

func (r *bifrostAsyncRunner) Complete(ctx context.Context, params responses.ResponseNewParams) (completionResult, error) {
	opts := []option.RequestOption{option.WithHeader("x-bf-async", "true")}
	if r.poll.ResultTTLSeconds > 0 {
		opts = append(opts, option.WithHeader("x-bf-async-job-result-ttl", strconv.Itoa(r.poll.ResultTTLSeconds)))
	}
	initial, err := r.client.Responses.New(ctx, params, opts...)
	if err != nil {
		return completionResult{}, classifyProviderError(err)
	}
	if initial == nil {
		return completionResult{}, retryableExecutorError(errProviderUnavailable, "empty_async_submit")
	}
	if isTerminalResponseStatus(initial.Status) {
		return completionResult{Response: *initial}, nil
	}
	if initial.ID == "" {
		return completionResult{}, retryableExecutorError(errProviderUnavailable, "async_submit_missing_job_id")
	}
	r.logAsyncJob(initial.ID, string(initial.Status))
	return r.pollJob(ctx, params, initial.ID)
}

func (r *bifrostAsyncRunner) pollJob(ctx context.Context, params responses.ResponseNewParams, jobID string) (completionResult, error) {
	backoff := newAsyncPollBackoff(r.poll)
	for {
		if err := sleepContext(ctx, backoff.Next()); err != nil {
			return completionResult{}, classifyProviderError(err)
		}
		resp, err := r.client.Responses.New(ctx, params, option.WithHeader("x-bf-async-id", jobID))
		if err != nil {
			return completionResult{}, classifyProviderError(err)
		}
		if resp == nil {
			return completionResult{}, retryableExecutorError(errProviderUnavailable, "empty_async_poll")
		}
		if isPendingResponseStatus(resp.Status) {
			continue
		}
		return completionResult{Response: *resp, AsyncJobID: jobID}, nil
	}
}

func (r *bifrostAsyncRunner) logAsyncJob(jobID, status string) {
	if r == nil || r.logger == nil {
		return
	}
	seq := 0
	if r.sequence != nil {
		seq = r.sequence()
	}
	_ = r.logger.modelAsyncJob(agentsession.ModelAsyncJobPayload{
		Sequence:  seq,
		JobID:     jobID,
		Provider:  r.provider,
		Model:     r.model,
		Transport: string(r.transport),
		Status:    status,
	})
}

type asyncPollBackoff struct {
	next time.Duration
	max  time.Duration
}

func newAsyncPollBackoff(cfg asyncPollConfig) *asyncPollBackoff {
	initial := cfg.Initial
	if initial <= 0 {
		initial = 500 * time.Millisecond
	}
	maxDelay := cfg.Max
	if maxDelay <= 0 {
		maxDelay = 5 * time.Second
	}
	if maxDelay < initial {
		maxDelay = initial
	}
	return &asyncPollBackoff{next: initial, max: maxDelay}
}

func (b *asyncPollBackoff) Next() time.Duration {
	delay := b.next
	if delay <= 0 {
		delay = 500 * time.Millisecond
	}
	if delay > b.max {
		delay = b.max
	}
	next := delay * 2
	if next > b.max {
		next = b.max
	}
	b.next = next
	if jitterMax := delay / 5; jitterMax > 0 {
		delay += time.Duration(rand.Int63n(int64(jitterMax)))
	}
	return delay
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (t *responsesClient) send(ctx context.Context) (agentTurn, error) {
	compStats, err := t.compactSessionState(ctx)
	if err != nil {
		_ = t.logger.errorEvent(t.sequence+1, err)
		// compStats carries any spend from a compaction attempt that failed after
		// the model call, so the wasted cost is still charged to the turn.
		return agentTurn{stats: compStats}, err
	}
	t.sequence++
	seq := t.sequence
	params := t.params()
	_ = t.logger.modelRequest(t.modelRequestLogData(seq, t.state.previousResponseID()))
	var final responses.Response
	var asyncJobID string
	for attempt := 0; attempt < 3; attempt++ {
		t.lastCostKnown = false
		t.lastCostUSD = 0
		t.activeSequence = seq
		result, runErr := t.runner.Complete(ctx, params)
		t.activeSequence = 0
		if runErr == nil {
			final = result.Response
			asyncJobID = result.AsyncJobID
			runErr = responseTerminalError(final, asyncJobID)
		}
		err = runErr
		if err == nil {
			break
		}
		var execErr *executorError
		if !errors.As(err, &execErr) {
			_ = t.logger.errorEvent(seq, err)
			return agentTurn{}, err
		}
		if !execErr.Retryable && isChainSpecificError(err) {
			break
		}
		if !execErr.Retryable {
			_ = t.logger.errorEvent(seq, err)
			return agentTurn{}, err
		}
		if attempt == 2 {
			break
		}
		delay := retryDelay(attempt)
		_ = t.logger.retry(seq, attempt+1, delay, execErr)
		select {
		case <-ctx.Done():
			ctxErr := classifyProviderError(ctx.Err())
			_ = t.logger.errorEvent(seq, ctxErr)
			return agentTurn{}, ctxErr
		case <-time.After(delay):
		}
	}
	if err != nil && t.state.mode == conversationStateServerChain && t.state.previousResponseID() != "" && isChainSpecificError(err) {
		t.state.fallbackToStatelessHistory()
		_ = t.logger.retry(seq, 1, 0, newExecutorError(errProviderUnavailable, "falling back to stateless_history"))
		_ = t.logger.modelRequest(t.modelRequestLogData(seq, ""))
		t.lastCostKnown = false
		t.lastCostUSD = 0
		t.activeSequence = seq
		result, runErr := t.runner.Complete(ctx, t.params())
		t.activeSequence = 0
		if runErr == nil {
			final = result.Response
			asyncJobID = result.AsyncJobID
			runErr = responseTerminalError(final, asyncJobID)
		}
		err = runErr
		if err != nil {
			_ = t.logger.errorEvent(seq, err)
			return agentTurn{}, err
		}
	}
	if err != nil {
		_ = t.logger.errorEvent(seq, err)
		return agentTurn{}, err
	}
	t.state.recordResponseID(final.ID)
	calls := responseToolCalls(final.Output)
	t.state.recordAssistantMessage(final.OutputText())
	t.state.recordAssistantToolCalls(calls)
	// model_response is a per-call ledger entry, so it records this response's own
	// usage; the compaction call has its own event. The merged total (compaction
	// + main) flows out via the returned agentTurn so it is metered by the loop's
	// budget check, the game cost cap, and the reported total — not just logged.
	mainStats := t.statsFromResponse(final)
	_ = t.logger.modelResponse(seq, final.ID, asyncJobID, responseStopReason(final), mainStats)
	stats := mergeTurnStats(compStats, mainStats)
	if final.Status == responses.ResponseStatusIncomplete {
		reason := final.IncompleteDetails.Reason
		if reason == "" {
			reason = "unknown"
		}
		if len(calls) > 0 && !toolCallsHaveCompleteArguments(calls) {
			reason += ":incomplete_tool_arguments"
		}
		err := newExecutorError(errAgentIncomplete, reason)
		_ = t.logger.errorEvent(seq, err)
		return agentTurn{stats: stats}, err
	}
	return agentTurn{
		text:       final.OutputText(),
		calls:      calls,
		stopReason: responseStopReason(final),
		stats:      stats,
	}, nil
}

// compactSessionState compacts the stateless-history conversation before the
// normal request when it would otherwise exceed the token budget. It returns
// the model usage/cost spent producing the summary so the caller folds it into
// the turn stats: compaction is real spend and must be metered against the run
// budget and reported cost, not merely logged. The returned stats are populated
// even on failure paths that occur *after* the model call, so a wasted summary
// attempt is still charged.
func (t *responsesClient) compactSessionState(ctx context.Context) (game.TurnStats, error) {
	plan, ok, err := t.compactor.plan(t.state)
	if err != nil {
		t.logCompactionFailure(plan, err)
		return game.TurnStats{}, newExecutorError(errProviderContextLimit, "autocompaction_failed:"+err.Error())
	}
	if !ok {
		return game.TurnStats{}, nil
	}
	if t.compactor.cfg.strategy == compactionStrategyTruncate {
		return game.TurnStats{}, t.truncateSessionState(plan)
	}
	summaryBudget := t.compactionSummaryBudget(plan.firstKeptIndex)
	params := responses.ResponseNewParams{
		Model:        openai.ResponsesModel(t.model),
		Instructions: openai.String(t.instructions),
		Input:        responses.ResponseNewParamsInputUnion{OfInputItemList: t.state.compactionRequestInput(plan.firstKeptIndex, summaryBudget)},
		Tools:        t.tools,
	}
	if t.maxOutputTokens > 0 {
		params.MaxOutputTokens = openai.Int(int64(t.maxOutputTokens))
	}
	if t.reasoning != "" {
		params.Reasoning = openai.ReasoningParam{Effort: t.reasoning}
	}
	final, stats, err := t.completeCompaction(ctx, params)
	if err != nil {
		t.logCompactionFailure(plan, err)
		return stats, compactionError(err)
	}
	// A summary call was made and billed. From here every failure path returns
	// `stats` so the spend is charged, and degrades to the truncate strategy
	// (drop old turns, no summary) rather than failing a turn we already paid
	// for. truncate produces a strictly smaller payload, so it also resolves the
	// "summary still over budget" case.
	summary := strings.TrimSpace(final.OutputText())
	if vErr := validateCompactionResponse(final, summary); vErr != nil {
		t.logCompactionFailure(plan, vErr)
		return stats, t.truncateSessionState(plan)
	}
	after := estimateHistoryTokens(t.state.inputWithSummary(summary, plan.firstKeptIndex))
	if budget := t.compactor.cfg.budgetTokens(); budget > 0 && after > budget {
		overErr := newExecutorError(errProviderContextLimit, fmt.Sprintf("autocompaction_failed:summary still over budget after compaction (%d > %d estimated tokens)", after, budget))
		t.logCompactionFailure(plan, overErr)
		return stats, t.truncateSessionState(plan)
	}
	t.state.applyCompaction(summary, plan.firstKeptIndex)
	_ = t.logger.compaction(agentsession.CompactionPayload{
		Reason:          plan.reason,
		FirstKeptIndex:  plan.firstKeptIndex,
		TokensBefore:    plan.tokensBefore,
		TokensAfter:     after,
		SummaryTokens:   estimateItemTokens(compactionSummaryMessage(summary)),
		ItemsSummarized: plan.itemsSummarized,
		ItemsKept:       plan.itemsKept,
		Model:           t.model,
		ResponseID:      final.ID,
		Usage: agentsession.ModelResponseUsage{
			Input:           int(final.Usage.InputTokens),
			Output:          int(final.Usage.OutputTokens),
			CacheRead:       int(final.Usage.InputTokensDetails.CachedTokens),
			CostUSD:         t.lastCostUSD,
			CostUnavailable: !t.lastCostKnown,
		},
		Details: detailsFromCompactionSummary(summary),
	})
	return stats, nil
}

// completeCompaction runs the compaction summary request with the same retry
// budget as a normal request, so a transient provider error at the context
// ceiling — exactly when compaction fires — does not abort the turn. On success
// it returns the response and the spend it incurred; on failure it returns the
// last error (and zero stats, since a failed attempt is not billed).
func (t *responsesClient) completeCompaction(ctx context.Context, params responses.ResponseNewParams) (responses.Response, game.TurnStats, error) {
	var final responses.Response
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		t.lastCostKnown = false
		t.lastCostUSD = 0
		t.activeSequence = t.sequence + 1
		result, runErr := t.runner.Complete(ctx, params)
		t.activeSequence = 0
		if runErr == nil {
			final = result.Response
			runErr = responseTerminalError(final, result.AsyncJobID)
		}
		err = runErr
		if err == nil {
			return final, t.statsFromResponse(final), nil
		}
		var execErr *executorError
		if !errors.As(err, &execErr) || !execErr.Retryable || attempt == 2 {
			break
		}
		delay := retryDelay(attempt)
		_ = t.logger.retry(t.sequence+1, attempt+1, delay, execErr)
		select {
		case <-ctx.Done():
			return responses.Response{}, game.TurnStats{}, classifyProviderError(ctx.Err())
		case <-time.After(delay):
		}
	}
	return responses.Response{}, game.TurnStats{}, err
}

// validateCompactionResponse reports whether the compaction response is a usable
// summary (completed, no tool calls, all required headings). A non-nil error
// means the model misbehaved; the caller degrades to the truncate strategy.
func validateCompactionResponse(final responses.Response, summary string) error {
	if final.Status != responses.ResponseStatusCompleted {
		reason := responseStopReason(final)
		if reason == "" {
			reason = "incomplete"
		}
		return newExecutorError(errAgentIncomplete, "autocompaction_failed:"+reason)
	}
	if calls := responseToolCalls(final.Output); len(calls) > 0 {
		return newExecutorError(errAgentProtocol, "autocompaction_failed:compaction response contained tool calls")
	}
	if err := validateCompactionSummary(summary); err != nil {
		return newExecutorError(errAgentProtocol, "autocompaction_failed:"+err.Error())
	}
	return nil
}

func (t *responsesClient) compactionSummaryBudget(firstKeptIndex int) int {
	if t == nil || t.compactor == nil {
		return 0
	}
	budget := t.compactor.cfg.budgetTokens()
	if budget <= 0 {
		return 0
	}
	base := estimateHistoryTokens(t.state.inputWithSummary("", firstKeptIndex))
	remaining := budget - base
	if remaining < 200 {
		return 200
	}
	return remaining
}

func (t *responsesClient) truncateSessionState(plan compactionPlan) error {
	after := estimateHistoryTokens(t.state.inputWithSummary("", plan.firstKeptIndex))
	if budget := t.compactor.cfg.budgetTokens(); budget > 0 && after > budget {
		return newExecutorError(errProviderContextLimit, fmt.Sprintf("autocompaction_failed:naive cutoff still over budget (%d > %d estimated tokens)", after, budget))
	}
	t.state.applyCompaction("", plan.firstKeptIndex)
	_ = t.logger.compaction(agentsession.CompactionPayload{
		Reason:          "token_budget_naive_cutoff",
		FirstKeptIndex:  plan.firstKeptIndex,
		TokensBefore:    plan.tokensBefore,
		TokensAfter:     after,
		ItemsSummarized: plan.itemsSummarized,
		ItemsKept:       plan.itemsKept,
		Model:           t.model,
	})
	return nil
}

func compactionError(err error) error {
	if err == nil {
		return nil
	}
	var execErr *executorError
	if errors.As(err, &execErr) {
		return &executorError{
			Code:       execErr.Code,
			Message:    "autocompaction_failed:" + execErr.Error(),
			Retryable:  execErr.Retryable,
			StatusCode: execErr.StatusCode,
		}
	}
	return newExecutorError(errProviderUnavailable, "autocompaction_failed:"+err.Error())
}

func (t *responsesClient) logCompactionFailure(plan compactionPlan, err error) {
	if err == nil {
		return
	}
	_ = t.logger.compaction(agentsession.CompactionPayload{
		Reason:          firstNonEmpty(plan.reason, "token_budget"),
		FirstKeptIndex:  plan.firstKeptIndex,
		TokensBefore:    plan.tokensBefore,
		TokensAfter:     plan.tokensBefore,
		ItemsSummarized: plan.itemsSummarized,
		ItemsKept:       plan.itemsKept,
		Model:           t.model,
		Error:           err.Error(),
	})
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func (t *responsesClient) captureResponseHeaders(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
	resp, err := next(req)
	if resp != nil {
		if cost, ok := costFromResponseHeaders(resp.Header); ok {
			t.lastCostUSD = cost
			t.lastCostKnown = true
		}
	}
	return resp, err
}

func (t *responsesClient) params() responses.ResponseNewParams {
	params := responses.ResponseNewParams{
		Model:        openai.ResponsesModel(t.model),
		Instructions: openai.String(t.instructions),
		Input:        responses.ResponseNewParamsInputUnion{OfInputItemList: t.state.requestInput()},
		Tools:        t.tools,
	}
	if previousID := t.state.previousResponseID(); previousID != "" {
		params.PreviousResponseID = openai.String(previousID)
	}
	if t.maxOutputTokens > 0 {
		params.MaxOutputTokens = openai.Int(int64(t.maxOutputTokens))
	}
	if t.reasoning != "" {
		params.Reasoning = openai.ReasoningParam{Effort: t.reasoning}
	}
	return params
}

func (t *responsesClient) recordToolResults(results []nativeToolResult) {
	t.state.recordToolResults(results)
}

func (t *responsesClient) recordCorrection(prompt string) {
	t.state.recordCorrection(prompt)
}

func (t *responsesClient) modelRequestLogData(sequence int, previousID string) modelRequestLogData {
	return modelRequestLogData{
		Sequence:        sequence,
		PreviousID:      previousID,
		StateMode:       t.state.mode,
		Model:           t.model,
		Transport:       string(t.transport),
		MaxOutputTokens: t.maxOutputTokens,
		ToolCount:       len(t.tools),
		ReasoningEffort: string(t.reasoning),
	}
}

func responseTerminalError(final responses.Response, asyncJobID string) error {
	switch {
	case final.Status == responses.ResponseStatusCompleted:
		return nil
	case final.Status == responses.ResponseStatusIncomplete:
		return nil
	case final.Status == responses.ResponseStatusFailed:
		message := strings.TrimSpace(final.Error.Message)
		if message == "" {
			message = "response_failed"
		}
		if asyncJobID != "" {
			message = "async_job_failed:" + message
		}
		return classifyProviderMessage(message, 0)
	case final.Status == responses.ResponseStatusCancelled:
		if asyncJobID != "" {
			return newExecutorError(errStopped, "async_job_cancelled")
		}
		return newExecutorError(errStopped, "response_cancelled")
	case isPendingResponseStatus(final.Status):
		if asyncJobID != "" {
			return retryableExecutorError(errProviderUnavailable, "async_poll_timeout")
		}
		return retryableExecutorError(errProviderUnavailable, "response_not_terminal:"+string(final.Status))
	default:
		if final.Status == "" {
			return retryableExecutorError(errProviderUnavailable, "response_missing_status")
		}
		return retryableExecutorError(errProviderUnavailable, "response_unknown_status:"+string(final.Status))
	}
}

func isPendingResponseStatus(status responses.ResponseStatus) bool {
	switch string(status) {
	case "pending", "processing", "queued", "in_progress":
		return true
	default:
		return false
	}
}

func isTerminalResponseStatus(status responses.ResponseStatus) bool {
	switch string(status) {
	case "completed", "failed", "cancelled", "incomplete":
		return true
	default:
		return false
	}
}

func retryDelay(attempt int) time.Duration {
	base := time.Duration(250*(1<<attempt)) * time.Millisecond
	return base + time.Duration(rand.Intn(125))*time.Millisecond
}

func isChainSpecificError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	for _, needle := range []string{"previous_response_id", "previous response", "response chain", "conversation state"} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

func classifyProviderError(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		classified := classifyProviderMessage(err.Error(), apiErr.StatusCode)
		if execErr, ok := classified.(*executorError); ok {
			execErr.StatusCode = apiErr.StatusCode
		}
		return classified
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if urlErr.Timeout() {
			return retryableExecutorError(errProviderTimeout, err.Error())
		}
		return retryableExecutorError(errProviderUnavailable, err.Error())
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return retryableExecutorError(errProviderTimeout, err.Error())
	}
	if errors.Is(err, context.Canceled) {
		return newExecutorError(errStopped, err.Error())
	}
	return retryableExecutorError(errProviderUnavailable, err.Error())
}

func classifyProviderMessage(message string, statusCode int) error {
	lower := strings.ToLower(message)
	switch {
	case statusCode == http.StatusTooManyRequests:
		return retryableExecutorError(errProviderRateLimited, message)
	case statusCode == http.StatusRequestTimeout || strings.Contains(lower, "timeout"):
		return retryableExecutorError(errProviderTimeout, message)
	case statusCode >= 500:
		return retryableExecutorError(errProviderUnavailable, message)
	case statusCode == http.StatusBadRequest && (strings.Contains(lower, "context") || strings.Contains(lower, "token")):
		return newExecutorError(errProviderContextLimit, message)
	case statusCode >= 400:
		return newExecutorError(errProviderInvalidRequest, message)
	// Text-only fallback classification is best-effort. Prefer typed/status
	// provider errors whenever the transport exposes them.
	case strings.Contains(lower, "rate limit") || strings.Contains(lower, "too many request"):
		return retryableExecutorError(errProviderRateLimited, message)
	case strings.Contains(lower, "context length") || strings.Contains(lower, "maximum context"):
		return newExecutorError(errProviderContextLimit, message)
	default:
		return retryableExecutorError(errProviderUnavailable, message)
	}
}

func responseToolCalls(output []responses.ResponseOutputItemUnion) []nativeToolCall {
	var calls []nativeToolCall
	for _, item := range output {
		if item.Type == "function_call" {
			calls = append(calls, nativeToolCall{
				ID:        item.CallID,
				Name:      item.Name,
				Arguments: item.Arguments,
			})
		}
	}
	return calls
}

func toolCallsHaveCompleteArguments(calls []nativeToolCall) bool {
	for _, call := range calls {
		raw := strings.TrimSpace(call.Arguments)
		if raw == "" || !json.Valid([]byte(raw)) {
			return false
		}
	}
	return true
}

func responseStopReason(r responses.Response) string {
	if reason := r.IncompleteDetails.Reason; reason != "" {
		return reason
	}
	return string(r.Status)
}

func statsFromResponsesUsage(model string, usage responses.ResponseUsage) game.TurnStats {
	return game.TurnStats{
		Model:           model,
		InputTokens:     int(usage.InputTokens),
		OutputTokens:    int(usage.OutputTokens),
		CacheReadTokens: int(usage.InputTokensDetails.CachedTokens),
		CostUnavailable: true,
	}
}

func (t *responsesClient) statsFromResponse(response responses.Response) game.TurnStats {
	stats := statsFromResponsesUsage(t.model, response.Usage)
	if t.lastCostKnown {
		stats.CostUSD = t.lastCostUSD
		stats.CostUnavailable = false
	} else if cost, ok := costFromResponseBody(response.RawJSON()); ok {
		stats.CostUSD = cost
		stats.CostUnavailable = false
	}
	return stats
}

func costFromResponseHeaders(headers http.Header) (float64, bool) {
	for _, name := range []string{
		"x-response-cost",
	} {
		raw := strings.TrimSpace(headers.Get(name))
		if raw == "" {
			continue
		}
		raw = strings.TrimPrefix(raw, "$")
		cost, err := strconv.ParseFloat(raw, 64)
		if err == nil && cost >= 0 {
			return cost, true
		}
	}
	return 0, false
}

func costFromResponseBody(raw string) (float64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	var value any
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		return 0, false
	}
	return costFromJSONValue(value)
}

func costFromJSONValue(value any) (float64, bool) {
	switch v := value.(type) {
	case map[string]any:
		for _, key := range []string{
			"response_cost",
			"cost",
			"total_cost",
			"total_cost_usd",
		} {
			if raw, ok := v[key]; ok {
				if cost, ok := parseCostValue(raw); ok {
					return cost, true
				}
			}
		}
		for _, key := range []string{"metadata", "response_metadata"} {
			if raw, ok := v[key]; ok {
				if cost, ok := costFromJSONValue(raw); ok {
					return cost, true
				}
			}
		}
	case []any:
		for _, item := range v {
			if cost, ok := costFromJSONValue(item); ok {
				return cost, true
			}
		}
	}
	return 0, false
}

func parseCostValue(value any) (float64, bool) {
	switch v := value.(type) {
	case json.Number:
		cost, err := v.Float64()
		return cost, err == nil && cost >= 0
	case float64:
		return v, v >= 0
	case string:
		raw := strings.TrimSpace(strings.TrimPrefix(v, "$"))
		cost, err := strconv.ParseFloat(raw, 64)
		return cost, err == nil && cost >= 0
	default:
		return 0, false
	}
}

func reasoningEffort(thinking string) openai.ReasoningEffort {
	switch openai.ReasoningEffort(thinking) {
	case openai.ReasoningEffortLow:
		return openai.ReasoningEffortLow
	case openai.ReasoningEffortMedium:
		return openai.ReasoningEffortMedium
	case openai.ReasoningEffortHigh:
		return openai.ReasoningEffortHigh
	default:
		return ""
	}
}

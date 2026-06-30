# Bifrost Async Responses Cutover Plan

## Decision Summary

Telos should cut over from the current LiteLLM-named, streaming-first Responses client to a gateway-neutral Responses transport layer with two first-class transports:

- `openai_sync`: standard OpenAI-compatible `POST /responses` using `openai-go` `Responses.New`.
- `bifrost_async`: Bifrost OpenAI-compatible `/openai/responses` using `openai-go` `Responses.New` plus Bifrost async headers, backed by Bifrost `async_jobs`.

Do not implement OpenAI `background: true` for this cutover. Do not keep old LiteLLM env names or provider names. This branch is not in production, so the correction should be complete rather than compatibility-preserving.

The key design point is to keep Telos's agent loop independent from the model transport. `agentLoop` should still receive one final `responses.Response` per model turn, then extract text, tool calls, status, usage, and cost exactly as it does today.

## Goals

- Standardize Telos gateway configuration around new names, not `TELOS_LITELLM_*`.
- Support standard OpenAI-compatible Responses APIs without relying on streaming.
- Support Bifrost async jobs as the canonical long-running gateway path.
- Continue to use the official `openai-go` SDK for request serialization, response decoding, model parameter types, tool schemas, and final response handling.
- Minimize hand-written gateway code to Bifrost polling mechanics and header injection.
- Preserve existing agent behavior: tool loop, protocol corrections, compaction, budget accounting, session logs, replay, and final transcript behavior.
- Make async jobs observable enough that future resumability is straightforward.

## Non-Goals

- No OpenAI `background: true` support in the MVP.
- No native manual `/v1/async/responses` wrapper in the MVP.
- No streaming token UX.
- No compatibility aliases for `TELOS_LITELLM_BASE_URL` or `TELOS_LITELLM_API_KEY`.
- No attempt to support Bifrost async streaming. Bifrost async explicitly does not stream.
- No broad rewrite of the executor, tool protocol, compaction, or game loop.

## Current State

Relevant files:

- `internal/executor/responses.go`
  - Owns the `responsesClient`.
  - Builds `responses.ResponseNewParams`.
  - Calls `t.client.Responses.NewStreaming`.
  - Ignores deltas and only uses terminal `response.completed` or `response.incomplete` events.
- `internal/executor/provider.go`
  - Resolves `TELOS_LITELLM_BASE_URL`, `TELOS_API_BASE_URL`, `TELOS_BASE_URL`.
  - Resolves `TELOS_LITELLM_API_KEY`, `TELOS_API_KEY`.
  - Returns provider name `"litellm"`.
- `internal/gateway/gateway.go`
  - Uses LiteLLM naming in local gateway resolution and probe errors.
- `internal/telosd/kubernetes_worker.go`
  - Propagates LiteLLM-named env vars to workers.
- `internal/agentsession/protocol.go`
  - Defines typed session events.
  - `ModelRequestPayload` and `ProviderConfigPayload` do not currently record transport or async job IDs.

Current streaming is mostly a transport reliability choice, not a user-visible streaming feature. The terminal response is the only response Telos acts on.

## Target Architecture

Introduce a narrow transport interface below `responsesClient`:

```go
type responseRunner interface {
	Complete(ctx context.Context, params responses.ResponseNewParams) (responses.Response, error)
}
```

`responsesClient.send` should call `t.runner.Complete(ctx, params)` instead of `t.streamResponse(ctx, params)`.

The runner implementations are:

```go
type openAISyncRunner struct {
	client openai.Client
}

type bifrostAsyncRunner struct {
	client        openai.Client
	pollInterval pollIntervalConfig
	logger        *nativeSessionLogger
	sequence      func() int
}
```

Optional, but not part of the cutover default:

```go
type openAIStreamingRunner struct {
	client openai.Client
}
```

If kept for local experiments, it should not be selected by default and should not carry old LiteLLM semantics.

## Gateway Configuration

Replace current LiteLLM naming with gateway-neutral names:

```txt
TELOS_GATEWAY_BASE_URL
TELOS_GATEWAY_API_KEY
TELOS_GATEWAY_TRANSPORT
TELOS_GATEWAY_KIND
TELOS_GATEWAY_HEADERS
```

Recommended semantics:

- `TELOS_GATEWAY_BASE_URL`
  - For standard OpenAI or OpenAI-compatible APIs: base URL ending in `/v1`.
  - For Bifrost OpenAI SDK integration: base URL ending in `/openai`.
- `TELOS_GATEWAY_API_KEY`
  - The bearer token passed to `openai-go`.
  - For Bifrost virtual-key-only deployments this can be the virtual key value.
- `TELOS_GATEWAY_TRANSPORT`
  - `openai_sync`
  - `bifrost_async`
- `TELOS_GATEWAY_KIND`
  - `openai`
  - `bifrost`
  - Mostly for diagnostics and defaults. Runtime behavior should be driven by `TELOS_GATEWAY_TRANSPORT`.
- `TELOS_GATEWAY_HEADERS`
  - JSON object of extra headers to attach to every model request.
  - Example: `{"x-bf-vk":"sk-bf-..."}`.

Do not accept old names:

- Remove `TELOS_LITELLM_BASE_URL`.
- Remove `TELOS_LITELLM_API_KEY`.
- Remove `TELOS_API_BASE_URL`.
- Remove `TELOS_BASE_URL`.
- Remove `TELOS_API_KEY`.

This is a deliberate mid-branch correction, not a migration.

## Provider Config Changes

Update `nativeConfig`:

```go
type nativeConfig struct {
	baseURL           string
	apiKey            string
	transport         responseTransport
	kind              gatewayKind
	headers           map[string]string
	capability        map[string]modelCapabilityProfile
	defaultCapability modelCapabilityProfile
}
```

Update `nativeProviderConfig`:

```go
type nativeProviderConfig struct {
	Provider   string
	Model      string
	BaseURL    string
	APIKey     string
	Transport  responseTransport
	Headers    map[string]string
	Capability modelCapabilityProfile
}
```

Suggested string constants:

```go
const (
	gatewayKindOpenAI  gatewayKind = "openai"
	gatewayKindBifrost gatewayKind = "bifrost"

	transportOpenAISync   responseTransport = "openai_sync"
	transportBifrostAsync responseTransport = "bifrost_async"
)
```

Validation:

- Missing base URL: `TELOS_GATEWAY_BASE_URL is required`.
- Missing key: `TELOS_GATEWAY_API_KEY is required`.
- Unknown transport: list accepted values.
- `bifrost_async` base URL should end in `/openai`, or the error should say Bifrost async via the SDK requires the `/openai` endpoint.
- `openai_sync` should not require Bifrost headers.

Defaulting:

- If `TELOS_GATEWAY_TRANSPORT` is unset:
  - `TELOS_GATEWAY_KIND=bifrost` defaults to `bifrost_async`.
  - Otherwise default to `openai_sync`.
- If both are unset, default to `openai_sync`.

## Request Options

Build all common SDK options once in `newResponsesClient`:

```go
opts := []option.RequestOption{
	option.WithAPIKey(cfg.APIKey),
	option.WithBaseURL(cfg.BaseURL),
	option.WithMaxRetries(0),
	option.WithMiddleware(t.captureResponseHeaders),
}
for k, v := range cfg.Headers {
	opts = append(opts, option.WithHeader(k, v))
}
```

Then construct `openai.NewClient(opts...)`.

The Bifrost async runner should add per-call headers on top of these common options:

- Submit: `option.WithHeader("x-bf-async", "true")`
- Poll: `option.WithHeader("x-bf-async-id", jobID)`

Do not put `background: true` in `responses.ResponseNewParams`.

## Runner Behavior

### `openai_sync`

Implementation:

```go
func (r *openAISyncRunner) Complete(ctx context.Context, params responses.ResponseNewParams) (responses.Response, error) {
	resp, err := r.client.Responses.New(ctx, params)
	if err != nil {
		return responses.Response{}, classifyProviderError(err)
	}
	if resp == nil {
		return responses.Response{}, retryableExecutorError(errProviderUnavailable, "empty_response")
	}
	return *resp, nil
}
```

Behavior:

- Uses standard OpenAI-compatible `POST /responses`.
- Blocks until the provider returns the final response.
- Leaves retries to existing Telos retry logic.
- Uses `Response.OutputText()` for final text extraction.

### `bifrost_async`

Implementation shape:

```go
func (r *bifrostAsyncRunner) Complete(ctx context.Context, params responses.ResponseNewParams) (responses.Response, error) {
	initial, err := r.client.Responses.New(
		ctx,
		params,
		option.WithHeader("x-bf-async", "true"),
	)
	if err != nil {
		return responses.Response{}, classifyProviderError(err)
	}
	if initial == nil {
		return responses.Response{}, retryableExecutorError(errProviderUnavailable, "empty_async_submit")
	}

	if isTerminalResponseStatus(initial.Status) {
		return *initial, nil
	}
	if initial.ID == "" {
		return responses.Response{}, retryableExecutorError(errProviderUnavailable, "async_submit_missing_job_id")
	}

	r.logAsyncJob(initial.ID)

	return r.poll(ctx, params, initial.ID)
}
```

Polling:

```go
func (r *bifrostAsyncRunner) poll(ctx context.Context, params responses.ResponseNewParams, jobID string) (responses.Response, error) {
	backoff := newAsyncPollBackoff()
	for {
		if err := sleepContext(ctx, backoff.Next()); err != nil {
			return responses.Response{}, err
		}
		resp, err := r.client.Responses.New(
			ctx,
			params,
			option.WithHeader("x-bf-async-id", jobID),
		)
		if err != nil {
			return responses.Response{}, classifyProviderError(err)
		}
		if resp == nil {
			return responses.Response{}, retryableExecutorError(errProviderUnavailable, "empty_async_poll")
		}
		if isPendingResponseStatus(resp.Status) {
			continue
		}
		return *resp, nil
	}
}
```

Status helpers:

```go
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
```

Terminal statuses to handle explicitly in `send`:

- `completed`: success.
- `incomplete`: existing incomplete handling.
- `failed`: classify `response.Error.Message`.
- `cancelled`: stopped/incomplete style error.

Bifrost's OpenAI SDK async route returns intermediate responses through `Responses.New` with `x-bf-async-id`. The final completed response is an OpenAI-shaped `responses.Response`, so Telos can keep using SDK response types.

## Async Job Observability

Add a new session event kind:

```go
const KindModelAsyncJob = "model_async_job"
```

Payload:

```go
type ModelAsyncJobPayload struct {
	Sequence int    `json:"sequence"`
	JobID    string `json:"job_id"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Transport string `json:"transport"`
	Status   string `json:"status"`
}
```

Emit immediately after Bifrost returns the job ID.

Also extend existing payloads:

```go
type ProviderConfigPayload struct {
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	Transport string `json:"transport,omitempty"`
	BaseURLKind string `json:"base_url_kind,omitempty"`
	...
}

type ModelRequestPayload struct {
	Sequence  int    `json:"sequence"`
	Transport string `json:"transport,omitempty"`
	...
}

type ModelResponsePayload struct {
	Sequence   int    `json:"sequence"`
	ResponseID string `json:"response_id"`
	AsyncJobID string `json:"async_job_id,omitempty"`
	...
}
```

This does not need to make Telos resumable in the MVP. It makes later resumability possible without changing the event vocabulary again.

## Conversation State Rules

`previous_response_id` should only record the final provider response ID, not the Bifrost job ID.

For Bifrost async:

- Submit returns a job UUID.
- Poll eventually returns a final `resp_*` response ID.
- `conversationState.recordResponseID` should use the final response ID only.

If the final response ID is empty but the request completed, degrade to stateless history for the next request rather than recording the async job ID as a response chain ID.

## Error Handling

Use the existing `classifyProviderError` and `classifyProviderMessage` helpers where possible.

Add async-specific messages:

- `empty_async_submit`
- `async_submit_missing_job_id`
- `empty_async_poll`
- `async_poll_timeout`
- `async_job_failed:<message>`
- `async_job_cancelled`

Bifrost failed async jobs can arrive as OpenAI-shaped failed responses or as SDK errors depending on the compatibility path. Handle both:

- If `Responses.New` returns an error, classify it.
- If a response has `status=failed`, inspect `resp.Error.Message`.
- Preserve provider status code when available through `openai.Error`.

Context cancellation:

- If `ctx.Done()` fires while polling, return `ctx.Err()`.
- Existing `classifyProviderError` already maps `context.Canceled` and `DeadlineExceeded`.

Stuck processing:

- Telos should rely on the turn/request timeout context, not Bifrost TTL.
- Bifrost documents that jobs stuck in `processing` are not auto-expired by TTL cleanup.

## Polling Defaults

Add env/config values:

```txt
TELOS_GATEWAY_ASYNC_POLL_INITIAL_MS=500
TELOS_GATEWAY_ASYNC_POLL_MAX_MS=5000
TELOS_GATEWAY_ASYNC_JOB_RESULT_TTL_SEC=3600
```

Behavior:

- Initial poll delay: 500 ms.
- Exponential backoff with jitter.
- Max poll delay: 5 seconds.
- Use `x-bf-async-job-result-ttl` only for Bifrost async submit if configured.
- Do not set result TTL for standard OpenAI sync.

The request/turn context remains the hard deadline.

## Cost And Usage

Keep the current cost flow:

- `captureResponseHeaders` observes response headers from all SDK calls.
- `statsFromResponse` reads usage from the final `responses.Response`.
- `costFromResponseBody(response.RawJSON())` remains the fallback.

Async considerations:

- Intermediate pending/processing polls usually do not contain usage and should not emit `model_response`.
- Only the terminal response should emit `model_response`.
- If Bifrost returns cost metadata only on the final poll, the existing middleware/body fallback should capture it.
- If Bifrost moves cost metadata into a custom JSON path later, extend `costFromJSONValue`, not the runner.

## Compaction

Compaction should use the same configured transport as normal model requests.

Rationale:

- If Bifrost async is chosen for long-running calls, compaction can also be long-running.
- Keeping one transport avoids split-brain cost and auth behavior.

Implementation:

- `streamCompaction` should be renamed to `completeCompaction`.
- It should call the same `responseRunner`.
- Retry behavior remains the same.

If this proves too slow in practice, add a later config override:

```txt
TELOS_COMPACTION_GATEWAY_TRANSPORT=openai_sync
```

Do not add that override in the first cutover unless needed.

## Gateway Probe

Update `internal/gateway/gateway.go`:

- Rename LiteLLM comments and errors to gateway-neutral wording.
- Use `TELOS_GATEWAY_*`.
- Probe should call `POST {baseURL}/responses` for `openai_sync`.
- Probe should call `POST {baseURL}/responses` with `x-bf-async: true`, then poll with `x-bf-async-id` for `bifrost_async`.
- The Bifrost probe should require `/openai` base URL.

Probe payload:

```json
{
  "model": "<configured model>",
  "input": "Reply with exactly OK.",
  "max_output_tokens": 64
}
```

Probe success:

- HTTP success.
- Terminal status `completed`.
- Final text can be extracted via `Response.OutputText()` or from `output[]`.

Do not depend on top-level raw `output_text`; Bifrost returned null there during testing while `output[].content[]` contained final text.

## Kubernetes And Worker Cutover

Update `internal/telosd/kubernetes_worker.go`:

- Replace LiteLLM env secret keys with new gateway keys.
- Store:
  - `TELOS_GATEWAY_BASE_URL`
  - `TELOS_GATEWAY_API_KEY`
  - `TELOS_GATEWAY_TRANSPORT`
  - `TELOS_GATEWAY_KIND`
  - `TELOS_GATEWAY_HEADERS` when present.
- Remove `TELOS_LITELLM_KEY_ALIAS` unless it is still needed by managed billing. If still needed, rename it to `TELOS_GATEWAY_KEY_ALIAS`.

Update tests in `internal/telosd/kubernetes_worker_test.go` accordingly.

## CLI And Config Cutover

Update `cmd/telos/configure.go`:

- User-facing wording should say "gateway" or "Responses API gateway", not LiteLLM.
- Add a transport flag:

```txt
telos configure gateway --base-url URL --api-key KEY --transport openai_sync
telos configure gateway --base-url URL --api-key KEY --transport bifrost_async
```

Optional:

```txt
--kind bifrost
--header x-bf-vk=...
```

Update config structs under `internal/config` if gateway config is persisted there.

## Test Plan

### Unit Tests

`internal/executor/provider_test.go`

- Resolves `TELOS_GATEWAY_BASE_URL`.
- Resolves `TELOS_GATEWAY_API_KEY`.
- Rejects missing base URL.
- Rejects missing API key.
- Rejects unknown transport.
- Defaults `TELOS_GATEWAY_KIND=bifrost` to `bifrost_async`.
- Defaults unset kind/transport to `openai_sync`.
- Parses `TELOS_GATEWAY_HEADERS`.
- Does not read old LiteLLM env names.

`internal/executor/responses_test.go` or `native_test.go`

- `openai_sync` uses `POST /responses` without `stream=true`.
- `bifrost_async` submit sends `x-bf-async: true`.
- `bifrost_async` polls using `x-bf-async-id`.
- Bifrost async handles `pending -> processing -> completed`.
- Bifrost async handles immediate `completed`.
- Bifrost async handles `failed` with provider message.
- Bifrost async respects context timeout while polling.
- Final `previous_response_id` uses final response ID, not async job ID.
- Session log includes `model_async_job`.
- `model_response` includes final response ID and async job ID.

`internal/gateway/gateway_test.go`

- Probe sync success.
- Probe async success.
- Probe async failed job.
- Probe rejects Bifrost async base URL that does not end in `/openai`.

`internal/telosd/kubernetes_worker_test.go`

- Worker secrets use new `TELOS_GATEWAY_*` keys.
- Old LiteLLM keys are not emitted.

### Integration Tests

Against local HTTP test server:

- Standard OpenAI-compatible `/v1/responses` sync.
- Bifrost-compatible `/openai/responses` async header protocol.
- Cost header/body capture on final response.

Against real Bifrost dev gateway:

- `GET /models` with configured key/header.
- Sync `/openai/responses`.
- Async submit and poll through `/openai/responses`.
- Confirm DB `async_jobs.status=completed` and `virtual_key_id` is set.

### Manual Smoke

Run one local Telos turn with:

```txt
TELOS_GATEWAY_BASE_URL=http://<bifrost>:8080/openai
TELOS_GATEWAY_API_KEY=<virtual-key>
TELOS_GATEWAY_KIND=bifrost
TELOS_GATEWAY_TRANSPORT=bifrost_async
```

Expected:

- Session log contains `model_request`.
- Session log contains `model_async_job`.
- Session log contains `model_response`.
- Assistant message is present.
- Tool calls still execute.
- Final result is unchanged from sync behavior.

## Implementation Phases

### Phase 1: Rename Gateway Config

- Replace env resolution in `internal/executor/provider.go`.
- Replace gateway resolution in `internal/gateway/gateway.go`.
- Replace worker env propagation in `internal/telosd/kubernetes_worker.go`.
- Update tests for new names.
- Remove old LiteLLM comments and provider labels.

Exit criteria:

- `rg "LITELLM|LiteLLM|litellm"` only finds historical changelog or intentionally removed references. Prefer zero matches in code.
- Provider config logs say `provider=bifrost` or `provider=openai`, not `litellm`.

### Phase 2: Introduce Response Runners

- Add `response_runner.go` or keep the small types in `responses.go`.
- Move current streaming logic into `openAIStreamingRunner` only if preserving it temporarily helps tests.
- Add `openAISyncRunner`.
- Change `responsesClient` to call `runner.Complete`.
- Rename `streamResponse` call sites to transport-neutral names.

Exit criteria:

- Existing native executor tests pass under `openai_sync`.
- Test server asserts no `stream=true` for sync transport.

### Phase 3: Add Bifrost Async Runner

- Implement submit and poll with SDK `Responses.New`.
- Add Bifrost async status helpers.
- Add polling backoff.
- Add async-specific error handling.
- Add per-call `x-bf-async`, `x-bf-async-id`, and optional `x-bf-async-job-result-ttl`.

Exit criteria:

- Unit test covers pending/processing/completed.
- Unit test covers failed jobs.
- Unit test covers context cancellation.
- Real Bifrost smoke completes and creates `async_jobs` rows.

### Phase 4: Session Log Observability

- Extend `agentsession` event constants and payloads.
- Add `nativeSessionLogger.modelAsyncJob`.
- Add transport fields to provider/request/response payloads.
- Update replay summary if it should count async jobs.

Exit criteria:

- Session JSONL records Bifrost job ID before polling completes.
- Replay tolerates new event kind.
- Existing consumers remain compatible with added optional fields.

### Phase 5: Probe And CLI

- Update `telos configure gateway` flags and validation.
- Implement sync and async probes.
- Update README or CLI help text.

Exit criteria:

- `telos configure gateway --transport openai_sync` probes standard Responses.
- `telos configure gateway --transport bifrost_async` probes Bifrost async headers.

### Phase 6: Clean Up

- Remove stale streaming-first comments.
- Remove old LiteLLM tests or rewrite them under gateway-neutral names.
- Ensure no old env var names remain.
- Run full Go/Bazel test suite used by this repo.

## Future Resumability

The MVP can still block the worker while polling. That is enough to avoid holding a provider generation connection open and to standardize on Bifrost `async_jobs`.

The session event additions make a later resumable design straightforward:

- On submit, persist `model_async_job`.
- If the Telos worker restarts mid-turn, locate the latest non-terminal async job for the sequence.
- Recreate the same request params from session state.
- Poll with `x-bf-async-id`.
- Continue normal `send` handling after the terminal response arrives.

Do not implement restart resumability in this cutover unless there is a product requirement. It adds state-machine complexity that is separate from transport modularity.

## Known Bifrost Details From Testing

Tested against Bifrost `v1.6.1`:

- `/openai/responses` with `x-bf-async: true` submits async jobs successfully.
- Polling `/openai/responses` with `x-bf-async-id` completes successfully.
- Jobs are recorded in Bifrost `async_jobs`.
- The valid virtual key can be passed as `Authorization: Bearer <key>` or `x-bf-vk: <key>`, depending on deployment.
- Raw top-level `output_text` may be null even when final `output[].content[]` contains text. Use SDK `Response.OutputText()`.
- Raw `background: true` is not a viable replacement for Bifrost async headers in this deployment.

## Documentation References

- Bifrost async inference: https://docs.getbifrost.ai/features/async-inference
- Bifrost OpenAI SDK integration: https://docs.getbifrost.ai/integrations/openai-sdk/overview
- OpenAI background mode, intentionally not used in this cutover: https://developers.openai.com/api/docs/guides/background

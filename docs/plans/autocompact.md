# Plan: Task F — Autocompact (token-budgeted history elision)

## Problem statement

In `stateless_history` mode the full conversation is resent on every model
request. Compaction today is `compactHistory` in
`internal/executor/responses.go`:

```go
func compactHistory(history responses.ResponseInputParam) responses.ResponseInputParam {
    const maxItems = 80
    ...
    window = append(window, history[0])
    window = append(window, history[len(history)-maxItems+1:]...)
    return dropOrphanFunctionOutputs(window)
}
```

Problems:

- **Count-based, not token-based.** A window of 80 items can be 2 KB or 2 MB
  depending on tool-output sizes; it has no relationship to the model's context
  window. Long `read_file` / `run` outputs blow the budget well before 80 items;
  short turns get truncated needlessly.
- **Lossy in the middle.** Items between item 0 and the last 80 are dropped
  wholesale. The orphaned `function_call_output` problem this creates is exactly
  why `dropOrphanFunctionOutputs` exists — we delete a `function_call` but keep
  (or drop) its paired output and then have to repair the pairing. Elision that
  preserves pairing removes the need for that repair on the elided region.
- **No per-model awareness.** `modelCapabilityProfile` has no `ContextWindow`,
  so compaction can't scale to the model actually in use.
- **No telemetry.** When compaction happens, nothing is recorded in the session
  log, so we can't see in replay/diagnostics whether a run was context-pressured.

`server_chain` mode threads state with `previous_response_id` and never calls
`compactHistory`; it must stay untouched.

## Proposed design

Introduce a `compactor` owned by `conversationState` and consulted from
`requestInput()`. Phase 1 is lossless-ish elision of old large tool outputs;
Phase 2 (behind a knob) adds a model-generated synopsis.

### 1. Per-model context window

Add a field to `modelCapabilityProfile` (`internal/executor/provider.go`),
populated from `TELOS_MODEL_CAPABILITY_TABLE` / scalar env exactly like the
existing fields:

```go
type modelCapabilityProfile struct {
    StateMode               string `json:"state_mode,omitempty"`
    MaxOutputTokens         int    `json:"max_output_tokens,omitempty"`
    SupportsReasoning       *bool  `json:"supports_reasoning,omitempty"`
    SupportsFunctionCalling *bool  `json:"supports_function_calling,omitempty"`
    StrictProtocol          bool   `json:"strict_protocol,omitempty"`
    ContextWindow           int    `json:"context_window,omitempty"`            // total tokens; 0 = unknown
    CompactionTriggerRatio  float64 `json:"compaction_trigger_ratio,omitempty"` // fraction of window that triggers; 0 = default
    CompactionKeepRecent    int    `json:"compaction_keep_recent,omitempty"`    // recent items kept full; 0 = default
}
```

Add a scalar env override in `modelCapabilityProfileFromEnv`:
`TELOS_MODEL_CONTEXT_WINDOW` (int). Defaults when unset/zero are applied by the
compactor, not stored here, so an unconfigured model behaves like today.

### 2. Compactor type

New file `internal/executor/compactor.go`:

```go
// compactionConfig is resolved once from the capability profile.
type compactionConfig struct {
    contextWindow   int     // total model context in tokens; 0 disables token-based compaction
    triggerRatio    float64 // compact when estReservedFraction of window is exceeded
    keepRecent      int     // most-recent history items always kept verbatim
    maxItemsFloor   int     // hard item-count ceiling, preserves today's safety net
    reserveOutput   int     // output tokens to keep free (maxOutputTokens)
}

// compactor elides old, large function_call_output bodies to fit a token
// budget while preserving call/output pairing. It is stateless across calls
// except for an optional synopsis cache (Phase 2).
type compactor struct {
    cfg      compactionConfig
    synopsis *synopsisState // nil unless Phase 2 enabled
    onEvent  func(compactionEvent) // emits the typed session event; nil-safe
}

type compactionEvent struct {
    Reason          string // "token_budget" | "item_floor"
    ItemsBefore     int
    ItemsAfter      int
    ElidedItems     int
    EstTokensBefore int
    EstTokensAfter  int
    SynopsisRefreshed bool
}
```

Defaults applied when config fields are zero:

- `triggerRatio` → `0.7` (compact when estimate exceeds 70% of window).
- `keepRecent` → `12` items.
- `maxItemsFloor` → `80` (preserves the current ceiling as a backstop even when
  `contextWindow == 0`).

### 3. Token estimation

Cheap and dependency-free; we never need exactness, only a stable trigger.

```go
// estimateItemTokens approximates an item's token cost. chars/4 is the standard
// rough heuristic; structural items (function_call args, message text) are
// measured from their serialized text.
func estimateItemTokens(item responses.ResponseInputParamUnion) int
func estimateHistoryTokens(history responses.ResponseInputParam) int
```

Optionally refine over time by feeding back the real `InputTokens` returned each
loop (the response already carries `usage.InputTokens`). Phase 1 ships with the
`chars/4` estimate; a follow-up can store the last observed input-token count on
`conversationState` and use `max(estimate, lastObserved-deltaSinceObserved)`.
The plan keeps the estimator pluggable so this is a one-function change.

### 4. Elision (Phase 1)

```go
// compact returns a history that fits the token budget. It keeps item 0 (task)
// and the last cfg.keepRecent items verbatim; for older items it replaces large
// function_call_output bodies with a stub, preserving the CallID so the matching
// function_call still has its output (no orphan-output repair needed on the
// elided region). Falls back to dropOrphanFunctionOutputs only for the legacy
// item-floor path.
func (c *compactor) compact(history responses.ResponseInputParam) responses.ResponseInputParam
```

Stub format (mirrors the handoff example), built from the tool result metadata
already serialized in the output body:

```
[elided: read_file path=/src/main.go 1.2KB exit=0]
```

If structured metadata isn't parseable from the output text, fall back to a
size-only stub: `[elided tool output: 1.2KB]`. Elision walks oldest-first,
eliding the largest old `function_call_output` bodies until the estimate drops
below `triggerRatio * contextWindow` (minus `reserveOutput`). `function_call`
items and assistant/user messages are never stubbed in Phase 1 — only
`function_call_output` bodies, which is where the bytes are.

When `contextWindow == 0`, `compact` degrades to today's behavior: keep item 0 +
last `maxItemsFloor`, run `dropOrphanFunctionOutputs`. This guarantees no
behavior change for unconfigured models.

### 5. Phase 2 — running synopsis (behind a knob)

Gated on `TELOS_AUTOCOMPACT_SYNOPSIS=1` (and a non-zero context window). When
elision alone can't reach budget, issue one summarization call condensing the
elided region into a synopsis note, injected as a synthetic user/assistant
message right after item 0:

```go
type synopsisState struct {
    note       string // current synopsis text
    coveredTo  int    // history index up to which the synopsis accounts
}

// refresh issues a summarization request for items (coveredTo, cutoff) and
// updates note/coveredTo. summarize is injected so tests don't hit the network.
func (s *synopsisState) refresh(ctx context.Context, summarize func(context.Context, responses.ResponseInputParam) (string, error), region responses.ResponseInputParam, cutoff int) error
```

The summarization call reuses the existing `responsesClient` plumbing with a
fixed terse instruction ("Summarize the following tool interactions into a few
bullet points an engineer would need to continue; preserve file paths, error
messages, and decisions."). Refreshed on each compaction that still overflows
after elision. Phase 2 is explicitly optional and can ship in a later PR; the
plan keeps `synopsisState` nil and the call site guarded so Phase 1 stands
alone.

### 6. Plumbing into conversationState

`conversationState` gains a `compactor` and `requestInput` consults it:

```go
type conversationState struct {
    mode       string
    input      responses.ResponseInputParam
    history    responses.ResponseInputParam
    previousID string
    compactor  *compactor // nil → legacy compactHistory
}

func (s *conversationState) requestInput() responses.ResponseInputParam {
    if s.mode != conversationStateStatelessHistory {
        return s.input
    }
    if s.compactor != nil {
        return s.compactor.compact(s.history)
    }
    return compactHistory(s.history) // retained as the nil-compactor fallback
}
```

`newConversationState` gains a `*compactor` parameter (or a
`newConversationStateWithCompactor` variant to avoid touching every test).
`newResponsesClient` builds the compactor from `cfg.Capability` and wires
`onEvent` to a logger callback.

### 7. Session event (Task D contract)

Add to `internal/agentsession/protocol.go`:

```go
const KindCompaction = "compaction"

type CompactionPayload struct {
    Reason            string `json:"reason"`
    ItemsBefore       int    `json:"items_before"`
    ItemsAfter        int    `json:"items_after"`
    ElidedItems       int    `json:"elided_items"`
    EstTokensBefore   int    `json:"est_tokens_before"`
    EstTokensAfter    int    `json:"est_tokens_after"`
    SynopsisRefreshed bool   `json:"synopsis_refreshed,omitempty"`
}
```

Add a logger method in `internal/executor/sessionlog.go`:

```go
func (l *nativeSessionLogger) compaction(p agentsession.CompactionPayload) error {
    return l.event(agentsession.KindCompaction, agentsession.MarshalPayload(&p))
}
```

The compactor's `onEvent` is set to `func(e compactionEvent) { _ = logger.compaction(...) }`.
If any session reader switch-dispatches on kind exhaustively, add a
`KindCompaction` arm there too (grep `sessionapi` / diagnostics for `KindError`).

## Risk assessment

- **Pairing correctness.** The core invariant: every retained
  `function_call_output` must have its `function_call` present. Phase 1 only
  rewrites bodies in place and never removes items in the token-budget path, so
  pairing is preserved by construction. The legacy item-floor path keeps
  `dropOrphanFunctionOutputs`. *Mitigation:* an assertion/test that
  `compact` output never contains an orphan output.
- **Under-compaction → provider context error.** If the estimate is too low we
  can still 400. Existing `classifyProviderMessage` already maps this to
  `errProviderContextLimit`. *Mitigation:* conservative `triggerRatio` (0.7) and
  subtracting `reserveOutput`; optionally tighten via observed `InputTokens`.
- **Over-compaction → quality loss.** Stubbing a still-relevant tool output may
  hurt the model. *Mitigation:* `keepRecent` keeps the working set verbatim;
  oldest-and-largest-first elision targets the least relevant bytes; Phase 2
  synopsis backstops.
- **Behavior change for configured models only.** Models with no
  `context_window` in the capability table are byte-for-byte unchanged. The new
  behavior is opt-in via config, lowering blast radius.
- **Phase 2 cost/latency.** A synopsis call adds tokens and a round trip. Gated
  behind an env knob and only fired when elision is insufficient.
- **server_chain untouched.** `requestInput` returns early for non-stateless
  modes; no compactor is consulted.

## Test strategy

New `internal/executor/compactor_test.go`:

- `estimateItemTokens` / `estimateHistoryTokens`: monotonic with content size;
  deterministic.
- `compact` with `contextWindow == 0`: byte-identical to `compactHistory` for
  representative histories (golden parity test guards the legacy path).
- `compact` under budget: history below trigger returned unchanged.
- `compact` over budget: asserts (a) item 0 retained, (b) last `keepRecent`
  retained verbatim, (c) large old outputs replaced with stub strings, (d) no
  orphan `function_call_output` in the result, (e) estimate after < trigger.
- Stub formatting: metadata-rich output yields `[elided: <tool> ... NN.NKB ...]`;
  unparseable output yields the size-only stub.
- Event emission: `onEvent` called once per compaction with correct
  before/after counts.
- Phase 2 (if implemented): `synopsisState.refresh` with an injected
  `summarize` func (no network) inserts the note after item 0 and advances
  `coveredTo`; knob-off path never calls `summarize`.

`internal/agentsession`: round-trip `CompactionPayload` through
`MarshalPayload`/`Unmarshal`.

Provider parsing: `provider_test.go` (or equivalent) — `ContextWindow` and the
new ratio/keep fields parse from `TELOS_MODEL_CAPABILITY_TABLE` and the scalar
`TELOS_MODEL_CONTEXT_WINDOW` env.

Regression: full `go test ./...`, `go vet`, `gofmt`.

## Implementation steps (ordered)

1. Add `ContextWindow` (+ ratio/keepRecent) to `modelCapabilityProfile` and the
   `TELOS_MODEL_CONTEXT_WINDOW` scalar override; table parse test.
2. Add `KindCompaction` + `CompactionPayload` to `agentsession/protocol.go` and
   a round-trip test.
3. Add `nativeSessionLogger.compaction`.
4. Create `compactor.go`: config resolution + defaults, token estimators,
   Phase 1 `compact` (elision + legacy fallback), `compactionEvent`.
5. Wire `compactor` into `conversationState` (`requestInput`,
   constructor param) and build it in `newResponsesClient` with the logger
   callback.
6. Write `compactor_test.go` (parity + elision + event tests).
7. (Optional, can be a follow-up PR) Phase 2 synopsis: `synopsisState`,
   `TELOS_AUTOCOMPACT_SYNOPSIS` knob, summarization call, tests.
8. Add a `KindCompaction` arm to any exhaustive session-event consumer.
9. `go test ./...`, `go vet ./...`, `gofmt -l`.

## Files to create / modify

- **Create:** `internal/executor/compactor.go`,
  `internal/executor/compactor_test.go`
- **Modify:** `internal/executor/provider.go` (profile field + env),
  `internal/executor/conversation_state.go` (compactor field + `requestInput`),
  `internal/executor/responses.go` (build compactor in `newResponsesClient`;
  `compactHistory`/`dropOrphanFunctionOutputs` retained as the nil-compactor
  fallback),
  `internal/agentsession/protocol.go` (kind + payload),
  `internal/executor/sessionlog.go` (`compaction` method),
  session-event consumers if they switch exhaustively on kind.

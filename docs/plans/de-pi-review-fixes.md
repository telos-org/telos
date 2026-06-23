# Implementation Plan: `julian/de-pi` Review Fixes (F1–F9)

## Context

Follow-up to the senior review of branch `julian/de-pi` (native executor +
budgets + typed session log + diagnostics). This plan addresses findings F1–F9
from that review. It deliberately **excludes** the Task A.3 work (structured
findings / `extractFindings` / `findingLineRE`), which stays deferred to its own
PR per `docs/plans/HANDOFF-F-A3-H.md`.

Scope decisions baked in here:

- **F1** — *remove* the argument-redaction machinery entirely (the
  "half-measure"), rather than extend redaction to tool outputs. Session logs
  become an explicit, documented non-boundary for secrets.
- **F4** — already done out of band (`gofmt -w` on the four offending files;
  `gofmt -l ./internal ./cmd` is clean). No CI gate is being added. F4 has no
  remaining section below.
- **F9** — reduced to the part Task H will *not* overwrite. The
  `artifactOriented` heuristic lives in the exact `loop.go` policy surface that
  Task H (table-driven protocol) rewrites, so the behavior change is folded into
  Task H instead of being made here and clobbered later. A.3 findings extraction
  stays out of scope as before.

Repo facts confirmed while writing this plan:
- `platform.Run` has **one** production caller (`internal/executor/tools.go:756`)
  and 10 call sites in `internal/platform/platform_test.go`.
- The session-log golden test logs only `{"path":"main.go"}` as arguments, so
  removing redaction does not perturb it.

Recommended landing order is in the last section. Each fix is independent except
F6 (depends on a small `sessionapi` export) and F3 (best done alongside F2,
since F2 changes what lands in history).

---

## F1 — Remove argument redaction; document logs as a non-boundary

### Problem
`redactToolArguments` only sanitizes tool-call *arguments* by key name, while
tool *outputs* (file contents, `bash` stdout/stderr, `write_file` `content`) are
logged verbatim. This implies a secrets boundary that does not exist and is
inconsistent with the documented unsandboxed posture in `tools.go`.

### Change
Delete the redaction code and log arguments verbatim, then state the threat
model plainly.

- `internal/executor/sessionlog.go`:
  - In `toolCall`, replace `Arguments: redactToolArguments(call.Arguments)` with
    `Arguments: call.Arguments`.
  - Delete `redactToolArguments`, `redactArgumentValue`, `sensitiveArgumentKey`,
    `containsSensitiveArgumentKey`.
  - Add a short doc comment on `toolCall` (and/or the package doc) stating that
    `session.jsonl` records tool arguments **and** outputs verbatim and may
    contain workspace secrets; it is not a redaction boundary. Point at the
    `tools.go` security-model comment.
- `README.md` / any ops doc that describes session artifacts: add one line that
  session logs may contain secrets and should be stored/handled accordingly
  (same trust level as the workspace itself).

### Files
`internal/executor/sessionlog.go`, `internal/executor/native_test.go`,
`README.md`.

### Tests
- Delete `TestNativeSessionLoggerRedactsSensitiveToolArguments`
  (`native_test.go:646`).
- Optionally add `TestNativeSessionLoggerLogsToolArgumentsVerbatim` asserting a
  `tool_call` event's `arguments` equals the raw input (lock in the new
  contract so nobody silently re-adds partial redaction).
- `TestNativeSessionLoggerSchemaGolden` is unaffected (no sensitive keys in the
  fixture); re-run to confirm.

### Risk
Low. This *reduces* protection, but it removes a misleading half-measure and
makes the boundary honest. The real mitigation (don't persist secrets) belongs
to checkpoint hygiene (already present) and to how session dirs are stored.

---

## F2 — Record the assistant's prose turns into history (stateless mode)

### Problem
`conversationState.history` only accumulates `function_call` items, tool
outputs, and correction messages — never the model's own assistant text
(`turn.text`). In the **default** `stateless_history` mode, `requestInput()`
returns this history, so protocol corrections ("your previous response was
missing `<status>`…") refer to a response the model can no longer see, and any
inter-call reasoning prose is dropped on the next iteration.

### Change
Persist the assistant message into history, ordered before its `function_call`
items so the reconstructed Responses input is well-formed.

- `internal/executor/conversation_state.go`: add
  ```go
  func (s *conversationState) recordAssistantMessage(text string) {
      if strings.TrimSpace(text) == "" {
          return
      }
      s.history = append(s.history,
          responses.ResponseInputItemParamOfMessage(text, responses.EasyInputMessageRoleAssistant))
  }
  ```
  (Verify the exact assistant-role constant in openai-go v1.12.0; mirror the
  user-role usage in `recordCorrection`.)
- `internal/executor/responses.go` `send()`: after `t.state.recordResponseID(final.ID)`
  and `calls := responseToolCalls(...)`, call
  `t.state.recordAssistantMessage(final.OutputText())` **before**
  `t.state.recordAssistantToolCalls(calls)`. Use the raw `OutputText()`; the
  reasoning-leak sanitization in `loop.go` is a display concern and need not
  gate what is fed back.

### The judgment call (read before implementing)

There are four sub-decisions inside F2 that are easy to get subtly wrong. They
are the reason this is a "judgment call" and not a one-line append.

**1. Where to record it: inside `send()`, not in `loop.go`.**
It is tempting to record the assistant turn in `loop.go` right after
`l.logger.assistant(...)`, because that is where we already have the sanitized
text in hand. Don't. `recordAssistantToolCalls(calls)` runs *inside* `send()`,
and `recordToolResults(...)` runs *after* `send()` returns in `loop.go`. If the
message is appended in `loop.go`, history ends up ordered
`[function_call …][assistant message][function_call_output …]`, which puts the
message *after* its own function calls — malformed input. The append must happen
inside `send()`, immediately before `recordAssistantToolCalls`, so the order is
`[assistant message][function_call …]` and the outputs slot in correctly on the
next `recordToolResults`. This couples the fix to `send()` rather than the loop,
which is the right seam: `send()` already owns history mutation for a turn
(`recordResponseID`, `recordAssistantToolCalls`).

**2. Which text: raw `final.OutputText()`, not the sanitized text.**
`loop.go` strips reasoning-leak tags (`<think>…`) from `turn.text` for display
and for the returned final answer. The history copy should be the *raw*
`final.OutputText()`. Two reasons: (a) it is what the model actually produced,
so re-feeding it is the faithful transcript; (b) feeding the model its own prior
reasoning back is harmless and avoids a second sanitize pass on a different code
path that could drift from the display one. The mild downside — re-showing
`<think>` tags may nudge the model to emit more reasoning — is acceptable and
strictly better than today's behavior of showing it *nothing*. If we later
decide reasoning should not be re-fed, that is a deliberate, separate change;
do not silently couple it to F2.

**3. The empty-text guard is load-bearing, not defensive.**
A pure tool-call turn (no prose) is common and produces `OutputText() == ""`.
Appending an assistant message with empty content risks a provider 400
("message content must be non-empty") and adds a useless item. The
`strings.TrimSpace(text) == ""` guard in `recordAssistantMessage` must stay; it
is the normal path for tool turns, not an edge case.

**4. Record in both modes; it only *matters* in stateless.**
`history` is maintained even in `server_chain` mode (so the stateless fallback
has a complete transcript). Recording the assistant message unconditionally
keeps that fallback faithful, and `requestInput()` ignores `history` entirely in
`server_chain` mode, so there is no request-size cost there. Do **not**
special-case the recording on `mode`; special-casing would re-introduce the
exact drift (a fallback transcript missing assistant turns) that F2 fixes.

Ordering rationale, restated: a turn's input reconstruction must be
`[assistant message][function_call …][function_call_output …]`. `send()`
already appends the function calls; the message must precede them, and the
outputs are appended later in `loop.go` via `recordToolResults`.

**What success looks like:** in the default stateless mode, the second model
request for a turn contains the first response's assistant text. The
highest-value concrete payoff is the correction loop: when a final has no tool
calls and trips a protocol nudge, the *next* request now contains both the
model's prior (wrong) answer and the correction message, so "your previous
response was missing `<status>`" finally refers to something the model can see.
The test in the next section asserts exactly this.

### Files
`internal/executor/conversation_state.go`, `internal/executor/responses.go`.

### Tests
- Add a stateless-mode test that drives two model turns through a fake provider
  and asserts the first turn's assistant text appears in the second request's
  `Input` (capture `params.Input` in the fake, or assert on
  `conversationState.history`).
- Add a correction-specific test: a final with no tool calls + missing
  `<status>` → assert the next request input contains both the prior assistant
  text and the correction user message.
- Re-run the existing protocol-correction suite; behavior should be unchanged
  except that history now carries the prior assistant turn.

### Risk
Low–medium. Slightly larger request payloads (the assistant text is re-sent),
bounded by `compactHistory`. Watch for providers that reject empty assistant
content — guarded by the non-empty check.

---

## F3 — Unit-test `compactHistory` / `dropOrphanFunctionOutputs`

### Problem
This is correctness-critical (the Responses API rejects a `function_call_output`
without a matching `function_call`) and is the target of the deferred Task F
rewrite, but it has no direct tests.

### Change
No production change. Add table-driven tests (new
`internal/executor/conversation_state_test.go` or extend `native_test.go`).

Cases:
- History `<= maxItems` returned unchanged (identity).
- History `> maxItems`: item[0] (task) preserved; only the last window kept.
- Call/output pair straddling the window boundary (call dropped, output kept)
  → output pruned, no orphan remains.
- A retained `function_call_output` always has its matching `function_call`
  present after compaction.
- All-text history (no calls) compacts without dropping anything spuriously.
- Interaction with F2: assistant messages in the dropped middle are removed
  cleanly (they have no orphan constraint).

Build inputs with the same `responses.ResponseInputItemParamOf…` helpers the
production code uses.

### Files
`internal/executor/conversation_state_test.go` (new) or `native_test.go`.

### Risk
None (tests only). May surface a latent bug; if so, fix under this finding.

---

## F4 — gofmt (done)

Resolved out of band: `gofmt -w` applied to
`internal/agentsession/protocol.go`, `internal/executor/provider.go`,
`internal/executor/sessionlog.go`, `internal/executor/tools.go`;
`gofmt -l ./internal ./cmd` is clean. No CI gate added per decision. Nothing
further to do; keep files `gofmt`-clean on subsequent edits in this batch.

---

## F5 — Don't silently no-op cost budgets when cost is unavailable

### Problem
When a provider returns no cost and no pricing-table entry exists,
`CostUSD` stays 0 and `CostUnavailable` is true, so `--max-cost-usd` enforces
nothing while appearing to. This is a live case (Silares Responses API omits
usage fields).

### Change
Emit a one-time, visible warning and rely on the token/duration/round caps as
the real backstop. Recommended over hard-fail because failing mid-run discards
completed work, and cost availability is only known after the first response.

- `internal/game/pvg.go`: add an unexported bool field on `PVG` (e.g.
  `costCapUnenforceable`). In `runAgentTurn`, after `p.Result.Accumulate(turn.Stats)`:
  ```go
  if p.Config.MaxCostUSD != nil && turn.Stats.CostUnavailable && !p.costCapUnenforceable {
      p.costCapUnenforceable = true
      p.Evidence.Log("cost_cap_unenforceable", roundNum, role, map[string]any{
          "max_cost_usd": *p.Config.MaxCostUSD,
          "reason":       "provider returned no cost and no pricing configured",
      })
  }
  ```
- Surface it in diagnostics: have `buildSessionDiagnostics` count a
  `cost_cap_unenforceable` evidence event (or add a boolean to
  `SessionDiagnosticsResponse`) so operators see it in `telos analyze`.

Optional stricter mode (not default): honor an opt-in
`TELOS_REQUIRE_COST_ENFORCEMENT=1` that turns the first unenforceable turn into
a terminal `runtime_budget_exhausted:cost_unenforceable` failure. Document but
leave off by default.

### Files
`internal/game/pvg.go`, `internal/sessionapi/diagnostics.go` (+ its types),
`internal/game/pvg_test.go`.

### Tests
- `pvg_test.go`: a fake executor returning `CostUnavailable` with a cost cap set
  → assert exactly one `cost_cap_unenforceable` evidence event and that the run
  is not cost-failed.

### Risk
Low. Pure observability addition; no change to the success/failure path unless
the optional strict mode is enabled.

---

## F6 — Dedupe event→diagnostics classification (CLI vs server)

### Problem
`cmd/telos/analyze.go` `analyzeSessionEvents` reimplements the event switch in
`internal/sessionapi/diagnostics.go` `buildSessionDiagnostics`, plus duplicate
map-coercion helpers (`intFromAny`/`stringFromAny`/`boolFromAny`/`floatFromAny`
vs `intFromMap`/`stringFromMap`/…). The shared `ClassifyFailure` mitigates
category drift but not the switch logic.

### Change
Route the CLI's offline (events-only) path through the server's builder, then
reuse the existing `analyzeSessionDiagnostics` mapper.

- `internal/sessionapi/diagnostics.go`: add an exported wrapper:
  ```go
  // DiagnosticsFromEvents builds a diagnostics response from a session and its
  // evidence events only (no session.jsonl scan). Shared by the CLI offline path.
  func DiagnosticsFromEvents(session *Session, events []SessionEvent) *SessionDiagnosticsResponse {
      d, _ := buildSessionDiagnostics(session, events)
      return d
  }
  ```
- `cmd/telos/analyze.go`: replace the body of the events path so it calls
  `sessionapi.DiagnosticsFromEvents(session, events)` then `analyzeSessionDiagnostics(...)`.
  Delete `analyzeSessionEvents`, `addFailure`, `eventSpecName`, and the
  `*FromAny` helpers **iff** they become unused (some — e.g. number formatting
  helpers used by `distribution`/printing — must stay; grep before deleting).

### Files
`internal/sessionapi/diagnostics.go`, `cmd/telos/analyze.go`,
`cmd/telos/main_test.go`.

### Tests
- Add/keep a parity test: feed the same `(session, events)` to
  `DiagnosticsFromEvents` and assert the CLI analysis matches the cloud-path
  analysis for failure categories and totals.
- Keep existing `analyze` CLI tests green.

### Risk
Medium (touches two consumed code paths). Mitigate by landing the export +
re-route first, then deleting dead helpers in a second commit so a mistaken
deletion is easy to bisect.

---

## F7 — Tighten error classification heuristics

### Problem
Free-text substring routing is fragile; `isChainSpecificError` matching bare
`"not found"` is broad enough to trigger a silent `server_chain →
stateless_history` fallback on unrelated 404-ish messages.

### Change
- `internal/executor/responses.go` `isChainSpecificError`: drop the bare
  `"not found"` needle; keep chain-specific markers (`previous_response_id`,
  `previous response`, `response chain`, `conversation state`). The fallback is
  already gated on `mode == server_chain && previousID != ""` in `send()`, so
  narrowing is safe and only affects the wording match.
- `classifyProviderMessage`: keep status-code-first ordering (already correct);
  add a comment that the string fallbacks are best-effort and that typed/status
  classification is preferred. Optionally require the 400 context-limit branch
  to match `context` **and** (`length`|`window`|`token`) to reduce false hits.

### Files
`internal/executor/responses.go`, `internal/executor/native_test.go`.

### Tests
- Add a case: a generic "not found" provider error in `server_chain` mode does
  **not** trigger stateless fallback.
- Keep `TestClassifyProviderMessageDistinguishesContextLimit` and the
  transient/invalid-request tests green.

### Risk
Low. Behavior-narrowing with explicit tests.

---

## F8 — Replace `platform.Run`'s trailing variadic `cwd`

### Problem
`Run(argv, task, env, timeout, interrupt, onLine, cwd ...string)` uses a
0-or-1-element variadic as a pseudo-optional param on an exported method — an
easy-to-misuse smell.

### Change
Make `cwd` an explicit `string` parameter (minimal churn — one production
caller already passes it positionally):
`Run(argv []string, task string, env map[string]string, timeout int, interrupt InterruptRequested, onLine OnStdoutLine, cwd string)`.

- `internal/platform/platform.go`: change signature; drop the `len(cwd) > 0 &&
  cwd[0] != ""` dance to `if strings.TrimSpace(cwd) != ""`.
- `internal/executor/tools.go:756`: already passes `runCWD` — works unchanged.
- `internal/platform/platform_test.go`: add `""` to the 10 call sites.

(If a richer option set is anticipated later, a `RunOptions` struct is the
alternative, but it's more churn than warranted now; explicit param is the
recommended minimal fix.)

### Files
`internal/platform/platform.go`, `internal/platform/platform_test.go`.

### Risk
Low; compiler enforces all call sites are updated.

---

## F9 — `artifactOriented`: hand the decision to Task H (no `loop.go` change now)

> Excludes the A.3 findings work (`extractFindings`/`findingLineRE`), which
> stays deferred.

### Problem
`artifactOriented(task)` (`loop.go:321`) keyword-matches essentially every
coding task, so `requireToolForArtifact` is effectively always-on for provers —
a heuristic dressed as policy.

### Why there is no code change here
The fix is to make the prover tool-use requirement an explicit policy rather
than a keyword guess. But `artifactOriented`, the `requireToolForArtifact`
branch, and the surrounding correction strings all live in
`protocolCorrectionForStrict` / `loopPolicy` — the exact surface Task H
(table-driven protocol) rewrites wholesale, and `artifactOriented` is named in
the Task H key-files list in `HANDOFF-F-A3-H.md`. Making the change in `loop.go`
now means writing it twice and throwing the first version away (plus an eval
pass spent on code that won't survive). The durable action is to record the
decision so Task H encodes it correctly the first time.

### Change (durable; survives Task H)
Capture the intent where Task H will read it, not in the code it replaces.

- `docs/plans/HANDOFF-F-A3-H.md`, Task H section: add a design note that the
  prover artifact rule should be an **explicit table entry** (e.g. a
  `requireToolForProverFinal` rule keyed on `(RoleProver, ProtocolModePVG)`),
  **not** a keyword heuristic on the task text; `artifactOriented` should be
  deleted as part of the H rewrite, not ported into the table. Note the one
  behavior change this implies (prover finals with no tool use get a single
  recoverable nudge regardless of task wording) and that it needs an eval pass —
  so the eval is run once, against the table-driven implementation.
- Optionally drop a one-line `// TODO(task-h):` comment next to
  `artifactOriented` in `loop.go` pointing at the handoff note, so the heuristic
  is visibly flagged for replacement. (This is the only optional code touch and
  is comment-only, so Task H removing the function also removes the TODO.)

### Files
`docs/plans/HANDOFF-F-A3-H.md` (design note); optionally a comment in
`internal/executor/loop.go`.

### Tests
None now. The behavior change and its test land with Task H.

### Risk
None. No behavior change in this batch; the substantive fix is deferred to the
rewrite that owns this surface, avoiding double work and a wasted eval.

---

## Suggested landing order

(F4 already done; F9 is a doc note with no code path.)

1. **F8** (Run signature) — mechanical, isolated.
2. **F1** (remove redaction) — isolated; delete + doc.
3. **F2 + F3 together** (record assistant turns; tests for compaction) — same
   subsystem; F3 also covers F2's new history items.
4. **F7** (error classification) — small, isolated.
5. **F5** (cost-cap warning) — isolated observability.
6. **F6** (diagnostics dedupe) — two-step: export wrapper + re-route, then delete
   dead helpers.
7. **F9** (handoff design note) — land alongside the others; no code dependency.

## Verification checklist (run before each merge)

- `gofmt -l ./internal ./cmd` is empty; `go vet ./...` and `go test ./...` pass.
- F2: a stateless-mode test proves prior assistant text is re-sent.
- F3: compaction tests cover the boundary/orphan cases.
- F5: `cost_cap_unenforceable` evidence event fires once and surfaces in
  `telos analyze`.
- F6: CLI offline analysis and cloud-diagnostics analysis agree on a shared
  fixture.
- F9: `HANDOFF-F-A3-H.md` Task H section carries the explicit-rule design note.

## Out of scope (unchanged here)

- Task F autocompact, Task A.3 structured findings, Task H table-driven protocol
  (see `docs/plans/HANDOFF-F-A3-H.md`). F9's substantive behavior change is
  folded into Task H.
- Extending redaction to tool outputs (explicitly rejected in favor of F1's
  removal + documentation).
- Replacing string-based provider/tool error classification wholesale (F7 only
  narrows the riskiest matcher).
- A CI / gofmt gate (declined for now; F4 fixed manually).

# Implementation Plan: F (Autocompact), A.3 (Structured Findings), H (Table-Driven Protocol)

## Context

This is a Go codebase for Telos, a PVG (Prover-Verifier Game) system that runs
coding-agent turns against a workspace. The native executor lives in
`internal/executor/`, the game loop in `internal/game/`, prompt rendering in
`internal/spec/`, and platform operations in `internal/platform/`.

A prior refactoring pass (commits `fbf6ddc` through `0168807` on branch
`julian/de-pi`) completed these tasks:
- **E**: Shared role/protocol-mode constants in `internal/game/roles.go`
- **B**: Per-model config resolved once in `NewNativeExecutor` via
  `nativeConfig`; `TELOS_MODEL_CAPABILITY_TABLE` for per-model capability
  profiles; pricing parsed once and injected (not per-response)
- **C**: Renamed `openaiTransport` → `responsesClient`,
  `transport_openai.go` → `responses.go`
- **A.1**: Tool handlers return `toolOutput` struct (typed fields + body
  sections); deleted `applyMetadataFromOutput` re-parse
- **D**: Shared typed session-log contract in `internal/agentsession/`;
  event kind constants, typed payload structs, `json.RawMessage` Data field
- **A.2**: `platform.WorkspaceSnapshot` struct; digest reads structured
  fields not string-split; deleted `workspaceSection` parser
- **G**: Extracted `internal/spec/digest.go` from `render.go`

All tests pass, `go vet` clean, `gofmt` clean.

Your job is to write **design plan documents** (markdown files) for the three
remaining tasks below. Do not implement them — just write the plans. Put each
plan in `docs/plans/` as a separate `.md` file.

---

## Task F — Autocompact Feature

### Current state

History compaction lives in `internal/executor/responses.go`:
- `compactHistory(history responses.ResponseInputParam) responses.ResponseInputParam`
  (around line 254): count-based — keeps item 0 (the task) + last 80 items,
  drops the middle wholesale. Calls `dropOrphanFunctionOutputs` to remove
  function_call_outputs whose matching function_call fell outside the window.
- The **default** state mode is `stateless_history` (set in
  `internal/executor/provider.go`'s `modelCapabilityProfileFromEnv`).
  `server_chain` mode uses `previous_response_id` and doesn't need compaction.

`conversationState` (in `internal/executor/conversation_state.go`) manages the
history items. `requestInput()` returns the items sent to the API.

The per-model capability profile (`modelCapabilityProfile` in
`internal/executor/provider.go`) currently has: `StateMode`, `MaxOutputTokens`,
`SupportsReasoning`, `SupportsFunctionCalling`, `StrictProtocol`. It does NOT
yet have a context window field.

### Design requirements

**Phase 1 — Lossless-ish elision (no extra model call):**
- Instead of removing middle items entirely, replace each large old
  `function_call_output` body with a stub like
  `[elided: read_file path=… 1.2KB, exit=0]` while keeping the call/output
  pairing (avoids the orphan-output problem `dropOrphanFunctionOutputs` exists
  to patch).
- Sizing is token-budgeted: estimate history tokens (cheap `chars/4`, or track
  actual `InputTokens` returned each loop). Compact when estimate exceeds a
  fraction of the model's context window.
- Keep recent N turns full.
- Add a `ContextWindow` field to `modelCapabilityProfile` (per-model, from B's
  `TELOS_MODEL_CAPABILITY_TABLE`). Config: context window + compaction
  thresholds in the capability profile.

**Phase 2 — Running synopsis (optional, behind a knob):**
- When elision isn't enough, issue a summarization call condensing the elided
  region into a synopsis note injected after the task.
- Refresh on each compaction.

**Plumbing:**
- A `compactor` consulted by `conversationState.requestInput()`.
- Emit a typed `compaction` event using D's `agentsession` package (new event
  kind + payload struct).
- `server_chain` path untouched.

### Key files to reference
- `internal/executor/responses.go` — `compactHistory`, `dropOrphanFunctionOutputs`
- `internal/executor/conversation_state.go` — `conversationState`, `requestInput`
- `internal/executor/provider.go` — `modelCapabilityProfile`, `nativeConfig`
- `internal/executor/loop.go` — `agentLoop.run` (the tool loop)
- `internal/agentsession/protocol.go` — event kinds and payload structs

---

## Task A.3 — Structured Findings

### Current state

Findings are detected by keyword-scanning prose in
`internal/game/ledger.go`:
- `findingLineRE` (a regex matching `\b(fail|finding|blocked|…)\b`) scans
  verifier turn logs for "finding-like" lines.
- `extractFindings` (in `ledger.go`) uses this regex to populate
  `ledger.OpenFindings`.
- This is NLP-via-regex: it both misses real findings and false-positives on
  casual mentions.

In **review mode**, the verifier emits a `<review>` block containing a CSV
(`criteria,score` rows) and a `<summary>` block. The CSV is structured data
that can be parsed directly.

In **pvg mode**, the verifier emits freeform text with a `<status>` tag — no
structured findings block exists.

### Design requirements

**Step 1 — No prompt change (contained, do now):**
- Parse the verifier `<review>` CSV to extract criteria and scores.
- Treat rows scoring below a threshold as findings.
- Carry the `<summary>` text as the evaluation summary.
- Replaces the keyword scan with structured CSV parse for review mode.
- The `<review>` and `<summary>` blocks are already extracted by regex in
  `internal/spec/digest.go` (`reviewBlockRE`, `summaryBlockRE`) and in
  `internal/executor/loop.go` (`reviewBlockRE`, `summaryBlockRE`).

**Step 2 — Optional, prompt-contract change (bigger, separate PR):**
- Add a `<findings>` block to the verifier output contract in
  `internal/spec/render.go`'s `renderOutputContract` so pvg-mode findings are
  structured too.
- This changes what the model is asked to emit, so it needs evaluation.

### Key files to reference
- `internal/game/ledger.go` — `findingLineRE`, `extractFindings`,
  `updateObjectiveLedger`
- `internal/spec/render.go` — `renderOutputContract` (the output contract
  that tells the model what tags to emit)
- `internal/spec/digest.go` — `reviewBlockRE`, `summaryBlockRE`,
  `lastMatch` (already extract these blocks from text)
- `internal/executor/loop.go` — `loopPolicy`, `protocolCorrectionForStrict`
  (validates review blocks)
- `internal/game/pvg.go` — `turnProtocolMode` (review vs pvg mode)

---

## Task H — Table-Driven Protocol Policy

### Current state

Protocol enforcement is spread across `internal/executor/loop.go`:
- `loopPolicy(role, protocolMode) roleLoopPolicy` — a switch statement that
  returns a struct of boolean flags (`requireStatus`, `requireProgressUpdate`,
  `requireReviewBlocks`, `requireToolForArtifact`).
- `protocolCorrectionForStrict(role, protocolMode, task, text, usedTool, strict, toolsAvailable) (string, string)`
  — a series of if-checks against the policy flags, returning a correction
  prompt and a correction key.
- `formattingCorrectionKeys` — a map of correction keys that get more retries
  (`maxFormattingCorrections = 3`) vs. semantic keys that get only 1 retry.
- `maxProtocolCorrections(key) int` — checks the map.
- `artifactOriented(task) bool` — keyword heuristic for prover tasks.
- Correction prompt strings are inline in the if-checks.

### Design requirements

Replace the switch + scattered correction strings + retry-budget map with a
table:

```go
type protocolRule struct {
    key     string
    check   func(text string, usedTool, strict bool) bool
    message string
    retries int
}

var policies map[policyKey][]protocolRule   // keyed by (role, mode)
```

`protocolCorrectionForStrict` iterates the matching rules. Localizes
everything; adding a role/tag/mode = one table entry. Behavior-preserving.

### Key files to reference
- `internal/executor/loop.go` — `loopPolicy`, `roleLoopPolicy`,
  `protocolCorrectionForStrict`, `protocolCorrectionFor`,
  `formattingCorrectionKeys`, `maxProtocolCorrections`, `artifactOriented`,
  `hasStatusTag`, `verifierConcedes`
- `internal/game/roles.go` — `RoleProver`, `RoleVerifier`,
  `ProtocolModePVG`, `ProtocolModeReview`
- `internal/executor/loop.go` tests in `internal/executor/native_test.go` —
  search for `protocolCorrection` and `loopPolicy` test cases

---

## Output

Write three markdown files:
1. `docs/plans/autocompact.md` — for Task F
2. `docs/plans/structured-findings.md` — for Task A.3
3. `docs/plans/table-driven-protocol.md` — for Task H

Each plan should include:
- Problem statement (what's wrong with the current code)
- Proposed design (concrete, with Go type definitions and function signatures)
- Risk assessment
- Test strategy
- Implementation steps (ordered)
- Files to create/modify

# Executor and Agent Loop Production Readiness Report

Date: 2026-06-19
Branch context: `julian/de-pi`, replacing Pi with the native Telos executor.

## Executive Summary

The branch is moving in the right direction: the native executor is small, the transport is consolidated around the OpenAI Responses API shape, the per-turn session log is typed JSONL, and the PVG loop already separates implementation and evaluation turns. That is a good base for replacing Pi.

The current implementation is not yet production-ready. The main gaps are not about adding providers. Given the product decision that models are accessed through a LiteLLM gateway, Telos should simplify the provider layer aggressively and invest the complexity budget in the pieces that improve reliability and agent performance:

- bounded, structured tools;
- explicit loop state and completion policy;
- robust retry/error classification;
- context packing that gives models the right state without flooding them;
- usable cost and budget enforcement;
- durable observability and replay artifacts;
- benchmark harness cleanup so Pi is no longer part of the operational path.

The highest leverage change is to treat the executor as a small deterministic runtime around a LiteLLM-compatible model endpoint. It should not try to become a full provider abstraction. The core contract should be: "given a Telos turn task, workspace, role, and budget, drive a bounded model-tool loop until a valid turn result is produced or a typed terminal failure is emitted."

## Current Architecture

### Native executor

The native executor lives under `internal/executor`:

- `executor.go` owns `NativeExecutor.ExecuteTurn`, timeout/stop handling, provider resolution, logger setup, tool setup, and result conversion into `game.TurnResult`.
- `loop.go` owns the model-tool loop. It repeatedly calls the transport, logs assistant text, executes tool calls, records tool outputs, and accepts the first tool-less visible final. It only nudges once when the final has no visible text.
- `transport_openai.go` uses `openai-go` Responses streaming, sends tools, threads state with `previous_response_id`, and extracts visible output text, function calls, stop reason, and token usage.
- `tools.go` exposes `read`, `write`, `edit`, `bash`, `ls`, `grep`, and `find`.
- `sessionlog.go` writes a compact agent-session JSONL file with user, assistant, tool result, and note messages.

### PVG loop

The PVG loop lives under `internal/game`:

- default mode alternates prover and verifier until the verifier concedes or the game fails;
- fixed review mode runs a configured number of implementation/evaluation cycles;
- executor failures are treated as recoverable up to `maxRecoverableAgentFailures`;
- transcript entries are capped to `maxTurnBodyChars`, while full per-turn session details are left in `session.jsonl`;
- budget failure only works if the executor reports nonzero `CostUSD`.

### Prompt and session layer

The rendered tasks in `internal/spec/render.go` provide role-specific instructions, spec text, skill roster descriptions, transcript path, workspace snapshot, and output contract. The current prompts emphasize independent review, real artifact inspection, and concise progress updates.

### Benchmark integration boundary

Executable benchmark adapters live outside this repository. Telos core should expose a stable native CLI, session artifacts, diagnostics, replay, and analysis outputs that external benchmark repositories can consume without carrying provider- or Pi-specific runtime assumptions here.

## Production Readiness Goals

Production readiness for Telos should mean:

1. The executor fails explicitly, cheaply, and recoverably when the provider, gateway, tools, or context fail.
2. The model gets enough state to finish real tasks, but not unbounded logs, files, or transcripts.
3. The loop encourages actual task completion without brittle natural-language heuristics.
4. Tool outputs are bounded, structured, and easy for different models to interpret.
5. Budgets, timeouts, stop requests, and checkpoints are enforced at the runtime layer.
6. Session artifacts support debugging, replay, benchmark analysis, and user trust.
7. The code remains simple: one LiteLLM-compatible model path, a compact toolset, typed policies, and minimal role-specific behavior.

## Key Findings

### 1. Provider handling should be simplified around LiteLLM

Current `resolveNativeProvider` supports a registry of direct providers and a generic provider-prefix fallback. That is unnecessary if all models come through a LiteLLM gateway. It also creates a correctness risk: when `TELOS_API_BASE_URL` is set, `stripProviderPrefix(model)` removes the first path segment. LiteLLM deployments often use model names that include provider-like prefixes, so Telos may send the wrong model ID.

Recommended design:

- Use a single gateway config:
  - `TELOS_LITELLM_BASE_URL`, with compatibility aliases `TELOS_API_BASE_URL` and `TELOS_BASE_URL`;
  - `TELOS_LITELLM_API_KEY`, with compatibility alias `TELOS_API_KEY`;
  - model string passed through exactly as configured.
- Remove the built-in provider registry from the executor path.
- Make direct provider URLs a deployment concern of LiteLLM, not Telos.
- Emit a clear startup/config error when gateway URL, key, or model is missing.
- Add tests proving `openai/gpt-5.1`, `anthropic/claude-*`, `sail-research/...`, and arbitrary LiteLLM aliases are passed through unchanged.

This is a simplification and a reliability improvement.

### 2. `previous_response_id` is convenient but a brittle sole memory strategy

The transport currently relies on server-side response chaining through `previous_response_id`. That keeps request payloads small, but it makes every tool loop depend on the gateway preserving and supporting that response chain. If the gateway drops state, returns a bad ID, routes the next request differently, or does not faithfully emulate Responses state, the loop cannot recover locally.

Recommended design:

- Keep a `ConversationState` abstraction with two implementations:
  - `server_chain`: use `previous_response_id`;
  - `stateless_history`: resend compact local messages and tool outputs.
- Default to the mode proven stable for the configured LiteLLM gateway.
- On a chain-specific failure, retry once using stateless history if feasible.
- Log response IDs, request sequence numbers, and state mode in `session.jsonl`.

Do not let the model loop itself become complex. Keep the complexity in a small conversation-state component.

### 3. Tool outputs are unbounded and can overwhelm context

The largest immediate brittleness is unbounded tool output:

- `read` returns entire files.
- `bash` returns all stdout lines and all stderr.
- `ls` returns every entry.
- `grep` can return long matching lines and scans many files.
- `find` walks the full tree before truncating results.
- `platform.Run` stores stdout/stderr in memory without size caps.

This can flood the next model request, make session logs huge, and bury the signal the model needs.

Recommended changes:

- Introduce a shared `ToolOutputLimit` policy:
  - max bytes per tool result;
  - max lines per tool result;
  - separate stdout/stderr caps;
  - explicit truncation markers with original byte/line counts.
- Add range-aware file reading:
  - `read_file(path, start_line, limit_lines)` or equivalent;
  - default to a bounded head or exact range;
  - include file size and truncation metadata.
- Add `stat`/`file_info` so the model can inspect size/type before reading.
- Add `cwd` to `bash`, constrained to the workspace.
- Return structured tool results, even if encoded as text:
  - `ok`, `exit_code`, `duration_ms`, `stdout`, `stderr`, `truncated`, `path`.
- Enforce output caps in `platform.Run`, not just in the executor wrapper.
- Add tests for huge stdout, huge stderr, huge file reads, binary files, and tree walks.

The goal is not to hide information. The goal is to make the model ask for the next useful slice.

### 4. The tool surface is too primitive for robust code editing

The current tools are simple, but the edit path is fragile:

- `edit` requires exact string matching;
- there is no patch tool;
- there is no explicit expected replacement count;
- there is no mode-preserving create/update helper;
- models must use shell ad hoc scripts for common patch operations.

Recommended tool surface:

- Keep `read`, `write`, `bash`, `ls`, `grep`, `find` for compatibility during migration.
- Add or rename toward more explicit tools:
  - `read_file`;
  - `write_file`;
  - `apply_patch`;
  - `replace_text` with `expected_count`;
  - `list_dir`;
  - `search_text`;
  - `find_files`;
  - `file_info`.
- Make `apply_patch` the preferred edit primitive for multi-file or line-oriented changes.
- Require every editing tool to return changed path, replacement count, byte count, and whether the file was created.
- Preserve file modes where possible.

This will help weaker or less tool-trained models avoid accidental broad rewrites.

### 5. Absolute path writes need a policy

`resolvePath` allows absolute paths as-is. That is useful for `/tmp` and benchmark workdirs, but dangerous as the default production policy because the model can mutate outside the workspace, including session/runtime files.

Recommended policy:

- Default writes to workspace-only.
- Allow reads from absolute paths only where needed.
- Allow writes to explicit safe prefixes such as `/tmp/telos-scratch` or configured benchmark workdir.
- Make absolute write enablement part of `NativeExecutor` config.
- Record every outside-workspace access in the session log.

External benchmark adapters can pass explicit safe prefixes or use the benchmark workdir as the workspace root, so this does not need to block benchmark use.

### 6. Completion behavior is currently too weak

The completion gate was removed, leaving only one empty-final retry. This reduces brittle prose heuristics, but the replacement is too permissive. A model can stop without tools, omit required tags, omit progress updates, or fail to address the task, and the executor will accept the final if it has text.

Recommended completion policy:

- Keep completion checks structural, not semantic.
- For verifier turns:
  - require a final `<status>...</status>` in normal PVG mode;
  - if missing, issue one correction prompt that asks for a valid final decision.
- For review mode:
  - require exactly one `<review>` and one `<summary>` block;
  - correction once if malformed.
- For prover turns:
  - require visible final text and final `<progress_update>...</progress_update>`;
  - correction once if malformed.
- Track whether any tool call occurred. If the task appears artifact-oriented and no tool was used, add a gentle correction that asks the model to inspect or modify the workspace before finalizing. Keep this limited and transparent.
- If the model hits max output or returns `response.incomplete`, treat it as `agent_incomplete` and retry or fail recoverably instead of accepting the partial final.

The executor should not try to prove the task is complete. The verifier does that. The executor should only enforce the runtime protocol that keeps PVG productive.

### 7. Provider errors need retries and classification

Transport errors currently bubble up as plain strings such as `provider_error:...`. `ExecuteTurn` turns them into recoverable turn failures. PVG then reruns later turns until the fixed recoverable failure budget is exhausted.

Recommended changes:

- Add typed executor errors:
  - `config`;
  - `provider_rate_limited`;
  - `provider_timeout`;
  - `provider_unavailable`;
  - `provider_invalid_request`;
  - `provider_context_limit`;
  - `tool_timeout`;
  - `tool_infra`;
  - `agent_protocol`;
  - `agent_incomplete`;
  - `stopped`.
- Retry transient provider failures inside a turn with bounded exponential backoff and jitter.
- Do not retry invalid requests blindly.
- Surface typed errors to evidence and manifest status.
- Include retry count, provider status code when available, and request sequence in `session.jsonl`.

This prevents PVG from spending full turns on transient gateway failures while still avoiding hidden infinite retries.

### 8. Incomplete Responses need explicit handling

`streamResponse` treats `response.incomplete` as an assembled final response. The loop logs and processes it just like a completed response. This can accept truncated output when the model hit `max_output_tokens` or another incomplete reason.

Recommended changes:

- If status is incomplete, inspect `IncompleteDetails.Reason`.
- If there are tool calls and arguments are complete, continue carefully.
- If final visible text is incomplete, request continuation or return a typed recoverable `agent_incomplete`.
- If reason is `max_output_tokens`, either raise the cap for that continuation or ask for a concise final.
- Test incomplete final text, incomplete tool call, and incomplete after tool results.

### 9. Cost and budget enforcement are not real yet

`statsFromResponsesUsage` records token counts but does not compute cost. `PVG.overBudget` only checks `TotalCostUSD`, so `--max-cost-usd` may not protect users unless a future transport fills `CostUSD`.

Recommended changes:

- Prefer cost reported by LiteLLM if available through response body, headers, or gateway callbacks.
- Otherwise maintain a local model pricing table only for configured aliases where pricing is known.
- Add a token-only fallback budget:
  - max input tokens;
  - max output tokens;
  - max tool loops;
  - max wall-clock duration;
  - max rounds.
- Record "cost unavailable" explicitly rather than silently reporting `$0`.
- Make budget checks happen before each model request as well as after each turn.

### 10. PVG needs hard round and duration budgets

The default PVG loop can continue indefinitely if the verifier never concedes and cost is zero or unavailable. External benchmark adapters may impose their own attempt limits, but normal default mode needs production guardrails.

Recommended changes:

- Add `MaxRounds` and `MaxDurationSec` to `PVGConfig` and session config.
- Provide sane defaults for local and cloud runs.
- Distinguish "goal failed" from "runtime budget exhausted" in `CompletionReason`.
- Stop launching new turns when the remaining time budget cannot fit a full agent turn.

### 11. Context packing is too coarse

The rendered prompt includes the full task, transcript path, skill roster, and a workspace snapshot. The model is told to read the transcript first, but the runtime does not provide a compact state digest. As runs get longer, models either ignore history or over-read large transcripts.

Recommended changes:

- Generate a small `TurnContext` object before each turn:
  - spec name and role;
  - current objective;
  - last implementation summary;
  - unresolved verifier findings;
  - latest workspace status/diff summary;
  - recent tool/runtime errors;
  - paths to full transcript and evidence.
- Put only the digest in the initial prompt, plus paths to inspect.
- Keep the full transcript append-only, but do not rely on every model to read it correctly.
- Add a transcript summarizer that is deterministic where possible:
  - parse final progress updates;
  - parse verifier findings/status;
  - parse result metadata;
  - avoid using an LLM just to summarize routine state.

This is important for long-running controllers and real engineering tasks.

### 12. Skills are only pointers, not a native runtime capability

`renderSkillsRoster` lists skill names and descriptions. The prompt says the harness can use mounted skill files by name, but the native executor has no dedicated skill lookup/read tool. A model may not know where the skill file lives, and in benchmark workspaces the skill body may not exist in the delivered tree.

Recommended changes:

- Add a `skill` tool or deterministic prompt section:
  - list available skills with names/descriptions;
  - read a selected skill body on demand;
  - read referenced skill files when needed.
- Do not inline every skill by default; that would overwhelm context.
- For required verifier rubrics, ensure the verifier can access the full rubric body before conceding.
- Log which skills were opened and applied.

This directly supports "wide variety of LLMs" because it removes hidden assumptions about mounted files.

### 13. Session logging is useful but incomplete

`session.jsonl` captures assistant text and tool results, but not enough for full forensic replay:

- raw tool call arguments are not logged;
- request sequence and response IDs are not explicit;
- transport retries/errors are not first-class events;
- tool duration and exit code are not structured;
- context mode and truncation are not captured;
- sanitized reasoning plan exists in docs but is not implemented in this branch.

Recommended changes:

- Add event types:
  - `model_request`;
  - `model_response`;
  - `tool_call`;
  - `tool_result`;
  - `retry`;
  - `context_pack`;
  - `budget`;
  - `protocol_correction`;
  - `error`.
- Log raw tool args with safe redaction.
- Log truncation metadata for every tool result.
- Implement visible-text sanitization for inline `<think>`, `<thinking>`, and `<reasoning>` leaks, preserving removed text in a separate session content block when enabled.
- Make the session JSONL schema versioned and tested with golden fixtures.

### 14. `platform.Run` needs production hardening

The command runner is central to executor reliability. Current concerns:

- stdout and stderr are accumulated without caps;
- stderr is read in a goroutine but not explicitly joined before `stderrBuf.String()`;
- process group termination schedules a SIGKILL callback that is not canceled after normal process exit;
- there is no structured timeout/interrupt distinction in tool output beyond `InfraError`;
- no per-command environment/cwd policy is exposed to the model.

Recommended changes:

- Add stdout/stderr byte caps at the runner layer.
- Join stdout and stderr reader goroutines before returning.
- Replace delayed SIGKILL timer with a cancelable kill sequence tied to process lifetime.
- Return structured command metadata:
  - started/ended time;
  - duration;
  - exit code;
  - signal;
  - timeout/interrupted;
  - truncated stdout/stderr.
- Add tests under `-race` for command output handling.

### 15. Workspace snapshots can become huge

`CheckpointWorkspace` walks the whole workspace and archives almost everything. It does not apply the same excludes as `WorkspaceState`. For real repositories, this can include `.git`, dependency folders, build outputs, and secrets.

Recommended changes:

- Add checkpoint exclude patterns:
  - `.git`;
  - `.telos`;
  - dependency caches;
  - build outputs where appropriate;
  - explicit secret/cache patterns.
- Respect `.gitignore` where feasible.
- Add a max archive size guard.
- Emit a manifest of included/excluded files.
- For benchmark tasks, allow override when the full workspace is the required artifact.

### 16. Benchmark adapter ownership should stay outside core

Benchmark adapter code has moved to a separate repository. Core Telos should not carry adapter-specific install scripts, provider credentials, or benchmark runner configuration.

Recommended changes:

- Remove in-repo benchmark adapter code and tests.
- Update README local-run instructions to describe LiteLLM gateway config.
- Keep benchmark-facing CLI/session contracts documented generically.
- Add a migration note for users moving from Pi credentials to LiteLLM.

## Recommended Target Architecture

### Runtime layers

1. `NativeExecutor`
   - owns turn-level deadline, stop requests, budgets, and result conversion.

2. `AgentLoop`
   - owns model-tool sequencing, protocol corrections, max tool loops, and typed loop termination.

3. `ModelGateway`
   - a single LiteLLM/OpenAI-compatible gateway client;
   - no provider registry;
   - exact model pass-through;
   - retry/error classification.

4. `ConversationState`
   - server-side response chain when stable;
   - stateless history fallback;
   - compact local replay state.

5. `ToolRuntime`
   - typed tool schemas;
   - bounded outputs;
   - workspace policy;
   - structured results.

6. `TurnContextBuilder`
   - deterministic digest of transcript, verifier findings, workspace state, and budgets.

7. `AgentSessionLogger`
   - versioned JSONL event stream suitable for debugging, replay, and benchmark analysis.

### Keep the model-facing contract simple

The model should see:

- its role;
- the concrete assignment;
- a compact state digest;
- clear tool descriptions;
- the output protocol;
- paths to inspect for full history.

The model should not see:

- internal provider concepts;
- full unbounded transcripts by default;
- huge command output;
- stale task claims as authoritative state;
- multiple competing ways to do the same tool action.

## Prioritized Roadmap

### P0: Hardening needed before relying on native executor broadly

1. LiteLLM-only provider resolution
   - file targets: `internal/executor/provider.go`, `internal/executor/native_test.go`, README;
   - pass model names through unchanged;
   - remove direct provider registry from the native path.

2. Tool and command output caps
   - file targets: `internal/executor/tools.go`, `internal/platform/platform.go`;
   - cap stdout/stderr/file reads;
   - return truncation metadata;
   - add tests for huge outputs.

3. Incomplete response and protocol handling
   - file targets: `internal/executor/transport_openai.go`, `internal/executor/loop.go`;
   - treat incomplete finals as recoverable or continueable;
   - require structural final tags by role;
   - add one-shot correction prompts.

4. Provider retry and typed errors
   - file targets: `internal/executor/transport_openai.go`, `internal/executor/executor.go`;
   - classify retryable vs nonretryable errors;
   - add bounded backoff;
   - record retry events.

5. Real budget guardrails
   - file targets: `internal/game/types.go`, `internal/game/pvg.go`, `internal/sessionapi/types.go`, CLI flags;
   - add max rounds and max duration;
   - handle unknown cost explicitly.

### P1: Agent performance and scalability

1. Range-aware file tools and patch tool.
2. Structured tool results with durations, exit codes, and truncation.
3. Deterministic turn context digest from transcript/evidence/workspace.
4. Skill lookup/read capability for required rubrics and task skills.
5. Session JSONL event schema expansion and replay fixtures.
6. Workspace checkpoint exclude policy and size guard.

### P2: Product-quality loop behavior

1. Conversation state fallback from `previous_response_id` to stateless local history.
2. Per-role loop policies in data rather than scattered prompt prose.
3. Benchmark analysis dashboards using the richer session/evidence events.
4. Controller-specific safety:
   - one child task at a time unless explicitly justified;
   - child artifact inspection gates;
   - merge/reconcile checklist support.
5. Optional model capability profiles:
   - supports Responses reasoning;
   - supports function calling reliably;
   - prefers lower output token caps;
   - needs stricter protocol corrections.

### P3: Larger revamps

1. Replace the prover/verifier alternation with a more explicit task state machine:
   - `plan`;
   - `implement`;
   - `verify`;
   - `repair`;
   - `finalize`;
   - `blocked`.
2. Introduce a durable "objective ledger" for open findings, resolved findings, and verification evidence.
3. Add replay/eval harnesses that can run saved `session.jsonl` and tool traces against parser/gating logic without calling a model.
4. Add a production admin surface for per-session budgets, retries, stop reasons, and artifact inspection.

## Specific Implementation Notes

### Provider simplification sketch

Desired behavior:

```text
model = "anthropic/claude-sonnet-4.5"
base_url = "https://litellm.example.com/v1"
api_key = "..."

request.model == "anthropic/claude-sonnet-4.5"
```

No stripping, no direct-provider branch, no provider-specific environment variable lookup. If a deployment needs `SAIL_API_KEY` or `OPENAI_API_KEY`, that belongs in the LiteLLM server environment.

### Tool result shape sketch

Human-readable text is fine, but it should be regular:

```text
tool: bash
ok: false
exit_code: 1
duration_ms: 842
truncated: false
stdout:
...
stderr:
...
```

For file reads:

```text
tool: read_file
path: internal/executor/loop.go
size_bytes: 3921
lines_returned: 1-120
truncated: true
content:
...
```

This gives less capable models stable anchors without requiring JSON parsing.

### Completion policy sketch

Keep it structural:

- missing verifier status tag -> correction;
- malformed review blocks -> correction;
- empty final -> correction;
- incomplete response -> continue or typed recoverable error;
- tool loop exceeded -> typed recoverable error.

Avoid semantic gates such as "did the answer sound complete?" The verifier and benchmark should judge semantic completion.

### Turn context digest sketch

Before a turn, build:

```text
## Current State Digest
- Last implementation: ...
- Last evaluation: ...
- Open findings:
  - ...
- Workspace changes:
  - git status summary
  - diff stat
- Full transcript: ...
- Evidence: ...
```

This should be generated from the transcript/evidence structure, not from a model summary unless the deterministic parser cannot extract enough.

## Testing Strategy

### Unit tests

- provider resolution passes all model IDs through unchanged;
- missing LiteLLM config returns clear errors;
- tool output caps and truncation metadata;
- range reads and binary file handling;
- patch application success/failure;
- incomplete Responses statuses;
- protocol correction prompts;
- typed error classification;
- command runner timeout/interruption and stderr join;
- workspace checkpoint excludes.

### Integration tests

- fake LiteLLM server that:
  - streams tool calls;
  - returns incomplete responses;
  - returns rate limits and transient 5xx;
  - breaks `previous_response_id`;
  - emits huge tool outputs.
- local Telos session that completes a small code change with native executor.
- verifier-missing-status correction test.
- long transcript test that confirms prompt size remains bounded.

### Benchmark regression tests

- External benchmark adapter smoke with one-attempt and multi-attempt runs.
- Repeated attempts with the same task and model to track pass rate variance.
- Cost/token/round distribution reports.
- Failure taxonomy report from evidence/session logs:
  - provider;
  - tool;
  - protocol;
  - verifier rejection;
  - task budget;
  - benchmark verifier failure.

## Documentation and Migration Work

Update:

- `README.md`: remove "Local runs require pi" and document LiteLLM gateway variables.
- remove in-repo benchmark adapter docs/code/tests now owned by the adapter repository.
- `internal/spec/skills/task-promotion/SKILL.md`: replace "black-box Pi solver" language with Telos/native or generic solver language.
- `docs/thinking-token-sanitization-plan.md`: either implement it or mark it superseded by the Responses-only path plus defensive sanitizer.

## Suggested First Milestone

A practical first milestone should be:

1. Replace provider resolution with LiteLLM-only exact model pass-through.
2. Add bounded tool outputs and command runner caps.
3. Add incomplete response handling.
4. Add structural protocol correction for verifier status tags and prover progress updates.
5. Update README and remove in-repo adapter docs/code so Pi is not a default dependency.
6. Add focused tests for those changes.

That milestone keeps the executor simple while removing the most likely production failures: wrong model IDs, context floods, accepted partial responses, malformed turn outputs, and stale Pi operational guidance.

## Bottom Line

Telos should not replace Pi by rebuilding a provider-rich coding agent framework. It should replace Pi with a small, durable, LiteLLM-backed turn executor that is excellent at:

- bounded tool use;
- clear context handoff;
- strict runtime protocol;
- robust retries and typed failures;
- high-quality artifacts for replay and inspection.

That path preserves simplicity while giving Telos the reliability and scalability needed for benchmarks, real engineering tasks, controllers, and spec-building workflows.

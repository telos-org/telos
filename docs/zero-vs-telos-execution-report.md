# Zero vs Telos Execution Engine Report

## Snapshot

- Zero repository reviewed: <https://github.com/Gitlawb/zero>
- Zero revision reviewed: `949ee43f71e5cb7fab4695c5cb7b442fe4ecfbf7`
- Zero revision log line: `949ee43 fix: preserve conversation context in exec prompts (#460)`
- Telos revision reviewed: `6fb38cda4c48b4405a29b61ed651ca30eea819bb`
- Telos revision log line: `6fb38cd Big chungus refactor`
- Date reviewed: 2026-07-04

The phrase "Zero revision" in this report means the Git object version used for
review. No commit was made to the Zero repository.

## Scope

This report compares Zero's agent runtime, execution substrate, provider layer,
tooling, sandboxing, session storage, daemon behavior, and CLI execution path
against Telos's native executor, session runner, session API, deployment
orchestration, and supporting platform code.

PVG/game-specific algorithm internals are intentionally treated as mostly
non-comparable. The report does call out where Telos's PVG/session packaging
creates product advantages, but it does not try to map Zero's generic agent loop
onto Telos's PVG algorithm.

Zero is MIT-licensed at the reviewed revision. Borrowing ideas and interfaces
is therefore practical, but Telos should still avoid mechanically copying source
without a normal attribution and compatibility review.

## Executive Thesis

Zero is stronger as a hardened generic agent executor. Its best ideas are not
individual tools; they are the boundaries around the tools:

- A central registry that owns permission checks, sandbox choice, redaction,
  metadata, and changed-file tracking.
- A provider-normalized streaming event contract.
- Strict transcript hygiene around tool calls and tool results.
- Proactive and reactive compaction with stale-output pruning.
- Durable session/event storage with file locks, fsync, and machine-readable
  stream output.
- Explicit completion semantics for headless execution.
- Sandboxed execution profiles and hooks before and after tool use.

Telos is stronger as an orchestration system. It already has root/child
sessions, spec packaging, artifact movement, budgeted native execution,
deployment surfaces, Kubernetes worker provisioning, evidence/rubric hooks, and
a domain model that Zero does not try to solve.

The production path for Telos is therefore not "become Zero." It is to keep the
Telos orchestration model and adopt Zero-style execution boundaries around the
native executor and session API.

## Coverage Inventory

### Zero areas reviewed

Core runtime and execution:

- `cmd/zero/main.go`
- `internal/cli/exec.go`
- `internal/cli/exec_sessions.go`
- `internal/agent/types.go`
- `internal/agent/loop.go`
- `internal/agent/guardrails.go`
- `internal/agent/compaction.go`
- `internal/agent/prune.go`
- `internal/agent/reconnect.go`
- `internal/zeroruntime/types.go`
- `internal/zeroruntime/helpers.go`

Providers:

- `internal/providers/factory.go`
- `internal/providers/providerio/providerio.go`
- `internal/providers/providerio/retry.go`
- `internal/providers/providerio/auth.go`
- `internal/providers/openai/provider.go`
- `internal/providers/openai/tool_state.go`
- `internal/providers/openai/codex_responses.go`
- `internal/providers/anthropic/provider.go`
- `internal/providers/gemini/provider.go`

Tool execution:

- `internal/tools/types.go`
- `internal/tools/registry.go`
- `internal/tools/workspace.go`
- `internal/tools/file_tracker.go`
- `internal/tools/read_file.go`
- `internal/tools/write_file.go`
- `internal/tools/edit_file.go`
- `internal/tools/apply_patch.go`
- `internal/tools/bash.go`
- `internal/tools/exec_command.go`
- `internal/tools/grep.go`
- `internal/tools/glob.go`
- `internal/tools/read_exclusions.go`
- `internal/tools/web_fetch.go`
- `internal/tools/web_search.go`
- `internal/tools/lsp_navigate.go`

Security, extension, and persistence:

- `internal/sandbox/types.go`
- `internal/sandbox/engine.go`
- `internal/sandbox/profile.go`
- `internal/sandbox/manager.go`
- `internal/sandbox/runner.go`
- `internal/sandbox/linux_helper.go`
- `internal/sandbox/landlock_linux.go`
- `cmd/zero-linux-sandbox/main.go`
- `internal/sandbox/windows_command_runner.go`
- `internal/sandbox/windows_setup.go`
- `internal/mcp/config.go`
- `internal/mcp/registry.go`
- `internal/mcp/client.go`
- `internal/mcp/network_client.go`
- `internal/mcp/resources.go`
- `internal/mcp/permissions.go`
- `internal/hooks/dispatch.go`
- `internal/hooks/hooks.go`
- `internal/redaction/redaction.go`
- `internal/secrets/scanner.go`
- `internal/securefile/securefile.go`
- `internal/sessions/store.go`
- `internal/sessions/checkpoint.go`
- `internal/sessions/lineage.go`
- `internal/daemon/pool.go`
- `internal/daemon/server.go`

Adjacent Zero areas were scanned at package level for relevance, including UI,
config, release/update, oauth, model registry, specialist, swarm, and benchmark
support. They are not central to Telos's native executor hardening path.

### Telos areas reviewed

Native executor:

- `internal/executor/executor.go`
- `internal/executor/loop.go`
- `internal/executor/provider.go`
- `internal/executor/responses.go`
- `internal/executor/dispatch.go`
- `internal/executor/tools.go`
- `internal/executor/table.go`
- `internal/executor/fileio.go`
- `internal/executor/search.go`
- `internal/executor/patch.go`
- `internal/executor/bash.go`
- `internal/executor/skills.go`
- `internal/executor/compactor.go`
- `internal/executor/conversation_state.go`
- `internal/executor/sessionlog.go`
- `internal/executor/errors.go`
- `internal/executor/sanitize.go`
- `internal/executor/tokens.go`
- native executor tests

Session, platform, and orchestration:

- `internal/platform/platform.go`
- `internal/sessionrun/localrun.go`
- `internal/sessionrun/workspace.go`
- `internal/agentsession/protocol.go`
- `internal/sessionapi/store.go`
- `internal/sessionapi/manifest.go`
- `internal/sessionapi/auth.go`
- `internal/sessionapi/server.go`
- `cmd/telos/logs.go`
- `cmd/telos/launch.go`
- `internal/spec/package.go`
- `internal/telosd/kubernetes_worker.go`
- `internal/telosd/control_reconciler.go`
- `internal/telosd/route_reconciler.go`
- `internal/telosd/server.go`
- selected PVG/game integration points

## Comparative Scorecard

| Area | Zero | Telos | Recommendation |
| --- | --- | --- | --- |
| Agent loop | Mature guardrails, completion gates, dropped tool recovery, post-edit self-correction | Budgeted native loop, protocol corrections, rubric/skill gates | Keep Telos loop shape; add Zero-style completion and stall guardrails |
| Provider abstraction | Small normalized stream interface with provider adapters | OpenAI Responses-specific client with sync/async and fallback modes | Introduce a normalized event boundary before adding more providers |
| Tool boundary | Central registry owns permissions, sandboxing, redaction, metadata | Native dispatch is simple and assumes trusted process/container boundary | Add registry envelope around native tools |
| Sandboxing | First-class sandbox profiles and backend runners | Explicitly unsandboxed native tools; containment delegated to outer runner | Make containment a checked invariant and support sandbox metadata |
| Path safety | Scoped, symlink-aware workspace resolver | Lexical containment; absolute paths allowed in native schemas | Replace native path handling with scoped resolver |
| File mutation | FileTracker stale-write protection, changed files, patch previews | Direct write/edit/apply_patch, no stale baseline tracking | Add FileTracker and richer mutation metadata |
| Shell | Session-capable shell, PTY, interrupt, bounded output, sandbox denial hints | `bash -lc`, bounded output and timeout caps | Add process cleanup policy, denial metadata, and optional persistent shell |
| Compaction | Tool-token accounting, pruning, recursive fallback, context-limit recovery | Strong BPE estimation and safe function-call boundaries | Merge the two: keep Telos boundary logic, add Zero pruning/fallback |
| Session durability | File locks, fsync, sequence/event log, checkpoint/rewind | Process-local lock, manifest tmp write, rich JSONL log | Add OS locks, fsync, indexed event log, and safer token storage |
| Streaming UX | Stream-json with structured events and usage | SSE API exists, CLI logs poll every 2 seconds | Expose structured streaming as a first-class CLI/API contract |
| Orchestration | Generic session lineage and daemon | Root/child sessions, spec packaging, workers, deployments | Telos is ahead; harden durability and worker lifecycle |
| Kubernetes | Not the focus | Real worker provisioning, network policy, credentials, runtime checks | Reduce blast radius and remove runtime bootstrap fragility |
| Tests | Runtime/tool invariants covered across packages | Good executor/sessionrun tests, less safety-boundary coverage | Add invariant tests before broad feature growth |

## Zero Module Notes

### CLI execution path

`internal/cli/exec.go` turns the agent loop into a headless product surface. It
does several things Telos should copy conceptually:

- It supports a completion contract through `RequireCompletionSignal` rather
  than treating any text response as a successful job.
- It returns a distinct incomplete exit code when work remains.
- It streams structured machine-readable events for assistant text, reasoning,
  tool calls, tool results, usage, and terminal state.
- It wires session persistence into execution rather than making logs a passive
  side effect.

Relevant Zero anchors:

- `internal/cli/exec.go:37` defines incomplete exit behavior.
- `internal/cli/exec.go:451` emits stream events.
- `internal/cli/exec.go:527` enforces completion signaling.
- `internal/cli/exec.go:538` persists structured tool and usage events.

Telos has equivalent raw material in `internal/agentsession/protocol.go` and
`internal/sessionapi/server.go`, but the CLI currently leans on polling in
`cmd/telos/logs.go:71`. The gap is productization, not data availability.

### Agent loop

Zero's `agent.Run` in `internal/agent/loop.go:107` is the center of its runtime.
Each turn partitions tool definitions, proactively compacts context, streams a
provider response with reconnect handling, collects text/reasoning/usage/tool
calls, validates transcript state, runs tools, appends tool results, and applies
post-batch guardrails.

Notable behaviors:

- Proactive compaction before provider calls at `loop.go:156`.
- Stream and reconnect orchestration at `loop.go:178`.
- Stall and retry handling only when no visible response has been committed at
  `loop.go:289`.
- Dropped tool-call and no-output recovery around `loop.go:357`.
- Tool batch execution and self-correction around `loop.go:458`.
- Post-edit self-correction around `loop.go:557`.
- Model escalation after repeated failed batches around `loop.go:571`.
- Final handling after max turns around `loop.go:652`.

Telos has a simpler and more domain-shaped loop in
`internal/executor/loop.go:255`. It already has useful mechanisms:

- Loop budget with `defaultMaxToolLoops=160`.
- Protocol correction prompts.
- Rubric/skill coverage gates.
- Explicit provider budget checks.
- Tool result recording into conversation state.

The main Telos gap is not lack of a loop. It is the missing tail of production
guardrails: no-output reminders, repeated failure detection, dropped-call
recovery, final "no more tools" request, and first-class incomplete terminal
state.

### Runtime and provider abstraction

Zero's provider abstraction is deliberately small. `internal/zeroruntime/types.go`
defines messages, events, tool calls, usage, and provider request/response
types. The provider interface is essentially:

- Accept a `CompletionRequest`.
- Emit normalized stream events.

`internal/zeroruntime/helpers.go` then centralizes event collection, including
ordered tool-call assembly and duplicate/empty ID handling.

Provider-specific code lives behind that boundary:

- `internal/providers/factory.go:38`
- `internal/providers/providerio/providerio.go:249`
- `internal/providers/providerio/retry.go:12`
- `internal/providers/providerio/auth.go:16`
- `internal/providers/openai/provider.go:135`
- `internal/providers/openai/tool_state.go:161`
- `internal/providers/openai/codex_responses.go:212`
- `internal/providers/anthropic/provider.go:361`
- `internal/providers/gemini/provider.go:320`

Telos currently has a more OpenAI-Responses-specific provider layer in
`internal/executor/responses.go` and `internal/executor/provider.go`. It is not
weak code: it supports synchronous calls, asynchronous Bifrost polling,
stateless/server-chain fallback, retries, cost extraction, model context
windows, and compaction-before-main-request. But it would become harder to
extend across providers unless Telos introduces an internal event contract
similar to Zero's.

### Compaction

Zero's compaction system combines several ideas:

- Proactive trigger around 80 percent context use at
  `internal/agent/compaction.go:27`.
- Tool definition token accounting at `compaction.go:120`.
- Tool-result-aware accounting around `compaction.go:334`.
- Context-limit reactive compaction around `compaction.go:395`.
- Recursive fallback when summary attempts are too large at
  `compaction.go:456`.
- State preservation for plans, tool schemas, skills, and instructions in
  `internal/agent/compaction_preserve.go:11`.
- Stale tool-output pruning in `internal/agent/prune.go:10`.
- Context reporting in `internal/agent/context_measurement.go:28`.

Telos's compactor is already good in a different way:

- BPE estimation in `internal/executor/tokens.go`.
- Safe function-call boundary handling in `internal/executor/compactor.go:311`.
- Heading validation and fallback truncation around `compactor.go:473`.
- Compaction statistics in the session log.

The strongest path is to merge the approaches. Telos should keep its boundary
safety and heading validation, then add Zero's stale-output pruning, tool-schema
token accounting, recursive fallback, and broader context-limit error detection.

### Tool registry and tool metadata

Zero's `internal/tools/types.go:14` is a strong model for tool metadata. Tool
results carry:

- Side-effect taxonomy.
- Permission requirements.
- Sandbox overrides.
- Changed file paths.
- Redaction flags.
- Short display previews.

`internal/tools/registry.go:117` is the important boundary. `RunWithOptions`
performs pre-permission rejection, sandbox evaluation, user prompt/deny
handling, option-aware dispatch, result redaction, and tool metadata recording.
Core tools are registered around `registry.go:234`.

Telos has tool definitions in `internal/executor/table.go` and execution in
`internal/executor/dispatch.go`. That code is direct and readable, but the
policy is split across the caller, the individual tool functions, and the
external environment. The comment in `internal/executor/tools.go:16` explicitly
states that native tools are unsandboxed.

The most important Telos change is a registry envelope around native tools. The
tool function should not be responsible for all of:

- Path scope.
- Permission policy.
- Sandbox policy.
- Redaction policy.
- Changed-file tracking.
- Log shaping.
- User-facing metadata.

### Workspace and file mutation safety

Zero has a stronger file safety story:

- `internal/tools/workspace.go:34` defines scoped, symlink-aware path resolution.
- `workspace.go:140` rechecks write targets.
- `internal/tools/file_tracker.go:12` records SHA-256 baselines and refuses
  stale writes.
- `internal/tools/write_file.go:61`, `edit_file.go:65`, and
  `apply_patch.go:144` return changed files and redacted previews.
- Zero's patch parser is hunk-aware.

Telos file IO is bounded and cleanly implemented, but less defensive:

- `internal/executor/table.go:43` schemas allow absolute file paths.
- `internal/executor/fileio.go:275` uses lexical containment checks.
- `write` and `edit` in `fileio.go:176` and `fileio.go:205` perform direct
  mutation without stale-baseline detection.
- `internal/executor/patch.go:194` validates paths before `git apply`, but
  conflict/stale tests should be stronger.

Telos should move all native filesystem operations through a single scoped path
resolver and add baseline tracking before adding more file tools.

### Shell and process execution

Zero has two shell-related layers:

- `internal/tools/bash.go` for command execution policy.
- `internal/tools/exec_command.go` for session-capable command execution with
  PTY support, polling, interruption, bounded buffers, and sandbox metadata.

Process cleanup is handled in platform-specific helpers such as
`bash_proc_unix.go:19` and `exec_pty_linux.go:82`. The shell tool blocks likely
interactive commands, applies sandbox overrides, redacts stdout/stderr where
needed, and marks likely sandbox denials.

Telos's native bash tool in `internal/executor/bash.go:10` is intentionally
small:

- `bash -lc`
- timeout cap
- interrupt/stop polling
- bounded output
- structured output metadata

That is sufficient for short native execution. For long-running production
sessions, Telos should add Zero-style process lifecycle policy:

- process group cleanup with wait/escalate behavior
- explicit timeout categories
- denial metadata
- optional persistent sessions
- tests for interrupted commands and orphan process cleanup

### Sandbox and permissions

Zero's sandbox model is broad:

- `internal/sandbox/types.go:256` defines policy.
- `internal/sandbox/engine.go:277` evaluates sandbox behavior.
- `internal/sandbox/profile.go:104` builds profiles.
- `internal/sandbox/manager.go:124` selects backends.
- `internal/sandbox/runner.go:70` wraps command execution.
- Linux support uses bwrap/Landlock helpers.
- macOS uses seatbelt-style profiles.
- Windows has restricted-token support and explicit degraded metadata.

Zero's default profile should not be mistaken for full secret isolation; it
allows broad filesystem read unless configured otherwise. The useful lesson is
not the exact policy, but that sandbox policy is explicit, recorded, and attached
to tool execution.

Telos delegates containment to the outer session runner/container. That can be a
valid architecture, but the executor should verify that invariant. A native
executor started without a trusted platform/workspace boundary should fail
closed, and every tool event should record whether execution was contained by a
known runner, sandbox, or neither.

### MCP, web, LSP, hooks, redaction

Zero has optional tool families Telos can adopt selectively:

- MCP registry and clients normalize tools, connect concurrently, enforce
  permissions, and handle network/SSE details:
  `internal/mcp/config.go:36`, `registry.go:63`, `client.go:123`,
  `network_client.go:327`, `resources.go:120`, `permissions.go:98`.
- Web fetch/search validate public HTTP(S), block local/private/metadata
  targets, pin DNS expectations, and constrain redirects:
  `internal/tools/web_fetch.go:143`, `web_search.go:119`.
- LSP navigation exists in `internal/tools/lsp_navigate.go:81`.
- Hooks can veto or observe sessions/tools and fail closed on timeouts:
  `internal/hooks/dispatch.go:112`.
- Audit logs use private file modes in `internal/hooks/hooks.go:500`.
- Redaction and secret scanning live in `internal/redaction/redaction.go:33`,
  `internal/secrets/scanner.go:27`, and `internal/securefile/securefile.go:1`.

Telos does not need all of this immediately. The redaction, hook boundary, and
web/MCP gating patterns are more important than the individual feature set.

### Sessions, daemon, checkpointing

Zero sessions use durable storage patterns that Telos should copy:

- OS-level locking in `internal/sessions/store.go:515`.
- fsync and durable event writes around `store.go:740`.
- sequence handling around `store.go:788`.
- checkpoint/rewind support in `internal/sessions/checkpoint.go:59`.
- lineage support in `internal/sessions/lineage.go:1`.
- daemon worker retry and classification in `internal/daemon/pool.go:88` and
  `pool.go:176`.
- graceful worker drain and kill in `pool.go:321`.
- daemon HTTP connection tracking and stalled-peer cleanup in
  `internal/daemon/server.go:168` and `server.go:231`.

Telos has richer domain concepts, but weaker local durability:

- `internal/sessionapi/store.go:52` uses a process-local mutex.
- `internal/sessionapi/manifest.go:224` writes via fixed `session.json.tmp`
  without fsync or directory sync.
- `internal/sessionapi/auth.go:296` scans manifests to find scoped tokens.
- `internal/sessionapi/manifest.go:12` stores scoped tokens directly.

Telos should preserve its manifest model but harden writes and indexing.

## Telos Module Notes

### `internal/executor/executor.go`

Strengths:

- Clear native executor lifecycle and config shape.
- Explicit stop path and cleanup model.
- Validates that native execution needs platform and workspace dependencies.
- `ExecuteTurn` cleanly maps run output into turn results.

Important anchors:

- `executor.go:17` native executor struct.
- `executor.go:52` fail-fast checks for platform/workspace.
- `executor.go:101` turn execution entry.
- `executor.go:201` turn context construction.
- `executor.go:241` recoverable/terminal helper behavior.

Recommendations:

- Make the containment assumption explicit in config and logs. The executor
  should know whether it is running inside a session workspace, a container, a
  sandbox profile, or an uncontained local process.
- Return a richer terminal state. Distinguish completed, incomplete, exhausted,
  interrupted, provider-failed, and tool-policy-failed.

### `internal/executor/loop.go`

Strengths:

- The loop is bounded.
- Protocol correction tables are explicit and testable.
- Rubric/skill checks are integrated into the turn.
- Budget checks happen before continuing expensive work.
- Native system prompt centralizes tool rules.

Important anchors:

- `loop.go:13` constants and defaults.
- `loop.go:236` loop construction.
- `loop.go:255` main run loop.
- `loop.go:322` budget checks.
- `loop.go:338` correction/rubric gate.
- `loop.go:404` native system prompt.

Recommendations:

- Add Zero-style no-output, dropped-call, and repeated-failure guardrails.
- Add final "complete without more tools" prompting before returning incomplete.
- Add explicit incomplete result classification for automation.
- Consider model escalation or strategy changes after repeated tool failures,
  but only when Telos has a clear provider/model policy for it.

### `internal/executor/responses.go`

Strengths:

- Strong OpenAI Responses integration.
- Supports stateless and server-chain modes.
- Supports async Bifrost polling.
- Retries are centralized.
- Cost extraction is pragmatic.
- Compaction-before-main-request is correctly placed.

Important anchors:

- `responses.go:24` client and runner abstraction.
- `responses.go:166` sync runner.
- `responses.go:179` async runner.
- `responses.go:289` send path.
- `responses.go:316` stateless/server-chain fallback.
- `responses.go:358` retries.
- `responses.go:401` compaction response validation.
- `responses.go:446` request params.
- `responses.go:486` terminal error classification.
- `responses.go:555` provider error classification.
- `responses.go:607` tool call extraction.
- `responses.go:648` cost extraction.

Recommendations:

- Put a provider-neutral event interface above this file before adding Anthropic,
  Gemini, local models, or alternate gateways.
- Add an explicit async poll cap independent of request context/client timeout.
- Broaden context-limit detection using Zero's provider error taxonomy as a
  reference.
- Persist replay artifacts for failed provider responses and schema drift.

### `internal/executor/provider.go`

Strengths:

- Config and environment parsing are straightforward.
- Model context windows are visible and easy to audit.
- Capabilities are represented directly.

Recommendations:

- Externalize model capabilities over time. Built-in tables are fine for a first
  pass, but become stale.
- Preserve the current simple config path, but expose a normalized provider
  capability object to the loop.

### `internal/executor/dispatch.go`

Strengths:

- Native tool dispatch is small and readable.
- Tool metadata has typed structure.
- Error classification is already centralized.
- Skill coverage checks are integrated.

Important anchors:

- `dispatch.go:17` native tool list.
- `dispatch.go:129` typed metadata.
- `dispatch.go:146` error classification.
- `dispatch.go:181` argument validation.

Gaps:

- Argument validation checks provided argument types, but should also enforce
  required fields, enums, additionalProperties, and tolerant JSON recovery
  policy.
- Policy is not centralized enough. Dispatch should call through a registry
  envelope that handles scope, permissions, redaction, changed files, and
  logging before/after the tool function.

### `internal/executor/tools.go` and `table.go`

Strengths:

- Tool output shape is typed.
- Native tool schemas are easy to inspect.
- Security comment is honest about the native executor trust boundary.

Important anchors:

- `tools.go:16` explicit unsandboxed native tool warning.
- `tools.go:45` typed output.
- `table.go:41` tool definitions.
- `table.go:158` OpenAI schema generation.

Recommendations:

- Keep the warning, but convert it into an enforceable precondition.
- Attach permission/sandbox/redaction metadata to tool schemas.
- Avoid absolute path schemas unless the policy truly permits host-level access.

### `internal/executor/fileio.go`

Strengths:

- Bounded reads.
- UTF-8/binary checks.
- Structured metadata.
- Logs outside-workspace attempts.

Important anchors:

- `fileio.go:14` read bounds.
- `fileio.go:92` UTF-8/binary handling.
- `fileio.go:176` write path.
- `fileio.go:205` replace/edit path.
- `fileio.go:275` path resolution.

Recommendations:

- Replace lexical containment with symlink-aware scoped resolution.
- Add stale-write protection through file baselines.
- Add changed-file metadata to all mutating outputs.
- Test symlink escapes, absolute paths, deleted/recreated files, and concurrent
  edits.

### `internal/executor/patch.go`

Strengths:

- Uses `git apply`, which avoids inventing patch semantics.
- Validates changed paths before applying.
- Extracts metadata.

Important anchors:

- `patch.go:14` patch flow.
- `patch.go:194` path validation.

Recommendations:

- Add malformed patch tests.
- Add stale-baseline/conflict tracking.
- Return a concise diff preview with redaction support.
- Record changed files in a common mutation metadata field.

### `internal/executor/bash.go`

Strengths:

- Simple execution model.
- Timeout cap.
- Stop/interrupt polling.
- Bounded output with metadata.

Important anchors:

- `bash.go:10` command execution entry.
- `bash.go:73` timeout/cap behavior.

Recommendations:

- Add process group cleanup tests.
- Add interactive command blocking or policy metadata.
- Add sandbox/containment metadata to every shell call.
- Add denial classification for likely sandbox/container policy failures.

### `internal/executor/search.go`

Strengths:

- Search/list/find are bounded.
- Directory skips are explicit.
- Output shaping is suitable for agent use.

Recommendations:

- Route path resolution through the same scoped resolver as file IO.
- Honor read-deny exclusions when Telos adds sandbox or permissions.
- Keep output limits visible in metadata.

### `internal/executor/skills.go`

Strengths:

- Skill list/read/read_ref is integrated.
- Required rubric coverage can be checked.

Important anchors:

- `skills.go:10` skill tool handling.
- `skills.go:146` missing required skill logic.

Recommendations:

- Keep this Telos-specific capability. Zero does not have a directly superior
  replacement.
- Add tests where skill-read failures interact with loop completion semantics.

### `internal/executor/compactor.go` and `conversation_state.go`

Strengths:

- Safe conversation boundaries.
- Summary heading validation.
- Fallback truncation.
- Conversation state supports stateless and server-chain provider modes.

Important anchors:

- `compactor.go:18` config/headings.
- `compactor.go:139` planning/summarization.
- `compactor.go:223` stats.
- `compactor.go:311` function-call boundary logic.
- `compactor.go:473` validation/extraction.
- `conversation_state.go:9` state model.
- `conversation_state.go:146` recording helpers.

Recommendations:

- Add stale tool-output pruning.
- Add tool-schema token accounting.
- Add recursive summarizer fallback.
- Preserve explicit task state, required skills, and current plan in compaction
  summaries.

### `internal/executor/sessionlog.go`

Strengths:

- Rich structured event coverage.
- Logs context, budget, knobs, provider requests/responses, retries,
  corrections, reasoning, skill events, outside-workspace attempts, compaction,
  and errors.

Important anchors:

- `sessionlog.go:28` verbatim logging privacy note.
- `sessionlog.go:50` event definitions.
- `sessionlog.go:177` assistant/tool logging.
- `sessionlog.go:347` append behavior.

Gaps:

- Logs can contain full args, outputs, assistant text, and reasoning verbatim.
- Append failures are mostly ignored.

Recommendations:

- Add redacted and raw log modes.
- Default UI/API surfaces to redacted events.
- Treat append failure as a policy decision: either fail closed for audit-critical
  runs or mark the session degraded.

### `internal/platform/platform.go`

Strengths:

- Central command runner abstraction.
- Process group support.
- Timeout/interrupt behavior.
- Capped output.
- Workspace snapshot and checkpoint archive support.

Recommendations:

- Use this as the enforcement point for native containment metadata.
- Add tests for process-group cleanup, checkpoint exclusions, archive restore,
  and interrupted long-running commands.

### `internal/sessionrun/localrun.go`

Strengths:

- Session creation and manifest flow are coherent.
- Worker process group start is present.
- Executor and PVG paths are both wired.

Important anchors:

- `localrun.go:47` session creation.
- `localrun.go:237` worker process-group start.

Recommendations:

- Strengthen stop semantics: SIGTERM, wait, SIGKILL escalation, persist observed
  exit state.
- Record worker lifecycle state in a durable event log.

### `internal/sessionrun/workspace.go`

Strengths:

- Clean workspace preparation model.
- Git workspace clone path rejects dirty worktrees, submodules, and LFS.
- Detached checkout and remote removal reduce accidental mutation.
- Artifact extraction has path safety.
- Snapshot copy path is explicit.

Important anchors:

- `workspace.go:153` workspace scope.
- `workspace.go:200` prepare/ensure workspace.
- `workspace.go:311` cloneGitWorkspace.
- `workspace.go:449` detached/remotes cleanup.
- `workspace.go:482` artifact extraction.
- `workspace.go:636` snapshot/copy support.

Recommendations:

- Keep this. It is one of Telos's strongest execution-safety assets.
- Connect workspace scope to executor file-tool scope so the same boundary is
  enforced in both places.

### `internal/agentsession/protocol.go`

Strengths:

- Typed JSONL session event schema.
- Good basis for durable streams and CLI/API replay.

Important anchors:

- `protocol.go:12` top-level event schema.
- `protocol.go:37` payload types.

Recommendations:

- Treat this as the public stream contract.
- Add versioning and compatibility tests.
- Emit provider/tool lifecycle states with enough detail to replace polling CLI
  logs.

### `internal/sessionapi/store.go`

Strengths:

- Clear file-backed session store.
- Process-local locking prevents intra-process races.
- Stop path exists.

Important anchors:

- `store.go:52` process-local mutex.
- `store.go:568` stop behavior.
- `store.go:986` worker process handling.

Gaps:

- No OS-level interprocess lock.
- Stop behavior should wait/escalate and persist observed state.
- Event durability is weaker than Zero's session store.

Recommendations:

- Add file locks.
- Use unique temp files.
- fsync file and directory on manifest/event writes.
- Add sequence numbers to event appends.
- Record stop request, signal sent, process exit, and forced kill as distinct
  events.

### `internal/sessionapi/manifest.go`

Strengths:

- Manifest format is simple and auditable.
- Scoped token fields are visible.

Important anchors:

- `manifest.go:12` manifest fields.
- `manifest.go:88` scoped token fields.
- `manifest.go:224` write path.

Recommendations:

- Do not store bearer-equivalent scoped tokens directly in session manifests.
  Store hashes and an index instead.
- Replace fixed `session.json.tmp` with unique temp files.
- fsync written files and parent directories.

### `internal/sessionapi/auth.go`

Strengths:

- Role and descendant-access checks are clean.
- Scoped token semantics are simple.

Important anchors:

- `auth.go:160` authorization logic.
- `auth.go:223` token generation.
- `auth.go:296` token lookup.

Recommendations:

- Replace manifest scanning token lookup with an indexed hashed-token store.
- Add token rotation/revocation event semantics.
- Keep descendant-access checks; they are a real product differentiator.

### `cmd/telos/logs.go` and `internal/sessionapi/server.go`

Strengths:

- SSE endpoint exists.
- CLI has a usable logs command.

Important anchors:

- `cmd/telos/logs.go:71` polling loop.
- `internal/sessionapi/server.go:259` SSE endpoint.

Recommendations:

- Make structured streaming the default CLI path for active sessions.
- Preserve polling as fallback.
- Align emitted events with `internal/agentsession/protocol.go`.

### `cmd/telos/launch.go`

Strengths:

- Root/child session behavior is explicit.
- Concurrent controller child sessions are blocked unless an env justification
  is present.

Important anchor:

- `cmd/telos/launch.go:307` controller-child guardrail.

Recommendation:

- Keep this. Zero has lineage, but Telos's launch guardrail is more directly
  aligned with multi-agent product safety.

### `internal/spec/package.go`

Strengths:

- Deterministic package and provenance behavior.
- Good foundation for reproducible deployment.

Important anchors:

- `package.go:74` package creation.
- `package.go:144` deterministic file handling.
- `package.go:227` metadata/provenance.
- `package.go:356` package verification.

Recommendation:

- Keep this as a Telos advantage. Tie package provenance into session execution
  logs so a run can prove which spec/package it executed.

### `internal/telosd/kubernetes_worker.go`

Strengths:

- Real Kubernetes worker lifecycle support.
- Creates namespaces, service accounts, network policies, secrets, and runtime
  checks.
- Runs workers as non-root.

Important anchors:

- `kubernetes_worker.go:329` namespace/service-account setup.
- `kubernetes_worker.go:423` hostPath state mount.
- `kubernetes_worker.go:537` worker pod spec.
- `kubernetes_worker.go:635` runtime checks.
- `kubernetes_worker.go:986` ClusterRole rules.
- `kubernetes_worker.go:1033` network policy.
- `kubernetes_worker.go:1122` runtime bootstrap.

Gaps:

- HostPath state mount expands node blast radius.
- ClusterRole verbs are broad.
- Network policy allows `10.0.0.0/8` egress.
- Runtime bootstrap via latest release, curl, and jq is fragile.

Recommendations:

- Prefer PVC or a narrower storage mount over hostPath where possible.
- Reduce ClusterRole verbs to observed minimums.
- Narrow egress policy to required endpoints.
- Pin runtime artifacts by version and digest.
- Move bootstrap logic into an image or verified init artifact.

### Reconcilers and HTTP server

Telos reconcilers are simple polling loops:

- `internal/telosd/control_reconciler.go:39`
- `internal/telosd/control_reconciler.go:88`
- `internal/telosd/route_reconciler.go:64`

They abort a cycle on first error. Zero's daemon worker pool uses retry
classification and jitter around `internal/daemon/pool.go:88` and `pool.go:176`.

Telos's HTTP server has read-header timeout and graceful shutdown in
`internal/telosd/server.go:61`. Zero's daemon additionally tracks connections,
closes stalled peers, and bounds handshakes in `internal/daemon/server.go:168`
and `server.go:231`.

Recommendations:

- Add per-item reconcile error isolation.
- Add jittered backoff and temporary/permanent error classification.
- Track HTTP connections and close stalled peers during shutdown.

## Feature-by-Feature Comparison

### Tool-call lifecycle

Zero keeps tool-call lifecycle validity close to the agent loop. Provider stream
events are collected into ordered tool calls, duplicate/empty IDs are handled,
tool results are appended in transcript-valid shape, and missing or dropped
tool calls trigger recovery behavior.

Telos records tool results cleanly in conversation state, but argument
validation and lifecycle recovery are thinner. Its strongest addition is
protocol correction prompts. Telos should keep those and add:

- dropped-call detection
- missing tool-result correction
- required/enums/additionalProperties validation
- tolerant malformed JSON recovery when safe
- explicit terminal classification when recovery is exhausted

### Completion semantics

Zero's headless exec path distinguishes "assistant said something" from "the
task is complete." `RequireCompletionSignal` is the important product concept.

Telos has budget exhaustion and terminal results, but should expose a comparable
automation contract:

- completed
- incomplete
- exhausted
- interrupted
- blocked by policy
- failed by provider
- failed by tool

This matters for CI, controller sessions, and deployment workers.

### Context management

Telos's compactor is already safer around function-call boundaries than many
agent frameworks. Zero adds production behaviors Telos lacks:

- stale output pruning
- tool-definition token accounting
- recursive fallback
- provider error-triggered reactive compaction
- context measurement events

Adopting these is low-risk because Telos already has a compaction abstraction.

### Path and mutation safety

Zero treats file mutation as a shared safety problem. Telos treats each file
tool as a direct operation inside a trusted workspace. The Telos approach is
reasonable only when workspace containment is guaranteed and there are no
concurrent writers. Production multi-agent sessions weaken both assumptions.

Telos should add:

- scoped path resolver
- symlink escape prevention
- file baselines
- stale-write refusal
- changed-file metadata
- redacted previews
- conflict tests

### Sandboxing and permissions

Zero has a real permission/sandbox model. Telos has an external containment
model. Either can work, but Telos currently has an observability gap: a session
log does not prove which containment boundary was active for each tool.

Minimum Telos production bar:

- Native executor fails closed if containment is required but absent.
- Every tool event records containment mode.
- Shell and filesystem tools share a policy evaluator.
- Logs redact sensitive args/outputs by default.

### Session durability

Zero is ahead on local persistence engineering:

- OS locks
- fsync
- sequence numbers
- durable event logs
- checkpoint/rewind

Telos is ahead on domain/session shape:

- root/child sessions
- scoped descendant auth
- manifests
- spec packaging
- artifacts
- deployment workers

The best direction is to keep Telos's data model and harden it with Zero's file
storage practices.

### Streaming and observability

Zero's CLI exec stream is a product-quality automation interface. Telos has
enough protocol structure to match it, but active CLI logs still poll.

Telos should make `internal/agentsession/protocol.go` the default wire format
for live stream consumers and persist exactly what the stream emits, with raw
events available only under explicit debug/audit settings.

### Orchestration

Telos is stronger here. Zero has sessions, daemon workers, lineage, and
checkpointing, but Telos's root/child sessions, controller guardrails, spec
packaging, Kubernetes workers, and deployment flows are more specialized and
more valuable for Telos's target product.

The main orchestration work is hardening:

- durable manifests/events
- robust worker stop
- narrower Kubernetes permissions
- better reconcile retries
- streaming logs
- token hashing/indexing

## What Telos Should Adopt From Zero

### 1. Tool registry envelope

Build a new internal layer around native tools. The interface should look more
like:

- tool definition
- required permissions
- path scope policy
- sandbox/containment policy
- redaction policy
- execution function
- result metadata
- changed files
- audit classification

All native tool execution should pass through it. Existing tool functions can
remain mostly intact behind the boundary.

### 2. Scoped workspace resolver

Create one resolver used by read, write, replace, apply_patch, list, search,
find, file_info, and bash working directories.

It should:

- reject paths outside workspace scope
- handle symlinks safely
- recheck write targets
- record outside-workspace attempts
- support explicit multi-root scopes if Telos needs them

### 3. FileTracker-style stale-write protection

Track baselines for read files and refuse writes/edits/patches when the file has
changed unexpectedly. This matters more in Telos than in single-agent tools
because root/child sessions and concurrent workers can mutate related state.

### 4. Redacted log surface

Keep raw audit logs only under explicit configuration. Default session APIs and
CLI streams should use redacted events. Tool schemas should tell the registry
which args and outputs may contain secrets.

### 5. Completion contract

Add an explicit completion signal or terminal state contract for native
execution. Controller sessions and deployment flows should not infer success
from any assistant text.

### 6. Compaction upgrades

Add Zero's stale-output pruning, tool-token accounting, recursive summarizer
fallback, and broader provider context-limit detection to Telos's existing
compactor.

### 7. Durable session storage

Harden file-backed session storage with:

- OS locks
- unique temp files
- fsync on files
- fsync on parent directories
- sequence numbers
- event append integrity tests
- hashed scoped-token index

### 8. Structured live streaming

Make structured stream events the default for active sessions. Polling should
remain only as a fallback for old clients or static log reads.

### 9. Worker lifecycle policy

Adopt Zero daemon-style worker shutdown:

- stop requested event
- signal sent event
- grace wait
- forced kill
- observed exit persisted
- temporary/permanent error classification
- jittered retries

### 10. Sandbox/containment metadata

Even if Telos continues to rely on containers instead of in-process sandbox
profiles, every native tool event should include:

- containment mode
- workspace root
- network policy status if known
- write scope
- read scope
- shell policy

## What Telos Should Not Copy Blindly

- Do not replace Telos's orchestration model with Zero sessions. Telos's
  root/child session and spec packaging model is product-specific and valuable.
- Do not copy Zero's default sandbox policy as if it were full isolation. It is a
  practical agent sandbox, not a complete secret boundary.
- Do not introduce every Zero tool family at once. MCP, web, LSP, hooks, and
  persistent shell are useful, but the registry boundary should come first.
- Do not dilute Telos's rubric/skill gate. Zero's generic loop does not replace
  Telos's domain-specific evidence checks.
- Do not expand provider support before creating a provider-neutral event
  contract. Otherwise Telos will duplicate OpenAI-specific assumptions.

## What Telos Already Does Better

- Root/child session semantics and descendant authorization.
- Controller-child concurrency guardrails.
- Spec packaging and provenance.
- Artifact extraction and workspace preparation.
- Kubernetes worker provisioning.
- Budgeted native execution tied to product sessions.
- Rubric/skill coverage gates.
- PVG/game integration.
- Deployment-oriented CLI and daemon surfaces.

These should remain the backbone of Telos. Zero should influence the executor
and storage boundaries, not the product identity.

## Adoption Plan

### Phase 0: invariants to decide first

Before implementation, make these explicit decisions:

1. Native executor containment: required always, required in daemon/sessionrun
   only, or optional for local developer use.
2. Log privacy mode: raw by default, redacted by default, or dual log streams.
3. Workspace model: single root only or named multi-root scopes.
4. Completion contract: explicit tool/signal, assistant final marker, or
   structured terminal event.
5. Provider roadmap: OpenAI-only for now or near-term multi-provider.

### P0: immediate production hardening

Implement these first because they reduce blast radius without changing product
direction:

1. Add a native tool registry envelope in `internal/executor`.
2. Route every native tool through central policy/log/redaction metadata.
3. Replace file path handling with a scoped, symlink-aware resolver.
4. Add file baseline tracking for read/write/edit/patch.
5. Enforce required/enums/additionalProperties in tool argument validation.
6. Add changed-file metadata to all mutating tools.
7. Add redacted session log output mode.
8. Add explicit terminal result classifications.
9. Harden manifest/event writes with OS locks, unique temp files, and fsync.
10. Hash scoped tokens and add an index instead of scanning raw manifests.

Acceptance tests:

- `internal/executor`: symlink escape rejection, absolute path policy,
  stale-write refusal, changed-file metadata, malformed tool args, missing
  required fields, enum rejection, redacted tool output.
- `internal/sessionapi`: concurrent manifest writes across processes, crash-like
  temp file behavior, token hash lookup, append sequence integrity.
- `internal/sessionrun`: contained vs uncontained native executor startup.

### P1: loop, compaction, and streaming

Add higher-level runtime resilience:

1. No-output and repeated-failure guardrails in `internal/executor/loop.go`.
2. Dropped tool-call recovery.
3. Final completion nudge before incomplete return.
4. Stale tool-output pruning in compaction.
5. Tool-definition token accounting.
6. Recursive summarizer fallback.
7. Broader context-limit error detection.
8. Structured live stream CLI using `internal/agentsession/protocol.go`.
9. Worker stop with wait/escalate/persisted exit state.
10. Async provider poll cap independent of request timeout.

Acceptance tests:

- Loop recovers from malformed/missing tool calls and eventually returns a
  classified incomplete state.
- Compaction never leaves orphan function outputs.
- Compaction prunes stale tool outputs and preserves required skills/plans.
- CLI stream emits assistant, reasoning, tool call, tool result, usage, and
  terminal events in order.
- Worker stop records every lifecycle transition.

### P2: provider and extension boundaries

Add extensibility after the core executor is safer:

1. Introduce provider-neutral stream event interfaces.
2. Move OpenAI Responses code behind that interface.
3. Add provider replay fixtures for schema drift.
4. Add hook support around tool execution.
5. Add gated web fetch/search if product needs it.
6. Add MCP only after permission/redaction policy is centralized.
7. Add LSP/navigation tools if code-editing workflows need richer context.
8. Add optional persistent shell sessions.
9. Add sandbox profile support or container policy verification metadata.

Acceptance tests:

- Provider adapters produce identical normalized events for equivalent fixtures.
- Hook timeouts fail closed.
- Web/MCP tools cannot reach local/private/metadata addresses unless explicitly
  allowed.
- Persistent shell sessions clean up child processes.

### P3: deployment hardening

Harden production daemon and Kubernetes surfaces:

1. Reduce Kubernetes ClusterRole verbs.
2. Replace or narrow hostPath state mount.
3. Narrow network policy egress.
4. Pin runtime artifacts by version and digest.
5. Move bootstrap into an image or verified init artifact.
6. Add per-item reconciler error isolation.
7. Add jittered backoff and temporary/permanent error classification.
8. Track HTTP connections and close stalled peers.

Acceptance tests:

- Kubernetes manifests match least-privilege snapshots.
- Reconciler continues processing other items after one item fails.
- Server shutdown drains active streams and closes stalled clients.
- Worker runtime provenance is recorded in session events.

## Suggested One-Pass Implementation Order

If the goal is to make Telos materially more production-ready in one sustained
engineering pass, the most efficient order is:

1. Build the executor registry envelope with metadata, redaction hooks, and
   changed-file fields.
2. Implement scoped resolver and file baseline tracking.
3. Port native file/search/patch/bash tools onto the envelope.
4. Expand argument validation and tests.
5. Add explicit terminal classifications and completion contract.
6. Add redacted session event output.
7. Harden session store writes and token storage.
8. Add loop guardrails and completion nudge.
9. Add compaction pruning/fallback/context-limit upgrades.
10. Replace CLI polling for active logs with structured streaming.
11. Add worker stop lifecycle persistence.
12. Start provider-neutral event interface only after the above is stable.

This order keeps the blast radius controlled: first secure the boundary, then
improve the loop, then expose better streams, then generalize providers.

## Risk Register

| Risk | Current state | Impact | Mitigation |
| --- | --- | --- | --- |
| Native tools mutate outside intended scope | Lexical checks and external containment | Host/session damage | Scoped resolver, containment precondition, tests |
| Secrets leak through logs | Verbatim session logs | Credential exposure | Redacted default stream/logs, raw opt-in |
| Concurrent writers clobber files | No stale baseline tracking | Lost work and bad patches | FileTracker-style baselines |
| Session manifests race across processes | Process-local mutex only | Corrupt state | OS locks, fsync, unique temp files |
| Scoped tokens leak from manifests | Tokens stored directly | Privilege escalation | Hash/index tokens |
| Provider schema drift breaks loop | Responses-specific parsing | Stuck sessions | Replay fixtures, normalized event contract |
| Context compaction loses state | Good boundaries, less pruning/fallback | Repeated failures or forgotten plans | Pruning, recursive fallback, state preservation |
| Worker stop leaves processes behind | Thin stop lifecycle | Resource leaks | Wait/escalate/persist lifecycle |
| Kubernetes permissions too broad | Broad role/egress/hostPath | Cluster blast radius | Least privilege and pinned runtime |

## Appendix: High-Value Line References

Zero:

- `internal/agent/loop.go:107` - agent run loop
- `internal/agent/loop.go:156` - proactive compaction
- `internal/agent/loop.go:289` - stream stall handling
- `internal/agent/loop.go:357` - dropped/no-output recovery
- `internal/agent/loop.go:557` - post-edit self-correction
- `internal/agent/loop.go:571` - model escalation after tool batches
- `internal/agent/compaction.go:27` - compaction trigger
- `internal/agent/compaction.go:334` - tool-output accounting
- `internal/agent/compaction.go:395` - context-limit recovery
- `internal/agent/compaction.go:456` - recursive fallback
- `internal/agent/prune.go:10` - stale output pruning
- `internal/zeroruntime/types.go:208` - provider interface
- `internal/zeroruntime/helpers.go:90` - stream collector
- `internal/zeroruntime/helpers.go:204` - tool-call collector
- `internal/tools/types.go:14` - tool metadata model
- `internal/tools/registry.go:117` - registry execution boundary
- `internal/tools/workspace.go:34` - path scope
- `internal/tools/file_tracker.go:12` - file baselines
- `internal/tools/web_fetch.go:143` - public web validation
- `internal/sandbox/types.go:256` - sandbox policy
- `internal/sandbox/engine.go:277` - sandbox evaluation
- `internal/mcp/registry.go:63` - MCP tool registry
- `internal/hooks/dispatch.go:112` - hook dispatch
- `internal/sessions/store.go:515` - session locking
- `internal/sessions/store.go:740` - durable writes
- `internal/daemon/pool.go:321` - worker drain/kill

Telos:

- `internal/executor/executor.go:52` - native executor preconditions
- `internal/executor/executor.go:101` - ExecuteTurn
- `internal/executor/loop.go:255` - native agent loop
- `internal/executor/loop.go:322` - budget checks
- `internal/executor/loop.go:338` - correction/rubric gate
- `internal/executor/responses.go:289` - provider send path
- `internal/executor/responses.go:358` - retries
- `internal/executor/responses.go:607` - tool-call extraction
- `internal/executor/dispatch.go:181` - argument validation
- `internal/executor/tools.go:16` - unsandboxed native tools warning
- `internal/executor/table.go:41` - tool schemas
- `internal/executor/fileio.go:176` - write tool
- `internal/executor/fileio.go:205` - edit tool
- `internal/executor/fileio.go:275` - path resolution
- `internal/executor/patch.go:194` - patch path validation
- `internal/executor/bash.go:10` - native bash tool
- `internal/executor/compactor.go:311` - function-call boundary logic
- `internal/executor/sessionlog.go:28` - verbatim log privacy note
- `internal/sessionrun/workspace.go:311` - git workspace clone
- `internal/sessionrun/workspace.go:482` - artifact extraction
- `internal/sessionapi/store.go:52` - process-local store mutex
- `internal/sessionapi/manifest.go:224` - manifest write path
- `internal/sessionapi/auth.go:160` - authorization checks
- `internal/sessionapi/auth.go:296` - token lookup
- `cmd/telos/logs.go:71` - polling logs
- `internal/sessionapi/server.go:259` - SSE endpoint
- `cmd/telos/launch.go:307` - controller-child guardrail
- `internal/spec/package.go:74` - spec package creation
- `internal/telosd/kubernetes_worker.go:423` - hostPath state mount
- `internal/telosd/kubernetes_worker.go:986` - ClusterRole rules
- `internal/telosd/kubernetes_worker.go:1033` - network policy

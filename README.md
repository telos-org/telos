# Telos

Telos is a spec-driven coding agent runtime for declarative goals and
background agent work.

Write a `SPEC.md`. Telos runs it as a durable session: implement, evaluate,
record evidence, and continue under policy.

This repo contains:

- `telos`, the CLI
- `telosd`, the environment-local Sessions API and worker runtime
- spec rendering, skills, transcripts, evidence, and release tooling

The hosted control plane and product surface live in `telos-org/cloud`.

## Install

```bash
curl -fsSL https://usetelos.ai/install.sh | sh
telos --version
```

The installer downloads checksummed `telos` and `telosd` binaries for your
platform.

## Run Locally

```markdown
---
version: v0
name: hello-service
platform: local
skills:
  - verify-engineering*
---

# Spec

Build a small HTTP service with `/healthz`, tests, and local run instructions.
```

```bash
telos run SPEC.md --workspace . --until 3
```

Local runs use Telos' native executor through a LiteLLM-compatible gateway.
Configure the gateway before starting a run:

```bash
export TELOS_LITELLM_BASE_URL="https://litellm.example.com/v1"
export TELOS_LITELLM_API_KEY="..."
telos run SPEC.md --workspace . --model anthropic/claude-sonnet-4.5 --until 3
```

`TELOS_API_BASE_URL`, `TELOS_BASE_URL`, and `TELOS_API_KEY` are accepted as
compatibility aliases. Model names are sent to the gateway unchanged.
Per-response cost is taken from what the LiteLLM gateway reports (the
`x-litellm-response-cost` header or a cost field in the response body). For
models the gateway does not price, cost is reported as unavailable and any
`--max-cost-usd` cap is not enforced for those turns.

Migration note: Telos no longer reads Pi, Harbor, or direct provider credential
environment variables such as `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, or
`SAIL_API_KEY` in the native executor. Put provider credentials in your LiteLLM
deployment and expose only the Telos gateway URL/key variables to Telos.

Workspace checkpoints exclude runtime, dependency, build, cache, and secret-like
paths by default. For benchmark tasks where the full workspace is the required
artifact, set `TELOS_CHECKPOINT_INCLUDE_ALL=1`.

### Native executor security model

The native executor's file/bash tools are deliberately unsandboxed: reads,
writes, and `bash` operate on whatever the process can touch, with no workspace
jail or write allowlist. The only real containment is whatever sandbox the
executor itself runs inside (e.g. an ephemeral container/pod). Relative paths
still resolve against the workspace for convenience and out-of-workspace access
is logged for telemetry, but neither is a trust boundary — `bash` and absolute
paths bypass both by design. Do not run the native executor on a host with
secrets or state you cannot afford to expose to the agent.
Session logs record tool arguments and outputs verbatim and may contain the same
secrets as the workspace; store and handle them at the workspace trust level.

### Budgets, knobs, and precedence

Per-session budgets (`--max-rounds`, `--max-tool-loops`, `--max-output-tokens`,
`--agent-timeout-sec`, etc., or their `TELOS_*` flag/env equivalents) are
persisted in the session manifest and are the source of truth for a run. The
manifest always wins over ambient environment.

A small set of executor-internal escape hatches have no manifest equivalent and
remain environment-only:

| Variable | Default | Effect |
| --- | --- | --- |
| `TELOS_NATIVE_TOOL_MAX_BYTES` | 96 KiB | Max bytes of a single tool's text output |
| `TELOS_NATIVE_TOOL_MAX_LINES` | 400 | Max lines of a single tool's text output |
| `TELOS_NATIVE_KEEP_REASONING` | unset | `1` disables stripping of reasoning/COT tags from visible output |
| `TELOS_AUTOCOMPACT_CONTEXT_WINDOW` | 128000 | Configured context window for stateless-history autocompaction; floored to the model's actual window (see `TELOS_MODEL_CONTEXT_WINDOW` / built-in family defaults); `0` disables |
| `TELOS_AUTOCOMPACT_TRIGGER_RATIO` | 0.75 | Fraction of the context window allowed for input before compacting |
| `TELOS_AUTOCOMPACT_KEEP_RECENT_TOKENS` | 20000 | Estimated recent-history tokens retained verbatim after compaction |
| `TELOS_AUTOCOMPACT_STRATEGY` | `llm` | `llm` summarizes old history; `truncate` drops old history for comparison/debugging |

These are resolved once per turn and recorded in the turn's session log
(`env_knobs` event) so a run is auditable from the log alone. Per-model
capability/behavior is configured via the `TELOS_MODEL_*` family
(`TELOS_MODEL_CAPABILITY_PROFILE`, `TELOS_MODEL_STATE_MODE`,
`TELOS_MODEL_CONTEXT_WINDOW`, etc.);
the resolved profile (including the effective context window) is logged at turn
start (`provider_config` event, no secrets). The autocompaction trigger uses the
floor of `TELOS_AUTOCOMPACT_CONTEXT_WINDOW` and the model's real context window
(explicit capability value, else a built-in family default) so it never exceeds
what the model can hold.

`TELOS_NATIVE_MAX_TOOL_LOOPS` and `TELOS_NATIVE_MAX_OUTPUT_TOKENS` were removed:
they conflicted with the manifest budgets (tool-loop budget was overridden by
env; output-token env acted as a hidden base ceiling that could only cap down).
Use the manifest `max_tool_loops` / `max_output_tokens` budgets instead.

## Run In Cloud

```bash
telos login
telos apply SPEC.md --env <env-handle>
```

Use `run` for bounded tasks. Use `apply` for persistent controllers.

## Read Next

- License: [`LICENSE`](LICENSE)

## Develop

```bash
go test ./...
go build ./cmd/telos ./cmd/telosd
```

Release builds use Bazel:

```bash
bazel test //...
scripts/publish-release.sh
```

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
If your LiteLLM gateway cannot report per-response cost, configure known exact
aliases with `TELOS_MODEL_PRICING_TABLE`, for example
`{"team/fast":{"input_usd_per_1m_tokens":1.0,"output_usd_per_1m_tokens":3.0}}`.

Migration note: Telos no longer reads Pi, Harbor, or direct provider credential
environment variables such as `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, or
`SAIL_API_KEY` in the native executor. Put provider credentials in your LiteLLM
deployment and expose only the Telos gateway URL/key variables to Telos.

Workspace checkpoints exclude runtime, dependency, build, cache, and secret-like
paths by default. For benchmark tasks where the full workspace is the required
artifact, set `TELOS_CHECKPOINT_INCLUDE_ALL=1`.

Native executor writes are workspace-only by default. To allow writes under
specific absolute paths, pass `--safe-write-prefixes /tmp/telos-scratch` or set
`TELOS_SAFE_WRITE_PREFIXES` to a comma-separated prefix list.

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

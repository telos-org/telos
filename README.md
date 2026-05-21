# Telos

Telos turns a `SPEC.md` into operated software with evidence.

A spec describes the desired world. Telos runs implementation agents, evaluates
the live result against the same contract, records a session transcript and
evidence trail, and keeps iterating until the contract is satisfied or the
configured run policy is exhausted.

This repository contains the Go runtime:

- `telos`: CLI for local runs, cloud runs, session inspection, and controller
  application
- `telosd`: environment-local Sessions API daemon and worker runtime
- built-in spec skills, prompt rendering, evidence, transcripts, workspace
  snapshots, and Kubernetes/local launchers

The hosted control plane, frontend, accounts, billing, and environment
provisioning live in `telos-org/cloud`.

## Install

```bash
curl -fsSL https://usetelos.ai/install.sh | sh
```

The installer downloads checksummed `telos` and `telosd` binaries for your
platform and installs them into `~/.local/bin` by default.

For containers or custom install paths:

```bash
TELOS_INSTALL_DIR="$HOME/.local/bin" \
curl -fsSL https://usetelos.ai/releases/latest/install.sh | sh
```

Make sure the install directory is on `PATH`:

```bash
export PATH="$HOME/.local/bin:$PATH"
telos --version
telosd --version
```

## Quick Start

Create a `SPEC.md`:

```markdown
---
version: v0
name: hello-service
platform: local
skills:
  - verify-engineering*
---

# Spec

Build a small HTTP service in this workspace. It must expose `/healthz`, include
a reproducible test command, and document how to run it locally.
```

Run locally:

```bash
telos run SPEC.md --workspace . --until 3
```

Inspect the session:

```bash
telos list
telos describe <session-id>
telos logs <session-id>
telos stop <session-id>
```

Local live runs require `pi` on `PATH` and model credentials configured for Pi.

## Cloud Runs

After login, submit specs to a managed environment:

```bash
telos login
telos apply SPEC.md --env <env-handle>
```

Use `apply` for persistent controllers and managed systems. Use `run` for
bounded tasks. Both paths submit raw spec markdown to the environment-local
Sessions API.

## Sessions API

`telosd` serves the environment-local Sessions API. Local and cloud runtimes use
the same session model:

```text
SPEC.md -> telos CLI -> Sessions API -> telosd -> worker -> evidence/transcript/workspace
```

Session artifacts include:

- immutable source spec snapshot
- append-only session transcript
- evidence JSONL
- workspace checkpoint
- status, progress, cost, and product handles

The API and runtime mental model are documented in
[`docs/sessions-api/SPEC.md`](docs/sessions-api/SPEC.md).

## Harbor / SCBench

Telos can run as a Harbor executable agent. The Harbor shim is Python because
Harbor's extension boundary is Python, but the evaluated agent is the Go
`telos` binary installed through the public release script.

Reproduce the SCBench circuit-eval harness:

```bash
OPENAI_API_KEY=... \
TELOS_HARBOR_MODEL=openai-codex/gpt-5.5 \
TELOS_HARBOR_UNTIL=1 \
TELOS_HARBOR_SKILLS='verify-engineering*' \
./integrations/harbor/run_scbench_circuit_eval.sh
```

Run with repair turns and quality review:

```bash
TELOS_HARBOR_UNTIL=3 \
TELOS_HARBOR_SKILLS='verify-engineering*,verify-quality*' \
./integrations/harbor/run_scbench_circuit_eval.sh
```

See [`integrations/harbor/README.md`](integrations/harbor/README.md).

## Development

Canonical build and release path:

```bash
bazel test //...
bazel build //cmd/telos:telos //cmd/telosd:telosd
scripts/build-release.sh v0.0.0
```

Fast local checks:

```bash
go test ./...
go build ./cmd/telos ./cmd/telosd
```

Publish a stamped runtime release:

```bash
scripts/publish-release.sh
```

## Package Map

| Package | Purpose |
|---|---|
| `cmd/telos` | CLI entry point |
| `cmd/telosd` | Sessions API daemon and worker entry point |
| `internal/cli` | Local session creation, run/apply/list/describe/logs orchestration |
| `internal/sessionapi` | Shared Sessions API types, file store, HTTP routes |
| `internal/spec` | SPEC.md loading, skills, prompt rendering |
| `internal/game` | Iterative implementation/evaluation loop and transcript rendering |
| `internal/executor` | Pi executor and event parsing |
| `internal/evidence` | Evidence JSONL writer/reader |
| `internal/platform` | Local subprocess execution and workspace snapshots |
| `internal/telosd` | telosd config, cloud mode, Kubernetes worker launcher |
| `internal/cloud` | Hosted control-plane and environment client |
| `integrations/harbor` | Harbor executable-agent shim for SCBench/eval runs |

## Runtime Boundary

New durable runtime work belongs in this repo. The cloud repo owns hosted
product surfaces and provisioning; this repo owns the environment-local runtime,
Sessions API behavior, worker execution, transcripts, evidence, and release
artifacts.

## License

Functional Source License, Version 1.1, ALv2 Future License. See
[`LICENSE`](LICENSE).

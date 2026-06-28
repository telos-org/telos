# Telos

Telos is a spec-driven coding agent runtime for declarative goals and
background agent work.

Write a `SPEC.md`. Telos applies it as a durable goal: implement, evaluate,
record evidence, and continue reconciling under policy.

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

Local runs require `pi` on `PATH` and model credentials configured for Pi.

## Run In Cloud

```bash
telos login
telos apply SPEC.md --env <env-handle>
```

For hosted workflows, `telos apply` is the primary interface: it applies a
declarative goal and keeps reconciling it. `telos run` remains available for
local specs and internal delegated work.

## Read Next

- Harbor / SCBench executable agent: [`integrations/harbor`](integrations/harbor)
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

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

Local runs require `pi` on `PATH` and model credentials configured for Pi.

## Run In Cloud

```bash
telos login
telos apply SPEC.md --env <env-handle>
```

Use `run` for bounded tasks. Use `apply` for persistent controllers.

## Read Next

- Runtime model and Sessions API: [`docs/sessions-api/SPEC.md`](docs/sessions-api/SPEC.md)
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

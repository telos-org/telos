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

Local runs execute spec turns through
[pi](https://github.com/mariozechner/pi-coding-agent), an open-source coding
agent:

```bash
npm install -g @mariozechner/pi-coding-agent
```

Configure model credentials for pi before your first run.

## Run In Cloud

```bash
telos login
telos apply SPEC.md --scope <scope>
telos list --cloud
```

For hosted workflows, `telos apply` publishes a versioned spec package and
creates or updates a cloud session. The control plane allocates the runtime
behind that session. `telos run` remains available for local specs and internal
delegated work.

## Read Next

- Harbor / SCBench executable agent: [`integrations/harbor`](integrations/harbor)
- License: [`LICENSE`](LICENSE)

## Develop

```bash
go test ./...
go build ./cmd/telos ./cmd/telosd
```

`scripts/smoke-test.sh` exercises the end-user CLI contract against a built
binary, including one tiny isolated local run.

Release builds use Bazel:

```bash
bazel test //...
scripts/publish-release.sh
```

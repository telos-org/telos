# telos

`telos` is a goal-oriented agent runtime. 

Use `telos` to turn software goals into running services. Maintain and manage goals, not code.

## Install

```bash
curl -fsSL https://usetelos.ai/install.sh | sh
telos --version
```

The installer downloads checksummed `telos` and `telosd` binaries for your
platform (macOS and Linux, amd64/arm64).

## Quickstart

Write a goal and save it as `SPEC.md`:

```markdown
---
version: 0.1.0
name: hello-service
platform: local
---

# Goal

Build a small HTTP service with `/healthz`, tests, and local run instructions.
```

Run it as a bounded task:

```bash
telos run SPEC.md --workspace . --until 3    # at most 3 review cycles
telos run SPEC.md --workspace . --until 30m  # at most 30 minutes
```

Local runs execute goal turns through
[pi](https://github.com/earendil-works/pi), an open-source coding agent:

```bash
npm install -g @earendil-works/pi-coding-agent
```

### Choosing a model

Telos defaults local runs to `openai-codex/gpt-5.5` with high thinking effort.
Authenticate that provider in pi before your first run, or select another pi
model with `--model`:

```bash
telos run SPEC.md --model openai-codex/gpt-5.5 --thinking high
```

Model names use `<provider>/<model-id>`. Set `TELOS_MODEL` to keep a different
default across runs; use `TELOS_THINKING` to change the thinking effort.
Providers, models, and credentials are managed by pi; run `pi` and use
`/login`, and see
[pi's model documentation](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/models.md)
for configuration details.

Local runs can optionally override the generator and verifier independently:

```bash
telos run SPEC.md \
  --generator-model openai-codex/gpt-5.5 --generator-thinking high \
  --verifier-model anthropic/claude-sonnet-4 --verifier-thinking medium
```

Omitted role settings inherit `--model` and `--thinking`. The equivalent
environment variables are `TELOS_GENERATOR_MODEL`, `TELOS_GENERATOR_THINKING`,
`TELOS_VERIFIER_MODEL`, and `TELOS_VERIFIER_THINKING`.

### Using registry skills

Reference an exact registry skill version directly from `SPEC.md`:

```yaml
skills:
  - "@telos/verify-engineering:0.1.0*"
```

Telos downloads the authenticated version, verifies its registry digest, and
pins the ref and digest in published package locks. The optional `*` marks a
required evaluation rubric.

## Apply a Goal

`run` executes bounded task work. `apply` creates or updates a durable
controller session that keeps reconciling desired state.

```bash
telos login
telos config --context @your-org
telos apply SPEC.md
telos list --cloud
```

Cloud commands run in the configured personal, team, or platform context.
Use `telos config` to show the active context and `telos config --context
@your-org` to switch it. Omit the context configuration to use your personal
context; `telos config --context personal` clears a stored organization
selection. The stored configuration keeps the user-facing handle as
`context: "@your-org"`; the CLI resolves its internal organization ID when
sending requests. `TELOS_CONTEXT` overrides the stored context for a single
command or process:

```bash
TELOS_CONTEXT=@your-org telos list --cloud
```

Hosted deployments pin their effective model when they are created. Override
the platform default with `--model` and `--thinking` (or `TELOS_MODEL` and
`TELOS_THINKING`). These settings seed new deployments; existing deployments
retain their pinned model.

To steer an existing controller, edit the same disk spec and apply it back to
the same session:

```bash
telos apply SPEC.md --session sess_...
```

The spec frontmatter `version` is the package version published to Telos Cloud,
so the reviewed file, package ref, and backend record stay aligned:

```yaml
version: 0.1.1
```

Managed Cloud is in early access; sign up at
[usetelos.ai](https://usetelos.ai).

## License

Fair Source (FSL-1.1), converts to Apache-2.0 two years after each release

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

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

## Apply a Goal

`run` executes bounded task work. `apply` creates or updates a durable
controller session that keeps reconciling desired state.

```bash
telos login
telos apply SPEC.md
telos list --cloud
```

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

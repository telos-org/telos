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

Write a goal:

```markdown
---
version: v0
name: hello-service
platform: local
---

# Goal

Build a small HTTP service with `/healthz`, tests, and local run instructions.
```

Run it once:

```bash
telos run goal.md --workspace . --until 3   # at most 3 turns
```

Local runs execute goal turns through
[pi](https://github.com/earendil-works/pi), an open-source coding agent:

```bash
npm install -g @earendil-works/pi-coding-agent
```

Configure model credentials for pi before your first run.

## Run a Goal in Telos Cloud

`run` executes a goal once. `apply` keeps it running.

```bash
telos login
telos apply goal.md
telos list --cloud
```

`telos apply` holds your goal in a persistent session on
[Telos Cloud](https://usetelos.ai) — reconciling it, not just executing it
once. Managed Cloud is in early access; sign up at
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

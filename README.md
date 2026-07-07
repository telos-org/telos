# telos

Telos is a goal-oriented agent runtime. 

Use Telos to turn software goals into running services. Maintain and manage goals, not code.

## Install

```bash
curl -fsSL https://usetelos.ai/install.sh | sh
telos --version
```

The installer downloads checksummed `telos` and `telosd` binaries for your
platform.

## Quickstart

Write a goal spec:

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
telos run goal.md --workspace . --until 3
```

Local runs execute Goal turns through
[pi](https://github.com/mariozechner/pi-coding-agent), an open-source coding
agent:

```bash
npm install -g @mariozechner/pi-coding-agent
```

Configure model credentials for pi before your first run.

## Run a Goal in Telos Cloud

```bash
telos login
telos apply SPEC.md --scope <scope>
telos list --cloud
```

`telos apply` runs a persistent `telos` session in the cloud, to execute your long running goal

# LICENSE

Fair Source (FSL-1.1), converts to Apache-2.0 two years after each major release

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

# Telos

**Declarative software.**

Describe the software you want in `SPEC.md`. `telos apply` turns that contract
into verified running software.

## Install

```bash
curl -fsSL https://usetelos.ai/install.sh | sh
telos login
```

The installer supports macOS and Linux on amd64 and arm64.
It also installs the canonical
[`@telos/telos-cli`](skills/telos-cli/SKILL.md) agent skill.

## Prompt

Give your coding agent an outcome:

> Use Telos for this Goal: &lt;what should remain true&gt;. Install and sign in if
> needed. Draft the smallest verifiable `SPEC.md`, then show me `telos plan`.
> After I approve, run `telos apply` and return the verifier evidence when it
> reaches Ready.

## Specification

A specification describes the outcome, important constraints, and evidence of
success. It does not prescribe the implementation.

```markdown
---
name: hello-service
version: 0.1.0
platform: cloud
---

# Goal

Run an HTTP service with persistent Postgres storage and a verified `/healthz`
endpoint.
```

## Apply

```bash
telos plan SPEC.md
telos apply SPEC.md
telos describe SESSION_ID
telos logs SESSION_ID
```

`plan` previews the contract without changing the Goal or target state. `apply`
creates or updates a persistent Goal in Telos Cloud. Ready means the verifier
accepted the exact running revision.

Read the [Telos documentation](https://usetelos.ai/docs) for the complete
workflow.

## Local runs

`telos run` executes a bounded Goal and stops. For a human, it is an imperative
tool for completing one piece of work within a limit. For an agent, it is a
declarative subsystem for satisfying a bounded subgoal and returning evidence.

Local runs use the open source
[pi coding agent](https://github.com/earendil-works/pi):

```bash
npm install -g @earendil-works/pi-coding-agent
pi # use /login to connect a model provider
telos run SPEC.md --workspace . --until 3
telos describe SESSION_ID
telos logs SESSION_ID
```

Callers supply the Goal and observe its state and evidence through `describe`
and `logs`; the implementation remains a black box.

## Develop

```bash
go test ./...
go build ./cmd/telos ./cmd/telosd
bazel test //...
```

## License

Fair Source (FSL-1.1), converting to Apache-2.0 two years after each release.

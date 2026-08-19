# telos

`telos` is a goal-oriented programming system.

> Work with your coding agent to write and iterate on your Goal spec, and have
> the agent drive the `telos` system for you.

## Install

### Via the CLI

```bash
curl -fsSL https://usetelos.ai/install.sh | sh
telos login
```

The installer supports macOS and Linux on amd64 and arm64.

`telos login` signs in to Telos Cloud and is required for `telos apply`.

### Via your coding agent

```text
Set up Telos.

- Install it with `curl -fsSL https://usetelos.ai/install.sh | sh`.
- Run `telos login` to sign in to Telos Cloud.
- Read the installed `telos-cli` skill to get started.
```

## Specifications

`SPEC.md` is the `main` entrypoint to a `telos` program. It declares the Goal,
the target platform, and the evidence required for success.

Skills are modular libraries imported by the specification. They package
reusable instructions, references, scripts, and assets, and can be loaded from
a local path or an immutable registry reference.

A starred skill is also a required evaluation rubric. The verifier evaluates
the result against every starred skill, and the revision cannot become Ready
until all of them pass.

```markdown
---
name: hello-service
version: 0.1.0
platform: cloud
skills:
  - skills/postgres
  - "@scope/service-readiness:1.0.0*"
---

# Goal

Run an HTTP service with persistent Postgres storage. Keep `/healthz`
available, preserve stored data across restarts, and return evidence for both.
```

## Apply

`telos apply` continuously reconciles a persistent Goal toward the desired
state in `SPEC.md`.

First, preview the contract without changing the Goal or its target state:

```bash
telos plan SPEC.md
```

After reviewing the plan, apply it:

```bash
telos apply SPEC.md
```

`apply` returns a session ID when the revision is accepted for work. The work
then continues in the background.

After applying, use `telos list` to find the session and `telos describe` to
check its status:

```console
$ telos list
NAME           STATUS  SESSION
hello-service  ready   sess_123

$ telos describe sess_123
Name      hello-service
Status    ready
Session   sess_123
Revision  sha256:abc123...
Service   https://hello-service.example.com
```

Status values:

- `working` — reconciliation is in progress.
- `ready` — the displayed revision is running and verified.
- `needs_attention` or `failed` — check `Reason` for details.

Follow agent updates and verification evidence with:

```bash
telos logs SESSION_ID
```

To update a live Goal, edit `SPEC.md`, bump its version, and apply the new
revision to the same session:

```bash
telos plan SPEC.md --session SESSION_ID
telos apply SPEC.md --session SESSION_ID
```

The agent system picks up the delta and incrementally reconciles the live
software toward the new desired state.

Read the [Telos documentation](https://usetelos.ai/docs) for the complete
workflow.

## Local runs

`telos run` executes a bounded Goal in a local workspace and stops. Use it for
one piece of work that does not need the persistent lifecycle of `telos apply`.

Local runs use the open source
[pi coding agent](https://github.com/earendil-works/pi):

```bash
npm install -g @earendil-works/pi-coding-agent
pi # use /login to connect a model provider
telos run SPEC.md --workspace . --until 3
telos describe SESSION_ID
telos logs SESSION_ID
```

`--until 3` stops the run after at most three review cycles.

## License

Fair Source (FSL-1.1), converting to Apache-2.0 two years after each release.

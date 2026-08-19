# telos

`telos` is a goal-oriented programming system.

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

`telos apply` makes the approved specification the desired state of a
persistent Goal. It hands the exact `SPEC.md` and its resolved skills to
background agents that implement, deploy, and verify the revision.

First, preview the contract without changing the Goal or its target state:

```bash
telos plan SPEC.md
```

After reviewing the plan, apply it:

```bash
telos apply SPEC.md
```

`apply` returns a session ID when the revision is accepted for work, not when it
is complete. Follow the session and inspect its evidence:

```bash
telos describe SESSION_ID
telos logs SESSION_ID
```

Ready means the running software produced from the exact specification and
locked skill revisions passed every required rubric and independent
verification.

To change an existing Goal, edit `SPEC.md`, bump its version, and apply the new
revision to the same session:

```bash
telos plan SPEC.md --session SESSION_ID
telos apply SPEC.md --session SESSION_ID
```

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

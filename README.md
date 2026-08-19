# telos

`telos` is a goal-oriented programming system. You describe what should remain
true; agents implement the current revision, and an independent verifier checks
the result against the Goal.

## Install

### Via the CLI

```bash
curl -fsSL https://usetelos.ai/install.sh | sh
telos login
```

The installer supports macOS and Linux on amd64 and arm64.

### Via your coding agent

Give your coding agent the same setup prompt used on
[usetelos.ai](https://usetelos.ai):

> Set up Telos.
>
> - Install it with `curl -fsSL https://usetelos.ai/install.sh | sh`.
> - Run `telos login` to sign in to Telos Cloud.
> - Read the installed `telos-cli` skill to get started.

## Specifications

`SPEC.md` is the main entrypoint to a `telos` program. Its frontmatter declares
the program's name, version, target platform, and imported skills. Its body
states the Goal: the observable outcome, important constraints, state that must
survive, and evidence that counts as success.

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

Skills are modular libraries for a Goal. Each skill is rooted at `SKILL.md` and
can carry reusable instructions, references, scripts, and assets. A spec can
import a skill from a relative path or by immutable registry reference.

A trailing `*` makes the skill a required evaluation rubric. The verifier must
evaluate the result against every starred skill, and the revision cannot become
Ready while any required rubric fails.

## Declarative application

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

## License

Fair Source (FSL-1.1), converting to Apache-2.0 two years after each release.

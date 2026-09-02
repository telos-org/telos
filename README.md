# telos

`telos` is a goal-oriented programming system.

## Install

### Via your coding agent

```text
Set up Telos.

- Install it with `curl -fsSL https://usetelos.ai/install.sh | sh`.
- Run `telos login` to sign in to Telos Cloud.
- Use the installed `telos-spec` skill to write a Goal contract.
- Use the installed `telos-cli` skill to plan and apply it.
```

### Via the CLI

```bash
curl -fsSL https://usetelos.ai/install.sh | sh
telos login
```

The installer supports macOS and Linux on amd64 and arm64.

`telos login` signs in to Telos Cloud and is required when `telos apply` targets
Cloud.

## Get started

> Work with your coding agent to write and iterate on your Goal spec, and have
> the agent drive `telos` for you.
>
> The CLI is optimized for the agent experience.

Invoke the conversational spec writer explicitly with `$telos-spec` in Codex or
`/telos-spec` in Claude Code. It asks only for decisions that materially change
the contract, derives sections from the product, and can create, improve, or
review `SPEC.md`. Use `telos-cli` after the contract is ready for operational
commands such as `plan` and `apply`.

The Goal specification (`SPEC.md`) is the main entry point to a `telos`
program. This example creates a persistent Cloud Goal; use an explicit context
so the plan and the apply address the same workspace.

A minimal `SPEC.md`:

```markdown
---
name: reading-list
version: 0.1.0
platform: cloud
---

# Goal

Run a public reading-list service. Books remain available when the application
restarts, and the result includes evidence of the write–restart–read sequence.
```

Skills are modular libraries imported by a spec. Load them from a local path or
pin them to an immutable registry version:

```yaml
skills:
  - path/to/postgres-skill
  - "@scope/service-readiness:1.0.0*"
```

**A trailing `*` makes a skill a required rubric.** Use starred skills for
quality, process, or subjective requirements. The revision must pass every
starred rubric in an independent evaluation before `ready`.

## Apply

`telos apply` reconciles a persistent Goal toward the desired state in
`SPEC.md`.

First, preview the spec without changing the Goal or its target state:

```bash
telos plan SPEC.md --context personal
```

After reviewing and approving the resolved action and context, apply it:

```bash
telos apply SPEC.md --context personal
```

`apply` returns a session ID when the revision is accepted for work. The work
then continues in the background.

After applying, use `telos list` to find the session and `telos describe` to
check its status. Once the public-route probe succeeds, `describe` also prints
the Service URL:

```console
$ telos list --context personal
NAME           STATUS  SESSION
reading-list   ready   sess_123

$ telos describe sess_123 --context personal
Name      reading-list
Status    ready
Session   sess_123
Revision  sha256:abc123...
Context   personal
Service   https://reading-list.example.com
```

Cloud reports `working`, `ready`, `needs_attention`, or `stopped`.
[The lifecycle](skills/telos-cli/references/lifecycle.md) is authoritative for
their revision, route-publication, and compatibility semantics.

For a service, exercise the live behavior in the spec before treating the Goal
as complete.

Follow agent updates with:

```bash
telos logs SESSION_ID --context personal
```

To update a live Goal, edit `SPEC.md`, bump its version, and apply the new
revision to the same session:

```bash
telos plan SPEC.md --session SESSION_ID --context personal
telos apply SPEC.md --session SESSION_ID --context personal
```

`telos` reconciles the existing live software toward the new desired state.

Continue with the worked [persistent Goal](skills/telos-cli/references/use-telos.md)
or read [the lifecycle](skills/telos-cli/references/lifecycle.md) to understand
sessions, revisions, states, and evidence.

## Local runs

`telos run` executes a bounded Goal in a local workspace and stops. Use it for
one piece of work that does not need the persistent lifecycle of `telos apply`.

Local runs execute through [pi](https://github.com/earendil-works/pi), the open
source coding agent Telos drives. Install pi once, then open it to authenticate
with `/login`:

```bash
npm install -g @earendil-works/pi-coding-agent
pi
```

Exit pi after authentication. [Bounded runs](skills/telos-cli/references/bounded-runs.md)
provides a complete local spec, an explicit stopping bound, and the commands to
inspect and extract the accepted workspace checkpoint.

## License

Fair Source (FSL-1.1), converting to Apache-2.0 two years after each release.

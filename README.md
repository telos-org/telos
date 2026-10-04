# telos

`telos` is a goal-oriented programming system.

## Install

### Via your coding agent

```text
Set up Telos.

- Install it with `curl -fsSL https://usetelos.ai/install.sh | sh`.
- Run `telos login` to sign in to Telos Cloud.
- Read the installed `telos-cli` skill to get started.
```

### Via the CLI

```bash
curl -fsSL https://usetelos.ai/install.sh | sh
telos login
```

The installer supports macOS and Linux on amd64 and arm64.

The default installation includes the CLI and its skill/docs. Cloud use needs
no local `telosd` or `pi`. To add local execution:

```bash
curl -fsSL https://usetelos.ai/install.sh | TELOS_INSTALL_LOCAL=1 sh
```

Cloud deployments require authentication. Use `telos login` interactively, or
supply `TELOS_AUTH_TOKEN` for agents and CI. An existing valid login or token
needs no additional login step; see
[Cloud authentication](skills/telos-cli/references/cloud.md#authenticate).

## Get started

> Work with your coding agent to write and iterate on your Goal spec, and have
> the agent drive `telos` for you.
>
> The CLI is built for the agent experience.

The Goal specification (`SPEC.md`) is the main entry point to a `telos`
program.

An example `SPEC.md`:

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

## Usage

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
check its status. Once the service is published, `describe` also prints its URL:

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

`telos apply` is the primary interface. For harness development, benchmarking,
and nested execution, `telos run` executes bounded local work. See
[Bounded runs](skills/telos-cli/references/bounded-runs.md) for setup and usage.

## Acknowledgements

Telos's agent execution is powered by [Pi](https://github.com/earendil-works/pi),
the open-source coding agent.

## License

Fair Source (FSL-1.1), converting to Apache-2.0 two years after each release.

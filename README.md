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
curl -fsSL https://usetelos.ai/install.sh | sh -s -- --with-telosd
```

`telos update` keeps your CLI, bundled skill, and any companion local runtime
on the same release. Managed Cloud runtimes are updated by Cloud automation.

Cloud deployments require authentication. Use `telos login` interactively, or
supply `TELOS_AUTH_TOKEN` for agents and CI. An existing valid login or token
needs no additional login step; see
[Sign in](skills/telos-cli/references/use-telos.md#sign-in).

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

`apply` returns a Goal ID when the revision is accepted for work. The work
then continues in the background.

After applying, use `telos list` to find the Goal and `telos describe` to
check its status. Once the service is published, `describe` also prints its URL:

```console
$ telos list --context personal
NAME           STATUS  ID
reading-list   ready   goal_123

$ telos describe goal_123 --context personal
Name      reading-list
Status    ready
Goal      goal_123
Revision  sha256:abc123...
Context   personal
Service   https://reading-list.example.com
```

Cloud reports `working`, `ready`, `needs_attention`, or `stopped`.
[Use Telos](skills/telos-cli/references/use-telos.md#watch-it-work) explains
each status.

For a service, exercise the live behavior in the spec before treating the Goal
as complete.

Follow agent updates with:

```bash
telos logs GOAL_ID --context personal
```

To update a live Goal, edit `SPEC.md`, bump its version, and apply the new
revision to the same Goal:

```bash
telos plan SPEC.md --goal GOAL_ID --context personal
telos apply SPEC.md --goal GOAL_ID --context personal
```

Goal names are unique within a context: applying a spec whose name is already
taken fails and names the Goal to update with `--goal`.

`telos` reconciles the existing live software toward the new desired state.

Continue with the worked [persistent Goal](skills/telos-cli/references/use-telos.md).

## Local runs

`telos apply` is the primary interface. For harness development, benchmarking,
and nested execution, `telos run` executes bounded local work.

## Acknowledgements

Telos's agent execution is powered by [Pi](https://github.com/earendil-works/pi),
the open-source coding agent.

## License

Fair Source (FSL-1.1), converting to Apache-2.0 two years after each release.

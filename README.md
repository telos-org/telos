# telos

τέλος [ˈte.los] — *the end, goal, or purpose*

`telos` is a goal-oriented programming system.

A `telos` program is a *goal specification*: a declarative description of a
service's desired state. `telos` agents build the service and keep reconciling
its current state toward the goal.

Read the guide at [usetelos.ai/docs](https://usetelos.ai/docs).

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

The installer supports macOS and Linux on amd64 and arm64. For agents and CI,
set `TELOS_AUTH_TOKEN` instead of logging in.

## Get started

Work with your coding agent to iterate on your goal specification and drive the
`telos` CLI, which is built for the agent experience.

The goal specification (`SPEC.md`) is the entry point to a `telos` program:

```markdown
---
name: reading-list
version: 0.1.0
---

# Goal

Run a public service for a shared reading list.

- `POST /books` adds a title.
- `GET /books` returns the current list.
- Books remain available when the application restarts.

# Acceptance

- Add a title, restart the application, and confirm that `GET /books` still
  returns it.
```

Skills are modular libraries imported by a spec. Load them from a local path or
pin them to an immutable registry version:

```yaml
skills:
  - path/to/api-guidelines
  - "@acme/service-readiness:1.0.0*"
```

**A trailing `*` makes a skill a required rubric.** Use starred skills for
quality, process, or subjective requirements. The revision must pass every
starred rubric in an independent evaluation before `ready`.

## Usage

Preview a spec, apply it, and watch `telos` work:

```bash
telos plan SPEC.md                    # preview what applying would change
telos apply SPEC.md                   # create the Goal and print its ID
telos logs goal_c7d2f0a4e8 --follow   # follow the agents' work
telos describe goal_c7d2f0a4e8        # status, service URL, and cost
```

When the service meets its spec, the Goal is `ready`:

```console
$ telos describe goal_c7d2f0a4e8
Name      reading-list
Status    ready
Goal      goal_c7d2f0a4e8
Revision  sha256:8f21c47a91ee
Model     telos/default
Thinking  medium
Context   personal
Service   https://reading-list-c7d2f0a4e8.usetelos.ai
Cloud     $1.25 (compute $1.00, storage $0.25)
Inference $3.40 (Telos)
```

To change the service, edit `SPEC.md`, bump its version, and apply it to the
same Goal. `telos` reconciles the running software toward the new desired state.

```bash
telos plan SPEC.md --goal goal_c7d2f0a4e8
telos apply SPEC.md --goal goal_c7d2f0a4e8
```

Goal names are unique within a context: applying a spec whose name is already
taken fails and names the Goal to update with `--goal`.

Deleting a Goal removes its environment and can't be undone:

```bash
telos delete goal_c7d2f0a4e8
```

[Use Telos](skills/telos-cli/references/use-telos.md) walks one Goal through
its whole lifecycle, including statuses, logs, and deletion.

## Local runs

`telos apply` is the primary interface. For harness development, benchmarking,
and nested execution, `telos run` executes bounded local work. It needs the
local runtime, which the default install leaves out:

```bash
curl -fsSL https://usetelos.ai/install.sh | sh -s -- --with-telosd
```

## Acknowledgements

Telos's agent execution is powered by [Pi](https://github.com/earendil-works/pi),
the open-source coding agent.

## License

Fair Source (FSL-1.1), converting to Apache-2.0 two years after each release.

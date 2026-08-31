---
name: telos-cli
description: Install and use the Telos CLI to apply persistent Goals or run bounded work. Use for Telos setup, SPEC.md authoring, plan/apply/run workflows, cloud login and context, session inspection, publishing or pulling packages and skills, nested child goals, and Telos troubleshooting.
metadata:
  registry: "@telos/telos-cli"
  quickstart_prompt: "assets/quickstart-prompt.txt"
  public_guide: "references/use-telos.md"
  source_repository: "https://github.com/telos-org/telos"
---

# Telos CLI

Telos turns a `SPEC.md` into either a persistent Goal or a bounded run. The
spec is the executable contract: it describes the outcome and its evidence,
while Telos implements and verifies a revision that satisfies it.

## Start with the live system

Read the repository's `AGENTS.md` and inspect the relevant code before drafting
the Goal. Then check the installed CLI:

```bash
telos --version
telos <command> --help
```

If Telos is absent, read [Install Telos](references/install.md). Cloud work also
needs an authenticated account and an explicit target:

```bash
telos login
telos config
```

Choose the lifecycle that matches the requested outcome:

| Lifecycle | Command | Use it for |
| --- | --- | --- |
| Persistent Goal | `telos apply` | Software that should retain identity and evolve across revisions. |
| Bounded run | `telos run` | One piece of work that ends at a cycle, time, or cost limit. |

Read the reference that matches the task:

- [Use Telos](references/use-telos.md) for the end-to-end workflow
- [Goals and specifications](references/goals.md) for writing `SPEC.md`
- [The Goal lifecycle](references/lifecycle.md) for states, revisions, and evidence
- [Telos Cloud](references/cloud.md) for contexts and managed deployments
- [Models and inference](references/inference.md) for Cloud and local model selection
- [Packages and skills](references/packages-and-skills.md) for the registry
- [Nested Goals](references/nested-goals.md) for bounded child work
- [Troubleshooting](references/troubleshooting.md) for failed or stalled work

## Apply a persistent Goal

Write the smallest spec that states the observable outcome, important
constraints, and evidence of success. Preview the contract before applying it:

```bash
telos plan SPEC.md
telos apply SPEC.md
```

`apply` returns a session ID as soon as Cloud accepts the revision. Follow that
session until the matching revision becomes `ready`, or until the displayed
state and reason call for a new decision:

```bash
telos describe SESSION_ID --json
telos logs SESSION_ID
```

When the Goal exposes a service, verify the live service as well as the Telos
state. A later revision uses the same session:

```bash
telos plan SPEC.md --session SESSION_ID
telos apply SPEC.md --session SESSION_ID
```

## Run bounded work

`run` executes one Goal in a local workspace and stops at its bound:

```bash
telos run SPEC.md --workspace . --until 3
```

Inside a Telos session, the same command creates a bounded child Goal. Its
session is inspected with `describe` and `logs` like any other run.

## Command effects

- `plan`, `list`, `describe`, and `logs` inspect state.
- `get` and `pull` materialize local files. `login`, `logout`, and
  `config --context` change local authentication or configuration.
- `run`, `apply`, `push`, and `delete` change execution or registry state and
  may spend resources. Their target and mutation must be part of the user's
  request.
- Package versions are immutable; changed content receives a new version.
- Completion is an observed state, not a successful submission. For managed
  Goals, it is `ready` for the current revision plus any live behavior promised
  by the spec.

## Return the result

Report the spec, target, session ID, current revision and state, and the
evidence that supports the result. Distinguish work that was planned, applied,
published, updated, or deleted.

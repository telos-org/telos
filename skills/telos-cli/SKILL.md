---
name: telos-cli
description: Install and use the Telos CLI to apply persistent Goals or run bounded work. Use for Telos setup, SPEC.md authoring, plan/apply/run workflows, Cloud login and context, session inspection, publishing or pulling packages and skills, nested child Goals, and Telos troubleshooting.
metadata:
  registry: "@telos/telos-cli"
  quickstart_prompt: "assets/quickstart-prompt.txt"
  public_guide: "references/use-telos.md"
  source_repository: "https://github.com/telos-org/telos"
---

# Telos CLI

Telos works from a `SPEC.md`: an authored contract for an observable outcome
and the evidence that proves it. `apply` gives that outcome a persistent Cloud
identity; `run` executes bounded local work. See
[The Goal lifecycle](references/lifecycle.md) for the relationship between a
Goal, its spec, revisions, session, and deployment.

## Before you act

Read the repository's `AGENTS.md`, inspect the relevant code, and check the
installed CLI before drafting the Goal:

```bash
telos --version
telos <command> --help
```

If Telos is absent, read [Install Telos](references/install.md). Cloud work also
needs an authenticated account and a confirmed context:

```bash
telos login
telos config
```

Choose the lifecycle that matches the requested outcome:

| Lifecycle | Command | Result |
| --- | --- | --- |
| Persistent Goal | `telos apply` | One Cloud session and deployment that evolve across revisions. |
| Bounded run | `telos run` | A local session that stops at its cycle, time, or cost bound. |

## Apply a persistent Goal

Before authoring a Cloud Goal, read [Telos Cloud](references/cloud.md) and
confirm that its delivery, storage, and external-service needs fit the managed
runtime.

1. Write the smallest `platform: cloud` spec that states the outcome,
   meaningful constraints, and observable acceptance evidence.
2. Choose the Cloud context explicitly and preview without changing remote
   state. Replace `CONTEXT` with `personal` or the intended `@team-handle`:

   ```bash
   telos plan SPEC.md --context CONTEXT
   ```

3. Confirm that the plan shows the intended target and context, then apply it:

   ```bash
   telos apply SPEC.md --context CONTEXT
   ```

4. Capture the session ID and revision digest from the receipt. Observe that
   session until the same revision becomes `ready`, or until its state and
   reason require a decision:

   ```bash
   telos describe SESSION_ID --context CONTEXT --json
   telos logs SESSION_ID --context CONTEXT
   ```

5. Verify the live behavior promised by the spec. Submission, a running
   process, and old green evidence are not completion of the current revision.

Revise the same Goal by editing `SPEC.md`, bumping its version, and applying to
the existing session:

```bash
telos plan SPEC.md --session SESSION_ID --context CONTEXT
telos apply SPEC.md --session SESSION_ID --context CONTEXT
```

[Use Telos](references/use-telos.md) follows this loop with one service.
[The Goal lifecycle](references/lifecycle.md) gives a bounded observation
pattern and explains every reported state.

## Run bounded work

`run` requires a `platform: local` spec and a cycle, time, or cost bound suited
to the task:

```bash
telos run REPORT_SPEC.md --workspace . --until 3
```

Read [Bounded runs](references/bounded-runs.md) for the complete local workflow.
Inside a Telos session, the same command creates a linked child session; see
[Nested Goals](references/nested-goals.md).

## Command effects

| Effect | Commands |
| --- | --- |
| Inspect state | `plan`, `list`, `describe`, `logs` |
| Materialize files or change local configuration | `get`, `pull`, `login`, `logout`, `config --context`, `config --model` |
| Start bounded local execution | `run` |
| Publish, update, spend, or stop remote work | `apply`, `push`, `delete` |

Resolve the target and context before a remote mutation. Package versions are
immutable, so changed content receives a new version.

## Return the result

Report the spec, target, context, session ID, current revision and state, and
the evidence behind the result. Distinguish work that was planned, applied,
published, updated, or deleted.

## References

- [Use Telos](references/use-telos.md) — one persistent Goal from first plan through revision
- [Write a SPEC.md](references/goals.md) — contract shape and expressive boundary
- [The Goal lifecycle](references/lifecycle.md) — identity, states, revisions, and evidence
- [Bounded runs](references/bounded-runs.md) — local work with an explicit stopping bound
- [Telos Cloud](references/cloud.md) — contexts and managed-runtime preflight
- [Models and inference](references/inference.md) — Cloud and local model selection
- [Packages and skills](references/packages-and-skills.md) — immutable registry artifacts and rubrics
- [Nested Goals](references/nested-goals.md) — bounded child work
- [Troubleshooting](references/troubleshooting.md) — symptom-led diagnosis

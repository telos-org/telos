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

Use Telos behind the user's interactive coding agent. `SPEC.md` is the Goal's
executable contract. Keep the user in control of intent and mutations; let
Telos implement and verify the current revision.

## Begin with the live system

1. Read the repository's `AGENTS.md` and inspect the relevant code.
2. Run `telos --version` and `telos <command> --help`. If Telos is absent, read
   [Install Telos](references/install.md).
3. For Cloud work, run `telos login` and confirm the target with `telos config`.
4. Decide whether the Goal is persistent (`apply`) or bounded (`run`).

Read only the reference needed for the current task:

- [Use Telos](references/use-telos.md) for the concise end-to-end workflow
- [Goals and specifications](references/goals.md)
- [The Goal lifecycle](references/lifecycle.md) for states, revisions, and evidence
- [Telos Cloud](references/cloud.md)
- [Models and inference](references/inference.md)
- [Packages and skills](references/packages-and-skills.md)
- [Nested Goals](references/nested-goals.md)
- [Troubleshooting](references/troubleshooting.md)

## Apply a persistent Goal

Draft the smallest `SPEC.md` that states the observable outcome, important
constraints, and evidence of success. Avoid prescribing ordinary implementation
choices.

Plan first:

```bash
telos plan SPEC.md
```

Review the contract with the user. After approval:

```bash
telos apply SPEC.md
```

Poll the returned session with a bounded timeout until the exact revision is
Ready:

```bash
telos describe SESSION_ID --json
```

Ready means the exact revision passed verification and is running. Once it is
Ready, collect a finite evidence snapshot with `telos logs SESSION_ID` and show
the user the result. Stop polling on a terminal failure or when the timeout
expires.

## Run bounded work

Use `run` for one-off work or a child Goal that should stop at a clear limit:

```bash
telos run SPEC.md --workspace . --until 3
```

For a human it is bounded imperative work. For an agent it is a bounded
declarative subgoal. Inspect it with the same `describe` and `logs` commands.

## Guardrails

- `plan`, `list`, `describe`, and `logs` do not change the Goal or target state.
- `get` and `pull` materialize local files. `login`, `logout`, and
  `config --context` update local credentials or configuration.
- `run`, `apply`, `push`, and `delete` mutate execution or registry state and
  may spend resources. Confirm the target and authorization first.
- Never guess a session, organization, package version, or deployment target.
- Package versions are immutable. Publish a new version for changed bytes.
- Do not report completion until the observed session state confirms acceptance
  of the exact current revision. For managed Goals, require `ready` and probe
  public behavior when the contract exposes it.
- Do not expose tokens, runtime allocation IDs, provider details, or other
  control-plane internals in user-facing artifacts.
- Do not pipe and execute the installer when the user only asked for advice.
  Show the command; run it when installation is part of the authorized task.
- Launch the fewest child Goals needed and inspect their results before
  creating more. Avoid recursive fan-out.
- Give child work an observable deliverable and a finite cycle, time, or cost
  bound. The parent integrates and verifies what a child returns.

## Handoff

Report the spec, target, session ID, commands run, observed state, and evidence.
Say plainly what was planned, published, launched, updated, or deleted.

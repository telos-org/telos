---
name: telos-cli
description: Install and use the Telos CLI to apply persistent Goals or run bounded work. Use for Telos setup, SPEC.md authoring, plan/apply/run workflows, cloud login and context, session inspection, publishing or pulling packages and skills, nested child goals, and Telos troubleshooting.
metadata:
  registry: "@telos/telos-cli"
  quickstart_prompt: "assets/quickstart-prompt.txt"
  source_repository: "https://github.com/telos-org/telos"
---

# Telos CLI

Use Telos as the non-interactive execution layer behind the user's interactive
coding agent. The Goal is the durable outcome; `SPEC.md` is its executable
contract. Keep the human in control of product intent and consequential
mutations; let Telos implement and independently verify the current revision.

## Start from the live system

1. Read the repository's `AGENTS.md` and inspect the relevant code.
2. Run `telos --version`. If Telos is absent, follow
   [references/install.md](references/install.md).
3. Use `telos <command> --help` for exact current flags. This skill owns
   workflows and judgment, not a duplicate flag reference.
4. Determine whether the outcome is bounded work or a durable reconciled
   service, and whether it belongs on `platform: local` or `platform: cloud`.

Read only the reference needed for the current task:

- [references/goals.md](references/goals.md) for writing a spec and choosing
  `apply` versus `run`.
- [references/cloud.md](references/cloud.md) for managed deployments, login,
  context, updates, and status.
- [references/packages-and-skills.md](references/packages-and-skills.md) for
  publishing and consuming reusable artifacts.
- [references/nested-goals.md](references/nested-goals.md) when a running Telos
  agent should delegate bounded child work.
- [references/troubleshooting.md](references/troubleshooting.md) when setup,
  launch, or reconciliation fails.

## Default workflow

Draft the smallest `SPEC.md` that states the observable outcome, important
preservation constraints, and evidence of success. Do not prescribe ordinary
implementation choices a capable coding agent can make.

Validate before mutation:

```bash
telos plan SPEC.md
```

For a persistent Goal in Telos Cloud:

```bash
telos apply SPEC.md
```

Use `telos run` as the bounded execution subsystem for one-off work or child
goals that should stop at a clear limit:

```bash
telos run SPEC.md --workspace . --until 3
```

Use the session ID returned by Telos. Observe the real session instead of
inferring success from command exit:

```bash
telos describe SESSION_ID
telos logs SESSION_ID
```

Use `telos logs -f SESSION_ID` only when live following is useful. Prefer JSON
output when another agent must parse the result.

## Mutation rules

- `plan`, `list`, `describe`, `logs`, and `get` are inspection operations.
- `run`, `apply`, `push`, and `delete` mutate state or spend resources. Confirm
  that the user's request authorizes the action and target.
- Never guess a session, organization, package version, or deployment target.
- Preserve immutable package versions. Change content by publishing a new
  version, then plan and apply that version deliberately.
- Do not report completion until the implementation agent finishes and the
  verifier accepts the exact current revision. For managed Goals, also require
  the Cloud deployment to report `ready` and present the verifier evidence.
- Do not expose tokens, runtime allocation IDs, provider details, or other
  control-plane internals in user-facing artifacts.

## Handoff

Report the spec path, target (`local` or `cloud`), session ID, commands run, and
current observed state. State clearly whether anything was merely planned,
published, launched, updated, stopped, or deleted.

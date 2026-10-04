---
name: telos-cli
description: Install and use the Telos CLI to apply persistent Goals or run bounded work. Use for Telos setup, SPEC.md authoring, plan/apply/run workflows, Cloud authentication and context, unattended agents and CI, session inspection, publishing or pulling packages and skills, nested child Goals, and Telos troubleshooting.
metadata:
  registry: "@telos/telos-cli"
  public_guide: "references/use-telos.md"
  source_repository: "https://github.com/telos-org/telos"
---

# Telos CLI

This skill bundle is the canonical Telos CLI documentation for users and agents.
It ships with every release and supplies the guide at `usetelos.ai/docs`.

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

Use `telos apply` as the primary interface. Use `telos run` primarily for
harness development, benchmarking, and bounded child work inside a Telos
session. Choose the lifecycle that matches the requested outcome:

| Lifecycle | Command | Result |
| --- | --- | --- |
| Persistent Goal | `telos apply` | One Cloud session and deployment that evolve across revisions. |
| Bounded run | `telos run` | A local session that stops at its cycle, time, or cost bound. |

If the CLI is missing or an update is requested, read
[Install Telos](references/install.md). The default installation needs no local
daemon for Cloud work. Request `TELOS_INSTALL_LOCAL=1` only when local execution
is needed. Managed runtime releases belong to Cloud automation, independently
of workstation installs. For model selection or `--thinking`,
read [Models and inference](references/inference.md).

Before drafting either kind of spec, read [Write a SPEC.md](references/goals.md).
When authoring or importing skills and rubrics, also read
[Packages and skills](references/packages-and-skills.md). Prefer
`skills/<name>/SKILL.md` for new local skills; a trailing `*` on the spec's
skill reference, not a directory name, makes its rubric required.

## Authorization

Before `run`, `apply`, `push`, or `delete`, resolve and present the action and
target. Include the spec, source workspace, and bounds for a run; whether an
apply creates or updates a Goal, its context, and any existing session; the
scope and immutable version for a push; and the exact session, context, and
consequences for a delete. `run` and `apply` may spend money.

Use explicit approval already given in the conversation when it covers that
same action, target, and bounds. Otherwise, finish the spec and any available
plan before requesting the missing approval. A request to draft or inspect
does not authorize execution, publication, or deletion. Resolve targets from
the user's instructions, configuration, and receipts; ask when they remain
ambiguous.

## Apply a persistent Goal

Check Cloud authentication, context, and inference defaults without displaying
credentials:

```bash
telos config
```

Reuse a valid saved login or supplied `TELOS_AUTH_TOKEN` and run Cloud commands
directly. If authentication is missing, read
[Cloud authentication](references/cloud.md#authenticate). Use `telos login`
only when a person can approve it; an unattended job needs a valid token.

Read [Telos Cloud](references/cloud.md) to confirm that the Goal's delivery,
storage, and external-service needs fit the managed runtime.

1. Write the smallest `platform: cloud` spec that states the outcome,
   meaningful constraints, and observable acceptance evidence.
2. Choose the Cloud context explicitly and preview without changing remote
   state. Replace `CONTEXT` with `personal` or the intended `@team-handle`:

   ```bash
   telos plan SPEC.md --context CONTEXT
   ```

3. Confirm that the plan shows the intended target and context. Once the
   resolved action is authorized, apply it:

   ```bash
   telos apply SPEC.md --context CONTEXT
   ```

4. Capture the session ID and revision digest from the receipt. Poll
   `describe --json` about every 15 seconds, comparing `package_digest` with
   that digest. Use a 30-minute observation deadline unless the task calls for
   another bound. Stop at `needs_attention`, `stopped`, a changed digest, or
   the deadline and report the last state and reason. At `ready`, a public
   service also needs a non-empty `service_url` before external verification:

   ```bash
   telos describe SESSION_ID --context CONTEXT --json
   telos logs SESSION_ID --context CONTEXT
   ```

5. Verify the live behavior promised by the spec. Submission, a running
   process, and old green evidence are not completion of the current revision.
   An observation deadline ends monitoring; it does not stop or delete the Goal.

Revise the same Goal by editing `SPEC.md`, bumping its version, and applying to
the existing session:

```bash
telos plan SPEC.md --session SESSION_ID --context CONTEXT
telos apply SPEC.md --session SESSION_ID --context CONTEXT
```

Updates retain the session's inference settings.

[Use Telos](references/use-telos.md) follows this loop with one service.
[The Goal lifecycle](references/lifecycle.md) explains the reported states,
revision evidence, and deletion semantics.

### If an update is rejected

If an inherited `TELOS_MODEL` or `TELOS_THINKING` makes an update fail, omit
those overrides as described in [Models and inference](references/inference.md)
while keeping the same session.

If the missing-snapshot gate rejects an update, explain that continuing loses
the ability to restore the current revision's exact workspace and runtime
state. Obtain explicit approval for that loss before retrying the same Cloud
session with `--force`:

```bash
telos apply SPEC.md --session SESSION_ID --context CONTEXT --force
```

`--force` is only valid for an existing Cloud session update. It bypasses this
snapshot gate only; it does not bypass authorization, active operations,
runtime availability, or stale-revision protection.

## Run bounded work

For a top-level local run, read [Bounded runs](references/bounded-runs.md).
Use a `platform: local` spec and choose a cycle, time, or cost bound suited to
the task. Check local `pi` authentication and the source workspace first: a Git
source must be clean, including a newly written spec. Confirm that the local
`telosd` runtime is installed, using the local installation option above when
needed. Keep the spec outside that checkout or include it in the intended source
commit. Do not discard or
silently commit the user's work to satisfy the cleanliness requirement.

Once the source and bounds are authorized, start the run:

```bash
telos run REPORT_SPEC.md --workspace . --until 3
```

Capture the session ID, inspect its status and evidence, and retrieve the
checkpoint described in [Bounded runs](references/bounded-runs.md). The result
lives in an isolated workspace; do not report that the source checkout was
updated. Reaching a bound is not evidence that acceptance passed.

Inside a Telos session, read [Nested Goals](references/nested-goals.md) and use
`run` for a linked child. A hosted child launch rejects `--workspace`; it does
not use the top-level source-checkout workflow.

## Command effects

| Effect | Commands |
| --- | --- |
| Inspect state | `config`, `plan`, `list`, `describe`, `logs` |
| Materialize files or change local configuration | `get`, `pull`, `login`, `logout`, `config --context` |
| Replace the invoked CLI executable | `update` |
| Start bounded local execution; may spend money | `run` |
| Publish or change remote state; `apply` may spend money | `apply`, `push`, `delete` |

Package versions are immutable, so changed content receives a new version.

## Return the result

Report the spec, target, context, session ID, current revision and state, and
the evidence behind the result. Distinguish work that was planned, applied,
published, updated, or deleted.

## References

- [Use Telos](references/use-telos.md) — one persistent Goal from first plan through revision
- [Write a SPEC.md](references/goals.md) — contract shape and expressive boundary
- [The Goal lifecycle](references/lifecycle.md) — identity, states, revisions, and evidence
- [Inspect logs](references/logs.md) — progress, history, and detailed evidence
- [Glossary](references/glossary.md) — canonical Telos product vocabulary
- [Bounded runs](references/bounded-runs.md) — local work with an explicit stopping bound
- [Telos Cloud](references/cloud.md) — browser and token authentication, CI, contexts, and managed-runtime preflight
- [Models and inference](references/inference.md) — Cloud and local model selection
- [Packages and skills](references/packages-and-skills.md) — immutable registry artifacts and rubrics
- [Nested Goals](references/nested-goals.md) — bounded child work
- [Troubleshooting](references/troubleshooting.md) — symptom-led diagnosis

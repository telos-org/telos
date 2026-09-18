---
name: telos-cli
description: Install and use the Telos CLI to apply persistent Goals or run bounded work. Use for Telos setup, SPEC.md authoring, plan/apply/run workflows, Cloud authentication and context, unattended agents and CI, session inspection, publishing or pulling packages and skills, nested child Goals, and Telos troubleshooting.
metadata:
  registry: "@telos/telos-cli"
  public_guide: "references/use-telos.md"
  source_repository: "https://github.com/telos-org/telos"
---

# Telos CLI

Use this skill to operate Telos on the user's behalf. Its linked references
are the customer-facing documentation and ship with the same CLI release.

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

For installation or a requested CLI update, read
[Install Telos](references/install.md). `telos update [VERSION]` replaces only
the CLI, not `telosd`, installed skills, or deployed runtimes. For Cloud work,
check authentication and context without displaying credentials:

```bash
telos config
```

Reuse a valid saved login or a supplied `TELOS_AUTH_TOKEN`. Telos already
supports non-interactive authentication for agents and CI through this
environment variable; `TELOS_TOKEN` is not a supported alias. Set the intended
context with `TELOS_CONTEXT` or a command's `--context` flag.

Run Cloud commands directly when a token is supplied. `telos login` checks
saved credentials and may start browser approval even when `TELOS_AUTH_TOKEN`
is set. Use it when credentials are needed and a person can approve the login.
For an unattended job with missing or rejected credentials, report that it
needs a valid token instead of starting a browser login. Read
[Cloud authentication](references/cloud.md#authenticate) for token setup,
environment precedence, and a CI example.

Choose the lifecycle that matches the requested outcome:

| Lifecycle | Command | Result |
| --- | --- | --- |
| Persistent Goal | `telos apply` | One Cloud session and deployment that evolve across revisions. |
| Bounded run | `telos run` | A local session that stops at its cycle, time, or cost bound. |

For model selection and `--thinking` on `apply` or `run`, read
[Models and inference](references/inference.md).

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

Updates retain the session's inference settings. If an inherited `TELOS_MODEL`
or `TELOS_THINKING` makes an update fail, use the guidance in
[Models and inference](references/inference.md) to omit those overrides while
keeping the same session.

A healthy revision may still be waiting for its restorable snapshot. If that
snapshot gate rejects the update, do not bypass it silently. Tell the user:

> The current revision has not been snapshotted.
>
> Deploying now means you won’t be able to restore its exact workspace and
> runtime state.

Obtain explicit approval for that loss, then retry the same Cloud session
update with `--force`:

```bash
telos apply SPEC.md --session SESSION_ID --context CONTEXT --force
```

`--force` is only valid for an existing Cloud session update. It bypasses this
snapshot gate only; it does not bypass authorization, active operations,
runtime availability, or stale-revision protection.

[Use Telos](references/use-telos.md) follows this loop with one service.
[The Goal lifecycle](references/lifecycle.md) explains the reported states,
revision evidence, and deletion semantics.

## Run bounded work

For a top-level local run, read [Bounded runs](references/bounded-runs.md).
Use a `platform: local` spec and choose a cycle, time, or cost bound suited to
the task. Check local `pi` authentication and the source workspace first: a Git
source must be clean, including a newly written spec. Keep the spec outside
that checkout or include it in the intended source commit. Do not discard or
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
| Inspect state | `plan`, `list`, `describe`, `logs` |
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
- [Glossary](references/glossary.md) — canonical Telos product vocabulary
- [Bounded runs](references/bounded-runs.md) — local work with an explicit stopping bound
- [Telos Cloud](references/cloud.md) — browser and token authentication, CI, contexts, and managed-runtime preflight
- [Models and inference](references/inference.md) — Cloud and local model selection
- [Packages and skills](references/packages-and-skills.md) — immutable registry artifacts and rubrics
- [Nested Goals](references/nested-goals.md) — bounded child work
- [Troubleshooting](references/troubleshooting.md) — symptom-led diagnosis

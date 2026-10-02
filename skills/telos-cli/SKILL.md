---
name: telos-cli
description: Install and use the Telos CLI to apply persistent Goals or run bounded work. Use for Telos setup, SPEC.md authoring, plan/apply/run workflows, Cloud authentication and context, unattended agents and CI, session inspection, publishing or pulling packages and skills, nested child Goals, and Telos troubleshooting.
metadata:
  registry: "@telos/telos-cli"
  public_guide: "references/use-telos.md"
  source_repository: "https://github.com/telos-org/telos"
---

# Telos CLI

This skill is the public operating contract for Telos. Its linked references
form the user-facing documentation surface.

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

Use `telos apply` as the primary interface. Use `telos run` primarily for
harness development, benchmarking, and bounded child work inside a Telos
session. Choose the lifecycle that matches the requested outcome:

| Lifecycle | Command | Result |
| --- | --- | --- |
| Persistent Goal | `telos apply` | One Cloud session and deployment that evolve across revisions. |
| Bounded run | `telos run` | A local session that stops at its cycle, time, or cost bound. |

For model selection and `--thinking` on `apply` or `run`, read
[Models and inference](references/inference.md).

For Cloud, use `telos config` to inspect saved connections and the workspace
default. Select a saved subscription or API-key connection with the existing
`--model CONNECTION/MODEL` form. Manage connections, available models, and
shared defaults under **Inference** in the Telos app.

## Authorization

Before `run`, `apply`, `push`, or `delete`, use the user's existing authorization when it covers the resolved action and target. Otherwise, present them and obtain approval. Include the spec and workspace for a
run, the session and context for an apply or delete, and the scope and package
version for a push. `run` and `apply` may spend money. Never infer a session,
context, scope, package version, or destructive target.

## Apply a persistent Goal

Before drafting a spec, read [Write a SPEC.md](references/goals.md). When
authoring or importing skills and rubrics, also read
[Packages and skills](references/packages-and-skills.md). Prefer
`skills/<name>/SKILL.md` for new local skills; a trailing `*` on the spec's
skill reference, not a directory name, makes its rubric required.

Before authoring a Cloud Goal, read [Telos Cloud](references/cloud.md) and
confirm that its delivery, storage, and external-service needs fit the managed
runtime.

1. Write the smallest `platform: cloud` spec that states the outcome,
   meaningful constraints, and observable acceptance evidence.
2. Choose the Cloud context explicitly and create a saved proposal. Replace
   `CONTEXT` with `personal` or the intended `@team-handle`:

   ```bash
   telos plan SPEC.md --context CONTEXT --out=change.plan --json --message "Record book ownership"
   ```

   Cloud planning uploads private Registry artifacts. With `--out`, it saves a
   Change Request even when the deployment does not require one; it does not
   deploy. Without `--out`, the plan is preview-only and cannot be applied.
   Local plans remain local and reject `--out`.
   Newly uploaded proposals stay private for all roles and are hidden from
   Library lists, even after applying. Keep explicit Registry publishing separate
   from deployment approval; use `telos push` only when publishing is authorized.

3. Present the proposal's context, request ID, and dashboard URL. On a
   protected deployment, only an owner or admin can apply it. For an unprotected
   deployment, an authorized editor can apply; any workspace member can create
   a new deployment. If someone else must
   review it, return the link and stop. Do not escalate to another credential.
   When the user has authorized applying this exact proposal and your credential
   has Apply permission, confirm it without another terminal prompt:

   ```bash
   telos apply change.plan --context CONTEXT --json
   ```

   Alternatively, for an authorized fresh spec, use `telos apply SPEC.md --message "Record book ownership" --yes
   --json --context CONTEXT`. This prepares a fresh plan and confirms automatically.
   Every request prepares its comparison immediately; only execution takes turns. `-y` is shorthand for `--yes`. Never infer permission
   from the lack of a terminal. Fresh Cloud apply without `--yes` requires an
   interactive terminal and rejects `--json` before uploads or request creation.
   Do not pipe `yes` to work around that check.

4. Inspect `change_request.status`. Unconfirmed requests return `operation: "requested"`
   and await confirmation; do not treat the current deployment's `ready` state
   as success of that proposal. Applied/confirmed/applying receipts identify
   authorized work, not completed verification. Capture the session ID and
   resulting revision/digest and observe that same revision:

   ```bash
   telos describe SESSION_ID --context CONTEXT --json
   telos logs SESSION_ID --context CONTEXT
   ```

5. Verify the live behavior promised by the spec. Submission, a running
   process, and old green evidence are not completion of the current revision.

Revise the same Goal by editing `SPEC.md` and applying to the existing session.
Cloud prepares an unused spec version automatically before review, using the
same rules for CLI and Web edits. Do not require a manual version bump for Cloud
changes. Inspect the returned spec diff and package digest: Cloud may have
prepared a different package from the upload, while the local `SPEC.md` stays
unchanged:

```bash
telos plan SPEC.md --session SESSION_ID --context CONTEXT
telos apply SPEC.md --message "Record book ownership" --session SESSION_ID --context CONTEXT --yes --json
```

Explicit Registry publishing with `telos push` still requires an unused version
for changed content.

A healthy revision may still be waiting for its restorable snapshot. If it is
missing, Cloud leaves the proposal pending and unconfirmed. The CLI exits with
an error and its dashboard link, including with `--yes`; it does not keep polling
or automatically apply when the snapshot becomes ready. The dashboard's Wait
action leaves it pending until someone confirms again. Prefer waiting for the
snapshot. If the user wants to bypass it, explain:

> The current revision has not been snapshotted.
>
> Deploying now means you won’t be able to restore its exact workspace and
> runtime state.

Use explicit authorization for that loss. The dashboard's Apply Now action can
allow the snapshot bypass for the waiting proposal. Alternatively, create a fresh
proposal with `--force`. Unconfirmed requests do not reserve an execution turn.
A local saved file cannot override its flags:

```bash
telos apply SPEC.md --message "Record book ownership" --session SESSION_ID --context CONTEXT --force --yes --json
```

`--force` is only valid for an existing Cloud session update. It bypasses this
snapshot gate only; it does not bypass authorization, active operations,
runtime availability, confirmation requirements, or stale-revision protection.

Plans freeze the finalized spec, package digest, skill locks, and baseline.
Every Change Request prepares immediately and can be reviewed independently.
Only execution is serialized. A different deployment revision makes a plan
stale; the request stays open with its URL and discussion. Unprotected fresh
apply creates a direct plan without a Change Request. Neither an unconfirmed
request nor a direct plan reserves an execution turn. Plans do not expire.

Requests may be edited or reconciled in Web while unconfirmed. Every update
creates a new immutable plan; old saved files never silently identify it.
Review the new full-package plan and obtain fresh apply authorization before
confirmation. A stale request stays open with its URL and discussion. Its author
can reconcile it with the current deployment in Web, including explicit conflict
resolution. Never treat a clean text merge as proof of correct combined behavior.

With fresh `apply --yes`, the CLI first prepares a plan and then explicitly
confirms only that exact plan. It never stores permission for future request
updates. Saved references pin both the request update and prepared plan ID;
replaced or stale plans fail without applying different contents. Older v1 files
remain valid only while their original proposal is unchanged.

Wait for an existing deployment's runtime provisioning to finish before submitting
or applying changes. Cloud rejects these mutations while provisioning; `--force`
does not bypass that guard. Read-only previews, status inspection, and deletion
remain available. Do not treat an agent's ongoing work as runtime provisioning.
A saved file is only a reference and grants no access. Check its context/API
binding; never change a file's endpoint to redirect a credential.

When saving a plan with `plan --out` or running fresh Cloud `apply`, supply
`--message` (or `-m`) with a concise description of the intended change. Messages
must be nonblank, single-line, and at most 200 Unicode characters. `--yes` and
`--json` do not waive this requirement. Preview-only plans may omit a message;
applying a saved plan retains its original message and rejects overrides.

New deployments are unprotected. Owners/admins can enable Require change requests
in a deployment's Settings. Protected updates, redeploys, and restores require
owner/admin confirmation; authorized users can confirm their own requests.
Unprotected direct writes use the ordinary deployment pipeline without Change
Requests; `plan --out` deliberately saves an optional Change Request. The Web
also offers optional request submission. Turning protection off preserves
pending requests. It does not apply or discard them. CLI fresh apply still requires its own confirmation, or explicit
`--yes`. Do not turn off protection to bypass a review. Cloud checks current
permissions when applying. A direct plan prepared before protection was enabled
requires a fresh request; saved Change Requests remain subject to the new policy.
A stale baseline requires a fresh plan. Return the dashboard link after a
missing-snapshot error so the user can confirm later or choose Apply Now.
Read [Change Requests](references/change-requests.md) for the full contract,
concurrent execution, reconciliation, cancellation, and immutable file format.

[Use Telos](references/use-telos.md) follows this loop with one service.
[The Goal lifecycle](references/lifecycle.md) gives a bounded observation
pattern and explains every reported state.

## Run bounded work

`run` requires a `platform: local` spec and a cycle, time, or cost bound suited
to the task. Resolve the source workspace and bounds, then obtain user approval
before starting it:

```bash
telos run REPORT_SPEC.md --workspace . --until 3
```

Read [Bounded runs](references/bounded-runs.md) for the complete local workflow.
Inside a Telos session, the same command creates a linked child session; see
[Nested Goals](references/nested-goals.md).

## Command effects

| Effect | Commands |
| --- | --- |
| Inspect state | `config`, local `plan`, `list`, `describe`, `logs` |
| Upload private artifacts and record a preview or saved proposal without deploying | Cloud `plan`, `plan --out=FILE` |
| Materialize files or change local configuration | `get`, `pull`, `login`, `logout`, `config --context` |
| Start bounded local execution; may spend money | `run` |
| Publish or change remote state; `apply` may spend money | `apply`, `push`, `delete` |

Package versions are immutable, so changed content receives a new version.

## Return the result

Report the spec, target, context, session ID, current revision and state, and
the evidence behind the result. Distinguish work that was planned, applied,
published, requested, updated, or deleted. The `requested` receipt operation
identifies an unconfirmed Change Request; confirmed, applying, and applied
receipts report its later execution state, regardless of protection. Direct
preview/apply receipts use `plan` and `preview_url`. A preview reports
`operation: preview`; an unconfirmed direct apply reports `operation: planned`.
Direct applies include `deployment_url` after execution starts. They contain no
`change_request`. Older Cloud releases may also return a direct saved plan;
preserve its `plan_id` reference and use the returned routes. Verification is a
separate result.

## References

- [Use Telos](references/use-telos.md) — one persistent Goal from first plan through revision
- [Write a SPEC.md](references/goals.md) — contract shape and expressive boundary
- [The Goal lifecycle](references/lifecycle.md) — identity, states, revisions, and evidence
- [Change Requests](references/change-requests.md) — review, reconciliation, exact confirmation, and request receipts
- [Glossary](references/glossary.md) — canonical Telos product vocabulary
- [Bounded runs](references/bounded-runs.md) — local work with an explicit stopping bound
- [Telos Cloud](references/cloud.md) — browser and token authentication, CI, contexts, and managed-runtime preflight
- [Models and inference](references/inference.md) — Cloud and local model selection
- [Packages and skills](references/packages-and-skills.md) — immutable registry artifacts and rubrics
- [Nested Goals](references/nested-goals.md) — bounded child work
- [Troubleshooting](references/troubleshooting.md) — symptom-led diagnosis

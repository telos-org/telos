---
title: Change Requests
description: Preview changes, save exact proposals, and choose which deployments require review.
group: Concepts
---

# Change Requests

Cloud plans compare your proposed spec and pinned skills with the current
revision. You see the changes in the terminal and get a dashboard link. Applying
starts the agent working toward that spec; successful verification comes later.

## Choose which deployments require review

New deployments start **unprotected**. Any workspace member can create one, and
the creator can edit it. An authorized editor can update, redeploy, or restore an
unprotected deployment directly. These direct actions create no Change Requests.
You can still choose to submit a Change Request from the Web or save one with
`telos plan --out=FILE`. The Change Requests tab remains available in either mode.

An owner or admin can turn on **Require change requests** in a deployment's
**Settings**. After that, updates, redeploys, and restores go through Change
Requests. Members can propose changes; an owner or admin must confirm them.
You can confirm your own request if you have Apply permission. There is no
required number of independent reviewers.

Existing deployments keep their protection when this feature rolls out. You can
change the setting with pending requests: they remain open and independently
reviewable. Wait for an apply already executing to finish before
changing the setting. Switching protection off never applies or discards a waiting
proposal. Cloud checks the current setting and your permissions when applying.
A direct plan prepared before protection was enabled cannot bypass the new
setting: create a fresh plan to submit a Change Request.

## Preview without applying

```bash
telos plan SPEC.md --session SESSION_ID --context @team-handle
```

This prints the proposed changes and a preview dashboard link. It cannot be
applied, even by an owner, and does not appear in the Change Requests list.
Omit `--session` to preview creating a new deployment. You must be signed into
the dashboard with access to the workspace to open the link.

Planning uploads private, content-addressed spec and skill artifacts. It does
not publish a reusable Library release. The files stay private after application
and remain available through the plan or deployment History. Use
[`telos push`](packages-and-skills.md#publish) separately to publish a release.
You can also plan an existing package, such as `@scope/package-name:0.1.0`.

## Versions in Cloud plans

You do not need to bump the version in `SPEC.md` for Cloud edits. Cloud prepares
an unused spec version for changed content before the plan is ready to review.
CLI and Web submissions follow the same rule. The terminal and dashboard show
the final version and spec diff; the CLI leaves your local `SPEC.md` unchanged.

Every request prepares its version and comparison immediately. Opening a request
does not reserve the deployment or block other reviews. Its plan freezes the
version, package, skill locks, and starting revision. Cloud never changes an
approved plan's inputs. If the deployment changes, reconcile the request and
review its new plan before applying.

Named Registry releases remain immutable. When a deployment needs a different
version, Cloud can derive a private proposal from a published package without
changing the original release. Publishing changed content with `telos push`
still requires a new Registry version.

## Save an exact proposal

```bash
telos plan SPEC.md --session SESSION_ID --context @team-handle --out=change.plan --message "Record book ownership"
```

The command shows the comparison and dashboard link, saves a small local JSON
reference, then exits without deploying. Using `--out` opens a Change Request,
even when requests are optional. There is one kind of request; the local file
identifies one exact plan within it, rather than a separate saved-request type.

- **Unprotected deployment:** the request appears in the Change Requests tab.
  An authorized editor can confirm it from the dashboard or CLI.
- **Protected deployment:** it saves a Change Request in the deployment's
  Change Requests tab. Share its link with an owner or admin to confirm.
- **New deployment:** it saves a request. No deployment starts until a workspace
  member applies it.

The filename and extension are your choice. The reference file looks like:

```json
{
  "version": 2,
  "change_request_id": "cr_example",
  "update_number": 1,
  "prepared_plan_id": "cp_example",
  "deployment_id": "sess_example",
  "context": "@team-handle",
  "org_id": "org_team",
  "api_endpoint": "https://api.usetelos.ai"
}
```

Older saved files may contain `plan_id` instead of `change_request_id`; the CLI
continues to support those direct plans while current permissions and protection
allow them. `version` identifies the reference format; the frozen inputs live in
Cloud. The file contains no credentials and grants no permission. The CLI refuses to overwrite an existing
file. Deleting it does not discard the remote proposal.

## Correct an existing request

You can keep the same request and discussion when a reviewer asks for a correction:

```bash
telos plan SPEC.md --request cr_42 --context @team-handle
```

This submits a new immutable update to `cr_42`. The request keeps its URL and
comments; the previous update and its prepared plan remain inspectable in Web.
Omit `--message` to keep its title, or supply a new message. You must be the
author or a deployment manager, and the request must still be unconfirmed.
Restore and redeploy requests keep their original source and cannot be edited.

For any request, you can also write a new exact saved reference:

```bash
telos plan SPEC.md --request cr_42 --context @team-handle --out=corrected.plan
telos apply corrected.plan --context @team-handle
```

An update prepares a new immutable plan immediately. It clears previous
confirmation and snapshot-bypass choices. You must review and confirm the new
plan before it runs. A request waiting for review never holds an execution turn.

Without `--request`, `plan` remains a preview, and `plan --out` opens a new
request. `--request` cannot be combined with `--session`, `--model`, `--thinking`,
or `--force`. It uses the deployment and creation settings already in the request.

In Web, use **Edit**, **Update request**, and **Add comment**. Comments
show the update they refer to and never approve or close the request. Once
confirmed or applying, its contents are fixed. **Discard** closes an abandoned
request. An update number such as Update 2 is separate from the spec's version.

Saved files identify one exact update and prepared plan. An old file fails if
that plan was replaced, even if the deployment has not changed. A plan also
becomes stale when another apply changes the deployment. The request stays open:
use the reconciliation workflow below to combine your proposal with the current
deployment and prepare a new plan. An ordinary `plan SPEC.md --request` update
refuses a stale request before uploading. Older v1 files work only while their
original plan remains unchanged.

## Reconcile changes from the current deployment

When another request applies first, reconcile your existing proposal instead of
replacing the deployment with an older copy of its package:

```bash
telos plan --request cr_42 --reconcile ./cr-42-merge --context @team-handle --json
```

Use a new directory. Telos writes `base/` (the proposal's starting package),
`current/` (the current deployment), `proposed/` (your request), and `merged/`
(the candidate result). The workspace also contains `merge.json`, which pins
the request update, deployment revision, API endpoint, and context. Preparation
changes no request and applies nothing.

A clean merge returns `operation: "merge_prepared"`. Conflicts return a nonzero
exit status and `error.code: "merge_conflicts"`, with structured conflict IDs,
paths, kinds, and all three versions. The files and manifest are still saved.
A text conflict contains familiar conflict markers in `merged/`.

For each entry in `merge.json`'s `resolutions`, set `choice` to:

- `base`, `current`, or `proposed` to use that exact file version.
- `merged` to use your edited file under `merged/`, or delete that file to
  resolve it as a deletion.

Removing markers alone does not mark a conflict resolved. Every conflict needs
an explicit choice. Binary conflicts require an existing source version; custom
binary edits are not supported. Files under `.telos/skills/` describe whether a
skill is required; they are merge metadata, not files published inside a skill.

Review the merged package, then save it as a new plan on the same request:

```bash
telos plan --request cr_42 --resolve ./cr-42-merge --context @team-handle --out=updated.plan --json
```

This creates a new immutable update and prints its final preview. It never
applies or carries forward an old confirmation. Review the diff before an
authorized person runs `telos apply updated.plan` or confirms in Web. A clean
text merge can still need corrections to make the combined behavior sensible.

If the deployment or request changed again, Cloud returns `stale_merge` and
keeps your local files untouched. Prepare a new workspace and transfer your
edits after comparing the new inputs. `invalid_resolution` identifies an invalid
choice, file, or package; `merge_conflicts` identifies unresolved conflicts.
With `--json`, each error has a machine-readable `error.code` and a nonzero exit
status. Network or preparation failures never claim a clean merge.

`--reconcile` and `--resolve` require `--request` and take no positional spec.
They cannot be combined with each other, `--session`, `--model`, `--thinking`, or
`--force`. Only `--resolve` accepts `--message` and `--out`. Neither command runs
an apply, and the local saved-plan reference format remains version 2.

## Apply a saved proposal

```bash
telos apply change.plan --context @team-handle
```

This command supplies confirmation; there is no extra prompt. It applies the
saved inputs, excluding edits you made after saving. Flags such as `--session`,
`--model`, `--force`, and `--message` cannot override them. Cloud checks your
current permissions, deployment protection, and the proposal's starting revision.
Repeated application cannot create another deployment or revision.

The request page has **Confirm & Apply** when you have permission. Older direct
saved plans have **Apply** on their plan page. Preview-only pages have neither
action. After application, follow the deployment link to see live progress and History.

## Plan and apply together

```bash
telos apply SPEC.md --message "Record book ownership" --session SESSION_ID --context @team-handle
```

For an unprotected deployment, the CLI prepares a preview, shows its dashboard
link and diff, and asks:

```text
Apply these changes? Type yes to confirm:
```

Typing `yes` applies directly. This creates no Change Request and does not
reserve the deployment while you consider the plan. A protected apply opens a
Change Request, prepares its comparison immediately, and asks the same question.
You can also confirm on the dashboard. Only execution is serialized; another
apply may make your plan stale while you review it.

Without Apply permission, fresh apply stops before uploading. Use
`plan --out=FILE` to propose a change for someone who can apply it: an authorized
editor when protection is off, or an owner or admin when it is on.
Typing another answer, EOF, or Ctrl-C attempts to discard an unconfirmed plan
or request. A lost connection may prevent cancellation; use its dashboard link
to check. Closing the terminal after confirmation does not undo the change.

## Messages, agents, and CI

Opening a request or applying a fresh spec requires `--message` (or `-m`): one nonblank line, up to
200 Unicode characters. It becomes the proposal title and deployment History
entry. Preview-only planning may omit it. Applying a saved file retains its
original message. Updating with `--request` can retain the existing message,
including when writing a replacement saved file. Web deployment forms require
the same message.

```bash
# Save without prompting or deploying.
telos plan SPEC.md --session SESSION_ID --context @team-handle --out=change.plan --json --message "Record book ownership"

# Apply the frozen inputs, when authorized.
telos apply change.plan --context @team-handle --json

# Prepare and apply a fresh plan without prompting.
telos apply SPEC.md --session SESSION_ID --context @team-handle --message "Record book ownership" --yes --json
```

`--yes` (or `-y`) confirms only the exact plan prepared by that invocation,
without granting permissions or bypassing protection. It never authorizes a
later edit or automatically reconciled replacement. Fresh Cloud apply requires it when stdin or the prompt
stream is not a terminal, or when `--json` is set. Otherwise the command fails
before uploading. `--json` keeps stdout machine-readable.

Direct-plan receipts include `plan`, `preview_url`, `session_id`, and `operation`.
Preview-only receipts report `preview`; unconfirmed direct applies report
`planned`. Execution reports `applying` or `applied` and adds `deployment_url`.
Change Request receipts use `change_request` and `review_url`;
an unconfirmed request reports `requested`. Saved-reference receipts include
`plan_file`. Neither an applied receipt nor its resulting revision means the
agent has finished verification.

The receipt's `package.ref` and `package.digest` identify Cloud's prepared
artifact, which may differ from the original upload. The proposed spec is in
`plan.preview.proposed_spec` or `change_request.preview.proposed_spec`. Use that
spec for the final deployment version; a private artifact's Registry version
is a separate content-addressed identifier.

## Concurrent changes and retries

While an existing deployment's runtime is provisioning, you can inspect it or
create a preview, but must wait before saving or applying changes, redeploying,
restoring, or changing deployment settings. You can still delete the deployment
or discard a request. `--force` cannot skip provisioning. These controls become
available once setup finishes, even while the agent is still working.

Several requests can start from the same revision and be reviewed at once.
Only one apply can execute on a deployment at a time. If another apply owns the
deployment, wait for it to finish; Cloud rechecks the exact plan and baseline
before execution. When that apply changes the deployment, the other plans become
stale while their requests, links, and comments remain open.

Reconciliation creates a new plan that needs fresh confirmation, even when its
text merges cleanly. Existing confirmations never carry across it. Requests
waiting for review do not block direct updates on an unprotected deployment.
Plans and requests have no time-based expiry.

An interrupted direct apply can be retried from the same saved file or plan
page. Cloud resumes the admitted operation without creating another revision.
If a snapshot is missing, your plan or Change Request stays pending and
unconfirmed. The CLI returns an error and the dashboard link, even with `--yes`.
On the dashboard, **Wait** leaves it pending until you confirm again. Snapshot
completion alone does not apply it. Choose **Apply Now** to proceed without a
snapshot. A fresh CLI update can use `--force` to allow the
snapshot bypass. This may leave the previous revision without a restore point;
it does not bypass permissions, active operations, protection, or stale checks.

Cloud plan/apply require a server advertising deployment plans. Local
`platform: local` plans stay local, provide no dashboard link, and reject
`--out`. Local apply retains its existing noninteractive behavior.

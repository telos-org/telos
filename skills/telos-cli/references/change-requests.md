---
title: Change Requests
description: Preview changes, save proposals for review, and confirm an exact deployment change.
group: Concepts
---

# Change Requests

Cloud plans show the proposed spec and pinned skill changes in your terminal
and provide a dashboard link. Confirmation authorizes the agent to work toward
the proposed spec. It does not approve the implementation that the agent will
build. The current revision can keep reconciling while a proposal waits.

Every organization member can plan changes for deployments they can access.
Only owners and admins can apply them. An authorized requester can confirm
their own proposal; there is no required number of independent reviewers.
Cloud enforces the same permission checks for API tokens and dashboard users.

## Preview without applying

```bash
telos plan SPEC.md --session SESSION_ID --context @team-handle
```

This creates a preview-only plan in Cloud, prints the proposed changes, and
provides a preview dashboard link. Anyone with permission to view the deployment
can inspect it. A preview cannot be applied, even by an owner. Nothing deploys.
Omit `--session` to preview creating a new deployment.

Planning uploads private, content-addressed spec and skill artifacts to the
Registry. It does not publish or overwrite a reusable package version. Existing
Registry references can also be planned directly:

```bash
telos plan @scope/package-name:0.1.0 --context @team-handle
```

## Save a proposal for review

```bash
telos plan SPEC.md --session SESSION_ID --context @team-handle --out=change.plan
```

This saves an immutable Change Request, prints its changes and dashboard link,
and writes a small JSON reference to `change.plan`. The command exits without
applying. The deployment's **Change Requests** tab contains the saved proposal.
Share its link with an owner or admin, who can confirm it on the dashboard.
The reviewer does not need the local file.

The filename and extension are your choice. An illustrative file is:

```json
{
  "version": 1,
  "change_request_id": "cr_example",
  "deployment_id": "sess_example",
  "context": "@team-handle",
  "org_id": "org_team",
  "api_endpoint": "https://api.usetelos.ai"
}
```

`version` identifies the reference file format. The exact proposal and baseline
revision live in Cloud. The file contains no token or secret values, and holding
it grants no permission. Telos refuses to overwrite an existing output file.
Removing the local file does not discard the Cloud request.

For a new deployment, `--model` and `--thinking` are frozen when you create
its plan. Saved proposals always require explicit confirmation.
For an update, `--force` records the snapshot bypass in the saved proposal.
The review output shows frozen creation settings and any snapshot bypass.

## Confirm a saved proposal

```bash
telos apply change.plan --context @team-handle
```

This command is the confirmation: it does not ask for another `yes` and does not
prepare a new plan. Telos checks your Apply permission, the selected API endpoint
and organization, and the saved request's validity. Local edits made after saving
the proposal are not included. Mutation flags such as `--session`, `--model`,
and `--force` cannot change the saved inputs.

An owner or admin can instead confirm the same request on its dashboard page.
A retry or simultaneous CLI and dashboard confirmation uses the same request
and cannot create a second revision. A failed or discarded request returns an
error rather than reporting that it applied.

## Plan and apply together

```bash
telos apply SPEC.md --session SESSION_ID --context @team-handle
```

This creates a new regular request and waits for its turn. Once it reaches the
front of the deployment's queue, Cloud prepares a plan against the then-current
revision. The CLI prints that plan and its dashboard link, then asks:

```text
Apply these changes? Type yes to confirm:
```

Type `yes`, or confirm the same request on the dashboard. Either confirms the
request. Cloud applies it through the existing deployment update pipeline when
the deployment is available. The CLI returns its
confirmed, applying, or applied status; it does not wait for the agent's
verification. A member without Apply permission is rejected before uploading
artifacts or creating this request, with guidance to use `plan --out` instead.

Typing anything other than `yes`, reaching end of input, or pressing Ctrl-C
while waiting for a turn or interactive confirmation attempts to discard the
request and release its turn. A lost
network connection can prevent cancellation; the error includes the dashboard
link so you can inspect or discard the request there. Closing the terminal after
confirmation does not undo an executing change.

## Agents and CI

`--yes` (or `-y`) confirms a fresh plan automatically when its turn arrives.
It does not grant Apply permission or bypass stale-revision checks.

```bash
# Submit a proposal for review, without prompting or deploying.
telos plan SPEC.md --session SESSION_ID --context @team-handle --out=change.plan --json

# Confirm that exact proposal, when your credentials allow applying.
telos apply change.plan --context @team-handle --json

# Prepare and confirm a new proposal, when authorized.
telos apply SPEC.md --session SESSION_ID --context @team-handle --yes --json
```

`--json` never prompts and keeps stdout machine-readable. A fresh Cloud apply
requires `--yes` when stdin or the prompt stream is not a terminal, or whenever
`--json` is set. Otherwise it fails before uploads or request creation. A terminal
check detects whether prompting is possible, not whether the caller is human.
Use a member's token when an agent should propose changes that an admin reviews.

JSON receipts include `operation`, `context`, `session_id`, `change_request`, and
`review_url`. New plans also include `package`; saved plans include `plan_file`.
The request includes its immutable preview, mode, status, and resulting
revision when one exists. A queued regular request has no prepared preview yet.

## Queues and conflicting proposals

Regular `apply SPEC.md` requests take turns. If Alice is waiting for confirmation,
Ben's regular apply waits before planning. Ben's plan uses the revision present
when his turn arrives. Waiting for confirmation holds the turn until the request
executes or is discarded. Requests do not expire, so discard an abandoned regular
apply on the dashboard to let the next request proceed.

Saved requests wait outside that queue. Alice and Ben can both save plans based
on Revision 7. If Alice applies hers, Ben's saved proposal is discarded as stale.
Ben must incorporate any desired changes into his spec and create a new request.
Telos does not merge proposals or carry an old confirmation to new content.

A saved plan may be prepared while a regular apply is waiting, but it cannot
apply while another request owns the deployment's turn. Any later deployment
revision can make that saved plan stale, including redeploy or restore. Saved
plans and previews have no time-based expiry.

## Web confirmation and deployment results

Web submissions create Change Requests, including new deployments, updates,
redeploys, and restores. An owner or admin inspects the proposal and clicks
**Confirm & Apply**. You can confirm your own request if you have Apply permission.
There is no per-deployment confirmation setting. CLI saved plans and interactive
apply retain their explicit confirmation flows; `--yes` confirms a fresh CLI
apply automatically without granting additional permissions.

`--force` only records permission to bypass the missing-snapshot gate. It does
not bypass confirmation, permissions, active operations, or baseline checks.
If an unstarted request is already waiting for a snapshot, wait for it or discard
it on the dashboard before submitting a new proposal with `--force`. A second
regular apply otherwise waits behind the first; saved inputs cannot be edited.
An applied request, successful verification, and an available snapshot are
separate results. Use `telos describe SESSION_ID --context @team-handle` and
[the Goal lifecycle](lifecycle.md) to follow the resulting revision.

Cloud plan and apply require a server advertising deployment plan support. Older
servers produce an upgrade error; the CLI does not fall back to immediate
unreviewed deployment. Local `platform: local` plans remain local, do not provide
a dashboard link, and reject `--out`. Local apply keeps its existing noninteractive
behavior; `--yes` is accepted but adds no permission or new prompt.

---
title: Change Requests
description: Queue deployment changes and optionally confirm them before execution.
group: Concepts
---

# Change Requests

On Cloud versions with Change Requests enabled, `telos apply` publishes the
immutable package and submits a request. The command returns after Cloud responds,
with `operation: "requested"` in JSON output. Check `change_request.status` to
see whether the request is waiting, applying, or already applied. The command
does not wait for the agent to finish or verify the resulting revision.

Your deployment's **Change Requests** tab contains proposals submitted through
both the CLI and dashboard. Requests are processed in order, with one request
being confirmed or executed at a time. Later submissions wait in the queue.

## Require confirmation

An organization owner can enable **Require confirmation before applying** on a
deployment's Settings page. With confirmation off, an eligible request starts
executing during `apply`. Requests behind earlier work wait for their turn.
With confirmation on, an authorized owner or admin must open the request and
choose **Confirm**. An authorized requester can confirm their own proposal;
there is no required reviewer count.

Confirming starts the saved change through the same deployment update path.
For an available runtime, update and redeploy send the new spec before the
confirmation response returns. Creation and restore retain their asynchronous
lifecycles, and the existing deployment reconciler recovers interrupted or
temporarily blocked work and advances the queue.

To require confirmation before a new deployment's first launch:

```bash
telos apply SPEC.md --context @team-handle --require-confirmation
```

The deployment identity is reserved, but no runtime or current revision exists
until the initial request executes. While it waits, `describe` shows that no
revision is deployed; `plan --session` and `pull` have no deployed revision to
read. This option requires a Cloud server with
Change Requests enabled and the server's permission to configure the setting.
It cannot be combined with `--session` or a local apply. Use the deployment's
Settings page to change an existing deployment's policy.

Disabling the setting does not automatically release a request that already
requires confirmation. Confirmation is available in the dashboard, not through
an API token or a CLI approval command.

## Submit an update

```bash
telos plan SPEC.md --session SESSION_ID --context @team-handle
telos apply SPEC.md --session SESSION_ID --context @team-handle
```

The session-aware plan shows whether confirmation is required. It previews
the currently deployed spec; the final request preview is prepared when the
request reaches the front of the queue, after earlier changes have executed.

An illustrative receipt is:

```text
requested reading-list

Request   req_42
Status    queued
Action    update
Session   sess_c7d2f0a4e8
Proposed  sha256:3211e8...
Current   rev_7
Queue     2
Context   @team-handle
Review    https://usetelos.ai/deployments/sess_c7d2f0a4e8?org=org_team&request=req_42&tab=change-requests
```

With `--json`, the receipt includes `context`, `operation`, `package`, `session`,
`change_request`, and `review_url`. The session describes the current deployment
after Cloud processes the request. An eligible update may already report
`status: "applied"` and show its new current revision. If the request is still
queued or awaiting confirmation, the current deployment's `ready` status does
not apply to the proposed package.

`telos describe SESSION_ID --context @team-handle` displays pending requests
separately from the current revision. Its JSON output includes
`pending_change_requests`. Open the receipt's review URL to inspect the request
and its result. Once applied, verify the resulting revision using
[the Goal lifecycle](lifecycle.md).

## Understand the preview and history

A request preserves its exact spec package, skill digests, and action inputs.
When its turn arrives, its preview compares those inputs with the current
revision. Queued proposals are not merged: an older proposal can remove an
earlier request's changes, and that removal appears in its preview.

If the reviewed baseline changes before execution, the request becomes
outdated. Update the proposal and submit a new request. Edited package content
needs a new registry version. Discarding a request does not delete its already
published package.

The same confirmation rule covers initial deployment, spec and skill updates,
historical revision redeploys, and snapshot restores. Restores add a revision;
they do not erase history. Local draft restoration, network settings, secret
rotation, sharing, and deletion are separate actions.

Confirmation authorizes the agent to work toward the proposed spec or perform
the recorded deployment action. It does not approve a finished implementation.
The existing revision can keep reconciling while requests wait. Applied,
verification passed, and snapshot available are separate results.

`--force` only expresses the existing snapshot bypass for an update. It does
not skip confirmation, permission checks, or stale-revision protection.

## Older Cloud servers

When Change Requests are unavailable, ordinary apply retains the existing
`created`, `updated`, or `unchanged` receipt. `--require-confirmation` fails
before publishing or creating a deployment rather than silently deploying
without the requested protection. A failure to read server capabilities is
also reported as an error.

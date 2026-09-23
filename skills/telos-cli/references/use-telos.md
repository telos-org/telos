---
title: Use Telos
description: Each Goal keeps its spec, revisions, deployment, and verification evidence together.
group: Getting started
---

# Use Telos

Telos is a goal-oriented programming system. You describe what the software
should do in `SPEC.md`; Telos assigns agents to implement, run, and verify the
current revision.

The spec is the durable source. Implementations can change as the Goal evolves,
while its session, deployment, history, and evidence remain connected.

This guide follows one small service from its first spec through a live update.
Generated IDs, digests, paths, and URLs in the transcripts are illustrative;
the command and field shapes match the current CLI.

## Sign in and choose a context

Install Telos first if needed. For first-time interactive setup, authenticate
and inspect the available Cloud contexts as shown below. If you already have
a valid saved login, proceed to `telos config`. Agents and CI can instead
supply `TELOS_AUTH_TOKEN` and skip `telos login`; see
[Cloud authentication](cloud.md#authenticate).

```console
$ telos login
Opening your browser to approve this login...
If it doesn't open, visit this link on any device: https://usetelos.ai/cli-auth?code=...
Waiting for approval...
logged in to https://api.usetelos.ai as alice@example.com

$ telos config
Config file     ~/.telos/config.yaml
Endpoint        https://api.usetelos.ai
Authentication  valid
Context         personal
Subscriptions
```

[Install Telos](install.md) covers first-time setup and PATH repair. This
walkthrough uses the personal context explicitly on every Cloud command.

## Describe the Goal

Create `SPEC.md`:

```markdown
---
name: reading-list
version: 0.1.0
platform: cloud
---

# Goal

Run a public service for a shared reading list.

- `POST /books` adds a title.
- `GET /books` returns the current list.
- Books remain available when the application restarts.

# Acceptance

- Add a title, restart the application, and confirm that `GET /books` still
  returns it.
```

The contract names the behavior that matters without choosing a framework,
database, or deployment layout. Before applying, confirm that the service fits
the [managed Cloud runtime](cloud.md). This one does: a small interpreted
service can be delivered from source, and environment-local persistent storage
can satisfy its restart requirement.

[Write a SPEC.md](goals.md) covers every supported frontmatter field and the
boundary between a desired outcome and an available platform capability.

## Preview the first revision

`plan` validates the spec and shows where it will run without changing remote
state:

```console
$ telos plan SPEC.md --context personal
Spec      reading-list
Target    cloud
Context   personal
Path      /Users/alice/reading-list/SPEC.md
Namespace ns-reading-list
Hash      799e5c31172afb26
```

Confirm the target and context before continuing. The first plan has no
deployed revision to compare, so it shows the Goal identity, namespace, and
content hash. Check the proposed Cloud change before applying it.

## Apply it

```console
$ telos apply SPEC.md --context personal
requested reading-list

Request   req_initial
Status    queued
Action    create
Session   sess_c7d2f0a4e8
Proposed  sha256:8f21c47a91ee1438e724bdb55edc81af864db782c29dfb10870e8cdb304f6e1a
Current   not deployed yet
Queue     1
Context   personal
Review    https://usetelos.ai/deployments/sess_c7d2f0a4e8?org=org_alice&request=req_initial&tab=change-requests
```

With Change Requests enabled on Cloud, `requested` means the proposal was
accepted into the queue. Open the review URL to follow it. Confirmation is off
by default, so the initial request executes automatically when its turn
arrives. To require confirmation before the first launch, add
`--require-confirmation` to the creation command.

Keep the session ID and proposed digest: the session identifies the Goal, and
the digest identifies the exact contract submitted. Once the request is
applied, `working` means Cloud is reconciling that revision. Follow progress
with the context printed in the receipt:

```bash
telos describe sess_c7d2f0a4e8 --context personal --json
telos logs sess_c7d2f0a4e8 --context personal
```

[Change Requests](change-requests.md) explains the queue, confirmation, and
JSON receipt. Older Cloud servers return an immediate `created` receipt.
[The Goal lifecycle](lifecycle.md) gives the polling interval, stopping
conditions, and evidence rules after execution starts.

## Observe `ready`

When reconciliation succeeds, `describe` reports the accepted revision. The
public route is published after its own surface probe; once that succeeds,
`describe` also includes `Service`:

```console
$ telos describe sess_c7d2f0a4e8 --context personal
Name      reading-list
Status    ready
Session   sess_c7d2f0a4e8
Revision  sha256:8f21c47a91ee1438e724bdb55edc81af864db782c29dfb10870e8cdb304f6e1a
Context   personal
Service   https://reading-list-c7d2f0a4e8.usetelos.ai
```

On current managed runtimes, this `ready` result belongs to the displayed
revision digest. The Cloud agent's evidence should include the promised
write–restart–read sequence. From outside the environment, exercise
`POST /books` and `GET /books` through the public URL and confirm the live
behavior independently.

If `ready` appears before `Service`, keep observing `describe` for route
publication within the same deadline. A public service is not externally
verifiable until that URL exists.

## Revise the same Goal

Suppose the reading list now needs attribution. Edit the same `SPEC.md`, bump
its version to `0.2.0`, and add “Every book records who added it” to the Goal.
Plan against the existing session:

```console
$ telos plan SPEC.md --session sess_c7d2f0a4e8 --context personal
Spec      reading-list
Target    cloud
Context   personal
Session   sess_c7d2f0a4e8
Current   @alice/reading-list:0.1.0
Confirm   off (queued changes apply automatically)
Path      /Users/alice/reading-list/SPEC.md
Namespace ns-reading-list
Hash      9e8d86776e85ffbc
Version   0.1.0 -> 0.2.0

--- deployed/SPEC.md
+++ proposed/SPEC.md
@@ -1,6 +1,6 @@
 ---
 name: reading-list
-version: 0.1.0
+version: 0.2.0
 platform: cloud
 ---

@@ -11,6 +11,7 @@
 - `POST /books` adds a title.
 - `GET /books` returns the current list.
 - Books remain available when the application restarts.
+- Every book records who added it.
```

The session-aware plan identifies the deployed package and displays the
contract change. Apply that new revision to the same session:

```console
$ telos apply SPEC.md --session sess_c7d2f0a4e8 --context personal
requested reading-list

Request   req_update
Status    queued
Action    update
Session   sess_c7d2f0a4e8
Proposed  sha256:3211e85fe81bd70aa74726d4ce0dc68d729d816826a21b62b18eb86074ff3317
Current   rev_initial (unchanged)
Queue     1
Context   personal
Review    https://usetelos.ai/deployments/sess_c7d2f0a4e8?org=org_alice&request=req_update&tab=change-requests
```

The current revision keeps running while the request waits. At the front of
the queue, Telos compares the proposed package with the then-current revision.
If confirmation is enabled in Settings, an authorized user must choose
**Confirm & Apply** in the dashboard. The requester can confirm their own
request if authorized.

When the request executes, the Goal, session, deployment, and history stay the
same; only the immutable revision changes. Observe the new digest through
`working` to `ready`, then exercise the updated API behavior. Older Cloud
servers return an immediate `updated` or `unchanged` receipt.

### Deploy without a restorable snapshot

If the current revision has not been snapshotted, you will get a warning saying
that deploying now means you won’t be able to restore its exact workspace and
runtime state. To continue anyway, retry the update with `--force`:

```bash
telos apply SPEC.md --session sess_c7d2f0a4e8 --context personal --force
```

This bypass applies only to the missing-snapshot gate. Active operations,
authorization, confirmation requirements, runtime availability, and
stale-revision protection still apply. A queued request may wait for an active
operation or snapshot to finish; `--force` never skips dashboard confirmation.

For another contract, continue with [Write a SPEC.md](goals.md). Use
[Bounded runs](bounded-runs.md) for local work and
[Troubleshooting](troubleshooting.md) when observed state diverges from the
contract.

## Resume later

Return through the same context and recover the session ID from `list`:

```bash
telos list --context personal
telos describe SESSION_ID --context personal
```

Continue revisions on that session so its identity and history remain joined.

## Delete the Goal

Cloud deletion is irreversible. Check the session and context, and confirm
that you want to remove the environment, application and PVC data, routes,
attachments, deployment record, and history before running:

```bash
telos delete SESSION_ID --context personal
```

Teardown continues asynchronously; subsequent inspection eventually returns
not found. [The Goal lifecycle](lifecycle.md#delete-a-goal) distinguishes this
from local deletion, which preserves session history.

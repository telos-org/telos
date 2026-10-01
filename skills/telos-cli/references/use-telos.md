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
Workspace model telos/default
Connections
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

`plan` validates your spec and saves a preview in Cloud without deploying:

```bash
telos plan SPEC.md --context personal
```

The terminal shows the proposed spec and skill changes and a dashboard link.
The initial plan compares your spec with an empty deployment. Anyone with the
appropriate access can inspect the preview, but it cannot be applied directly.
Add `--out=change.plan --message "Launch the reading list"` to save an immutable
plan in a Change Request for later confirmation, even when requests are optional.

## Apply it

```bash
telos apply SPEC.md --message "Launch the reading list" --context personal
```

For a new deployment, this displays a fresh plan and asks
`Apply these changes? Type yes to confirm:`. Type `yes` to proceed, or apply the
same plan through its dashboard link. Any workspace member can create a new
deployment, and creation starts directly without a Change Request. For authorized
noninteractive execution, use `--yes --json`.

On an existing deployment, authorized editors can apply directly unless its
Settings require Change Requests. Protected updates open a request with an
immediately prepared plan and need owner/admin confirmation. Use `plan --out` to save a request for someone
else to confirm, whether or not protection is enabled.

The receipt identifies the plan or request, session, dashboard URL, and resulting
revision when available. Applying or applied does not mean that the agent has
finished verification. Once execution starts, follow the deployment using its
session ID and selected context:

```bash
telos describe sess_c7d2f0a4e8 --context personal --json
telos logs sess_c7d2f0a4e8 --context personal
```

[Change Requests](change-requests.md) explains exact plans, concurrent requests,
permissions, deployment settings, and JSON receipts. Cloud plan and apply
require a compatible server; older servers return an upgrade error.
[The Goal lifecycle](lifecycle.md) gives the observation deadline, stopping
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
Inference Managed
Model     telos/default
Thinking  medium (requested)
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

Suppose the reading list now needs attribution. Edit the same `SPEC.md` and add
“Every book records who added it” to the Goal. You can leave its version alone:
Cloud prepares an unused version before showing the plan. Save a proposal
against the existing session:

```bash
telos plan SPEC.md --session sess_c7d2f0a4e8 --context personal --out=attribution.plan --message "Record book ownership"
```

The terminal and dashboard show the diff. For example:

```diff
 - `POST /books` adds a title.
 - `GET /books` returns the current list.
 - Books remain available when the application restarts.
+- Every book records who added it.
```

The plan preserves the final spec and version shown in the diff,
package digest, skill digests, and baseline revision. Your local `SPEC.md` stays
unchanged. Web edits use the same version preparation. Resubmitting a changed
proposal does not require a manual bump; ordinary `telos push` still requires a
new Registry version when publishing changed content.

When you are ready, confirm the exact saved proposal:

```bash
telos apply attribution.plan --context personal
```

This command needs Apply permission and asks no additional question. You can
instead confirm the request on its dashboard page. If another change has moved
the deployment to a new revision, the plan is stale while its request stays open.
Run `telos plan SPEC.md --request REQUEST_ID --context personal --out=updated.plan`
from the original local package. It combines your proposal with the current
deployment and saves a new plan when changes merge cleanly. If conflicts appear
in your files, resolve them and rerun the same command. On another computer,
first use `telos get REQUEST_ID --context personal --output ./request` to obtain
the request's exact proposal and record where your edits start.
Review that plan before applying it; earlier confirmation never carries over.

Alternatively, `telos apply SPEC.md --message "Record book ownership" --session sess_c7d2f0a4e8 --context personal`
prepares a plan and asks for confirmation. On a protected deployment, it opens
a Change Request and prepares the comparison immediately. The current revision
keeps reconciling while it waits for confirmation. The Goal, session, deployment, and history stay the same when the new
revision executes. Observe that revision through `working` to `ready`, then
exercise the updated API behavior.

### Deploy without a restorable snapshot

If the current revision's snapshot is missing, your proposal stays pending and
unconfirmed. The CLI stops with an error and a dashboard link, even with `--yes`.
On that page, **Wait** leaves it pending until you confirm again; it will not
apply automatically when the snapshot is ready. Choose **Apply Now** to proceed
without the snapshot. You can also create a fresh CLI plan with `--force`.
Applying with this bypass can leave the previous revision without
an exact workspace and runtime restore point. A pending request does not reserve
an execution turn or prevent reviewing other requests.

```bash
telos apply SPEC.md --message "Record book ownership" --session sess_c7d2f0a4e8 --context personal --force
```

This bypass applies only to the missing-snapshot gate. Active operations,
authorization, confirmation requirements, runtime availability, and
stale-revision protection still apply. An apply must wait for any executing
operation to finish; `--force` never supplies confirmation by itself.

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

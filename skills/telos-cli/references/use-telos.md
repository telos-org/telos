---
title: Use Telos
description: Get started with building on Telos.
group: Getting started
---

# Use Telos

Telos is a goal-oriented programming system. Telos treats the goal specification as the source of truth, and background agents own the software lifecycle beneath it. 


The spec is the durable source. Implementations can change as the Goal evolves,
while its session, deployment, history, and evidence remain connected.

This guide follows one small service from its first spec through a live update.
Generated IDs, digests, paths, and URLs in the transcripts are illustrative;
the command and field shapes match the current CLI.

## Installation

Install telos with

```console
curl -fsSL https://usetelos.ai/install.sh | sh
```

This will install the `telos` binary into your `$HOME/.local/bin` or `$TELOS_INSTALL_DIR`. This also packages with it the coupled `telos-cli` agent skill, installed under `$HOME/.agents/skills/`. The `telos` binary is self-contained, and you can check the installed version with:

```console
telos --version
```

If you want to update your local installation, use:

```console
telos update
```
to install the latest stable version

or

```console
telos update $VERSION
```
to install a specific release. 

## Sign in 

For first-time interactive setup, authenticate with Telos Cloud as shown below.

```console
$ telos login
Opening your browser to approve this login...
If it doesn't open, visit this link on any device: https://usetelos.ai/cli-auth?code=...
Waiting for approval...
logged in to https://api.usetelos.ai as alice@example.com
```

For non-interactive use (such as agents, CI and other automation) you can instead supply `TELOS_AUTH_TOKEN`. API tokens can be provisioned [on the web dashboard](https://usetelos.ai/account?tab=tokens).

To validate your signed-in configuration, run

```
$ telos config
Config file     ~/.telos/config.yaml
Endpoint        https://api.usetelos.ai
Authentication  valid
Context         personal
Workspace model telos/default
```

Congratulations! You are now ready to run your goals on Telos.

## An example Goal

This section serves as a walkthrough of the lifecycle of a goal specification on Telos.

Start by writing a preliminary `SPEC.md` for your service:

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

Writing good goals is important enough of a topic that we've dedicated [an entire section to it](goals.md), but at a framework level, the above example covers the minimal required items.

## Plan

Once you are happy with the goal specification, `telos plan` validates the spec and shows you a dry-run of what the spec would look like when applied.

```console
$ telos plan SPEC.md --context personal
Spec      reading-list
Target    cloud
Context   personal
Path      /Users/alice/reading-list/SPEC.md ?? -> is this really needed?
Namespace ns-reading-list ->> seems false now? 
Hash      799e5c31172afb26 --> ?? is this really needed?
```

The first plan has no deployed version to compare against, so it shows the Goalidentity, context, and content hash. 

## Apply it

Once the plan looks good, use `telos apply` to deploy the spec in the cloud environment.

```console
$ telos apply SPEC.md --context personal
created reading-list

Status    working
Session   sess_c7d2f0a4e8
Revision  sha256:8f21c47a91ee1438e724bdb55edc81af864db782c29dfb10870e8cdb304f6e1a
Inference Managed
Model     telos/default
Thinking  medium (requested)
Context   personal
Logs      telos logs --context personal sess_c7d2f0a4e8
```

The `apply` command returns immediately and launches a session in the cloud. You can monitor status (at different levels of detail and verbosity0 of your active goal at any time with: 

```bash
$ telos describe sess_c7d2f0a4e8 --context personal --json
*todo need example*
```

or

```bash
$ telos logs sess_c7d2f0a4e8 --context personal
*todo need example*
```

## Wait for readiness

When reconciliation succeeds, `describe` reports the accepted goal as `ready` and exposes a public handle.

```console  **I think too much slop output in here as well!*
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

Once ready, open the service and poke around? In this case, we'll exercise

Exercise `POST /books` and `GET /books` through the public URL and confirm the live behavior independently.

## Updating the Goal

Suppose the reading list now needs attribution. Edit the same `SPEC.md`, bump
its version to `0.2.0`, and add “Every book records who added it” to the Goal.
Plan against the existing session:

*below notes - not sure if should add explicit `--context personal`*

```console
$ telos plan SPEC.md --session sess_c7d2f0a4e8 --context personal
Spec      reading-list
Target    cloud
Context   personal
Session   sess_c7d2f0a4e8
Current   @alice/reading-list:0.1.0
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
updated reading-list ->>??? is this the wrong output / outdated?

Status    working
Session   sess_c7d2f0a4e8
Revision  sha256:3211e85fe81bd70aa74726d4ce0dc68d729d816826a21b62b18eb86074ff3317
Inference Managed
Model     telos/default
Thinking  medium (requested)
Context   personal
Service   https://reading-list-c7d2f0a4e8.usetelos.ai
Logs      telos logs --context personal sess_c7d2f0a4e8
```

The Goal, session, deployment, and history stay the same; only the immutable
revision changes. Observe the new digest through `working` to `ready`, then
exercise the updated API behavior.

If the update is rejected, see
[Troubleshooting](troubleshooting.md#cloud-update-is-rejected) before retrying.

## Resume later

Return through the same context and recover the session ID from `list`:

```bash
telos list --context personal
telos describe SESSION_ID --context personal
```

Continue revisions on that session so its identity and history remain joined.

## Delete the Goal

Cloud deletion is irreversible: it removes the environment, application and
PVC data, routes, attachments, deployment record, and history. Confirm the
session and context identify the Goal you intend to delete, then run:

```bash
telos delete SESSION_ID --context personal
```

Teardown continues asynchronously; subsequent inspection eventually returns
not found. [The Goal lifecycle](lifecycle.md#delete-a-goal) distinguishes this
from local deletion, which preserves session history.

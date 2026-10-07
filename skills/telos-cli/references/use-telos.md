---
title: Use Telos
description: Get started with building on Telos.
group: Getting started
---

# Use Telos

Telos is a goal-oriented programming system. Telos treats the goal specification as the source of truth, and background agents own the software lifecycle beneath it. 

Implementations can change as the Goal evolves,
while its session, deployment, history, and evidence remain connected.

This guide follows one small service from its first spec through a live update.
Generated IDs, digests, paths, and URLs in the transcripts are illustrative;
the command and field shapes match the current CLI.

## Installation

Install Telos with

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
telos update <version>
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

To validate your signed-in configuration, run

```
$ telos config
Config file     ~/.telos/config.yaml
Endpoint        https://api.usetelos.ai
Authentication  valid
Context         personal
Workspace model telos/default
```

Congratulations! You are now ready to run your Goals on Telos.

## An example Goal

This section serves as a walkthrough of the lifecycle of a goal specification on Telos.

Start by writing a preliminary `SPEC.md` for your service:

```markdown
---
name: reading-list
version: 0.1.0
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

Writing good Goals is important enough of a topic that we've dedicated [an entire section to it](goals.md), but at a framework level, the above example covers the minimal required items.

## Plan

Once you are happy with the goal specification, `telos plan` validates the spec and shows you a dry-run of what the spec would look like when applied.

```console
$ telos plan SPEC.md
Spec      reading-list
Target    cloud
Context   personal
Path      /Users/alice/reading-list/SPEC.md TODO(grohan): is this really needed?
Namespace ns-reading-list TODO(grohan): seems false now?
Hash      799e5c31172afb26 TODO(grohan): is this really needed?
```

The first plan has no deployed version to compare against, so it shows the Goal identity, context, and content hash. 

## Apply it

Once the plan looks good, use `telos apply` to deploy the spec in the cloud environment.

```console
$ telos apply SPEC.md
created reading-list

Status    working
Session   sess_c7d2f0a4e8
Revision  sha256:8f21c47a91ee1438e724bdb55edc81af864db782c29dfb10870e8cdb304f6e1a
Inference Managed TODO(grohan): remove
Model     telos/default
Thinking  medium (requested)
Context   personal
Logs      telos logs --context personal sess_c7d2f0a4e8
```

The `apply` command returns immediately and launches a session in the cloud.

## Watch it work

You can monitor your active Goal at different levels of detail and verbosity. To see an overview of all your active Goals:

```bash
$ telos list
TODO(grohan): need example
```

`telos list` and `telos describe` report one of four statuses. `describe` also shows the reason.

| Status | Meaning |
|---|---|
| `working` | Telos is preparing the Goal's environment, implementing the current spec, or verifying it. |
| `ready` | The system accepted the current Goal, and the service is live. |
| `needs_attention` | The latest run failed or stopped unexpectedly. `telos describe` shows why. |
| `stopped` | The Goal has stopped running, usually because you deleted it. |

To follow the agent's work in detail:

```bash
$ telos logs sess_c7d2f0a4e8
TODO(grohan): need example
```

For scripts and agents, `telos describe --json` returns the same information as JSON. Rely on its `status` field.

## Wait for readiness

When the system is done working, `describe` reports the accepted Goal as `ready` and exposes a public handle.

TODO(grohan): I think too much slop output in here as well!

```console
$ telos describe sess_c7d2f0a4e8
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

Once ready, open the service and verify that the behaviour is as desired.
In this case, you would exercise `POST /books` and `GET /books` through the public URL to confirm everything is in order.

## Iterating on the Goal

Suppose the reading list now needs attribution. Edit the same `SPEC.md`, bump its version to `0.2.0`, and add “Every book records who added it” to the Goal.

Plan against the existing session:

```console
$ telos plan SPEC.md --session sess_c7d2f0a4e8
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
 ---

 # Goal
@@ -10,6 +10,7 @@
 - `POST /books` adds a title.
 - `GET /books` returns the current list.
 - Books remain available when the application restarts.
+- Every book records who added it.
```

The session-aware plan identifies the deployed package and displays the
contract change. Apply that new revision to the same session:

```console
$ telos apply SPEC.md --session sess_c7d2f0a4e8
updated reading-list TODO(grohan): is this the wrong output / outdated?

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

This applies the update in-place, and the system begins reconciling towards the new desired Goal. Continue to monitor status from `working` to `ready`, then exercise the updated API behavior.


Suppose the spec has updated under you (by your coworker), you can fetch the deployed package with

```console
$ telos get <session-id>
```

This writes the goal spec and its skills to a directory named after the Goal. Use `--output <dir>` to choose the directory, or `--output SPEC.md` to fetch only the spec.

TODO(grohan): why do we even support `--output SPEC.md`


## Delete the Goal

If you would like to delete your Goal, its corresponding agent workspace, and the sandbox it lives in — you can run:

```console
$ telos delete sess_c7d2f0a4e8
delete requested for reading-list

Status    stopped
Session   sess_c7d2f0a4e8
Context   personal
```

> Deleting a Goal is irreversible — please proceed with caution.
> Deletion can take up to 10 minutes as background processes reconcile billing data and in-flight inference requests.

The Goal disappears from `telos list` as soon as you delete it.

## Advanced Configuration

For advanced use — such as scripting, CI, or custom development setups — you can configure various defaults.

In terms of precedence, flags override environment variables, and environment variables override your saved config (in `telos config`).

| Variable | Purpose | Default |
|---|---|---|
| `TELOS_AUTH_TOKEN` | API token provisioned [on the web](https://usetelos.ai/account?tab=tokens) for agents, CI and other non-interactive use. | Your saved login |
| `TELOS_CONTEXT` | Context for every command, like `--context`. | Your saved context |
| `TELOS_CONFIG` | Path to the config file. | `~/.telos/config.yaml` |
| `TELOS_MODEL` | Model for new Goals, like `--model`. | Your workspace default |
| `TELOS_THINKING` | Thinking effort for new Goals: `low`, `medium`, `high` or `xhigh`. | `medium` |
| `TELOS_INSTALL_DIR` | Where the installer puts `telos`. | `~/.local/bin` |
| `TELOS_AGENT_SKILLS_DIR` | Where the installer puts the `telos-cli` skill. | `~/.agents/skills` |


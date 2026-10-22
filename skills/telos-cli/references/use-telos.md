---
title: Use Telos
description: Get started with building on Telos.
group: Getting started
---

Telos is a goal-oriented programming system.

A Telos program is a *goal specification*: a declarative description of a service's desired state. Telos agents build the service and keep reconciling its current state toward the goal.

This page takes you from installing Telos to launching your first Goal, then walks that Goal through the rest of its lifecycle.

## Install

Install Telos with:

```console
$ curl -fsSL https://usetelos.ai/install.sh | sh
```

This installs the `telos` binary and the `telos-cli` agent skill on your machine. Telos is built for the agent experience, and can be driven entirely through your coding agent of choice. Check the installed version with:

```console
$ telos --version
```

To update your installation later:

```console
$ telos update            # the latest release
$ telos update <version>  # a specific release
```

## Sign in

For first-time interactive setup, authenticate with Telos Cloud as shown below.

```console
$ telos login
Opening your browser to approve this login...
If it doesn't open, visit this link on any device: https://usetelos.ai/cli-auth?code=...
Waiting for approval...
logged in to https://api.usetelos.ai as alice@example.com
```

To validate your signed-in configuration, run:

```console
$ telos config
Config file      ~/.telos/config.yaml
Endpoint         https://api.usetelos.ai
Authentication   valid
Context          personal
Workspace model  telos/default
Credits          $50.00
Inference
  telos  Managed  telos/default, telos/max
Credentials
  example-api-key
```

`telos config` is useful to see the active context, the default workspace model, credits left, configured inference providers (see [Models](models.md) for more), and credentials (see [Credentials](environment.md#credentials) for more).

A context is the workspace your commands act in: `personal`, or a team's `@handle`.

## Write a spec

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

At a framework level, the above example covers the minimal required items. [Goals](goals.md) walks through everything else a goal specification can include: frontmatter, skills, rubrics, and packages.

## Plan

Once you are happy with the goal specification, `telos plan` validates the spec and shows what applying it would change, without changing anything.

```console
$ telos plan SPEC.md
Spec      reading-list
Version   0.1.0
Context   personal
```

The first plan has no deployed version to compare against, so it shows the Goal's name, version, and context.

## Apply

Once the plan looks good, use `telos apply` to deploy the spec to Telos Cloud.

```console
$ telos apply SPEC.md
created reading-list

Status    working
Goal      goal_c7d2f0a4e8
Revision  sha256:8f21c47a91ee
Model     telos/default
Thinking  medium
Context   personal
Logs      telos logs goal_c7d2f0a4e8
```

The `apply` command returns immediately and starts the Goal in the cloud with a unique ID.

To change only the model or thinking effort of an existing Goal, omit the spec file:

```console
$ telos apply --goal goal_c7d2f0a4e8 --model telos/max --thinking high
```

The current turn keeps its settings; the change takes effect at the next prover or verifier turn. Apply spec changes and inference settings in separate commands. See [Models](models.md) for supported choices.

## Watch

You can monitor your active Goal at different levels of detail and verbosity.

For a one-line overview of all running Goals:

```console
$ telos list
NAME          STATUS   ID
reading-list  working  goal_c7d2f0a4e8
```

For a description of a specific Goal, run `telos describe goal_c7d2f0a4e8`.

While an inference change is queued, text output shows the confirmed value and the requested value with an arrow and `(next turn)`. The model row names the connection, so changes between accounts remain visible even when both offer the same model. After confirmation, it shows the new value without an arrow. Rejected or unconfirmed requests are not shown as queued.

For scripts and agents, `telos describe --json` returns structured Goal details. Its `inference_state` contains confirmed settings only; it does not include the queued change shown in text output.

To follow the agent's work in detail:

```console
$ telos logs goal_c7d2f0a4e8
...
[2026-10-08T17:02:11Z] [INFO] Working on the spec requirements
[2026-10-08T17:06:48Z] [INFO] Stood up POST /books and GET /books per goal requirements
[2026-10-08T17:11:23Z] [INFO] Stored the reading list in PostgreSQL backed by persistent storage
[2026-10-08T17:14:05Z] [INFO] Checking the result against the spec
...
```

Add `--follow` to keep printing new activity as it happens, until the Goal stops or you press Ctrl-C.

`telos list` and `telos describe` report one of four statuses. When a Goal needs attention or has stopped, `describe` also shows why. Here's how to interpret them:

| Status | Meaning |
|---|---|
| `working` | Telos is preparing the Goal's [environment](environment.md), implementing the current spec, or verifying it. |
| `ready` | Telos accepted the current Goal. |
| `needs_attention` | The latest run failed or stopped unexpectedly. `telos describe` shows why, and what to run next. |
| `stopped` | The Goal has stopped running, usually because you deleted it. |

When a Goal is in `needs_attention`, re-applying the same spec with `telos apply SPEC.md --goal goal_c7d2f0a4e8` retries straight away. Credit or key failures also recover on their own once you add credits or fix the key. If the reason points to the environment or runtime, it may need escalation to Telos support (hello@usetelos.ai).

When Telos is done working, `describe` reports the Goal as `ready`, with its service URL and what it has cost so far.

```console
$ telos describe goal_c7d2f0a4e8
Name      reading-list
Status    ready
Goal      goal_c7d2f0a4e8
Revision  sha256:8f21c47a91ee
Model     telos/default
Thinking  medium
Context   personal
Service   https://reading-list-c7d2f0a4e8.usetelos.ai
Cloud     $1.25 (compute $1.00, storage $0.25)
Inference $3.40 (Telos)
```

Costs come in two lines: `Cloud` (compute and storage) and `Inference` (through Telos, your own API key, or a subscription; see [Models](models.md)).

Once ready, open the service and verify that the behavior is as desired.
In this case, you would exercise `POST /books` and `GET /books` through the public URL to confirm everything is in order.

Congratulations! Your first Goal is live.

## Update

Suppose the reading list now needs attribution. Edit the same `SPEC.md`, bump its version to `0.2.0`, and add "Every book records who added it" to the Goal.

Names are unique within a context, so updates target the Goal by its ID with `--goal`. Without it, `telos plan` and `telos apply` refuse to create a second `reading-list`.

Plan against the existing Goal:

```console
$ telos plan SPEC.md --goal goal_c7d2f0a4e8
Spec      reading-list
Context   personal
Goal      goal_c7d2f0a4e8
Current   @alice/reading-list:0.1.0
Version   0.1.0 -> 0.2.0

diff deployed/SPEC.md proposed/SPEC.md
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
 
 # Acceptance
 
```

The plan shows the Goal's current spec and how the new one
differs. Apply that new revision to the same Goal:

```console
$ telos apply SPEC.md --goal goal_c7d2f0a4e8
updated reading-list

Status    working
Goal      goal_c7d2f0a4e8
Revision  sha256:3211e85fe81b
Model     telos/default
Thinking  medium
Context   personal
Service   https://reading-list-c7d2f0a4e8.usetelos.ai
Logs      telos logs goal_c7d2f0a4e8
```

This applies the update in place, and Telos begins reconciling toward the new desired state. Continue to monitor status from `working` to `ready`, then exercise the updated API behavior.

If the Goal has been updated by someone else, you can fetch its current spec with:

```console
$ telos get goal_c7d2f0a4e8
```

This writes the spec and its skills to a directory named after the Goal. Use `--output <dir>` to override the directory name.

## Delete

If you would like to delete your Goal, along with its environment and the code the agent wrote — you can run:

```console
$ telos delete goal_c7d2f0a4e8
delete requested for reading-list

Status    stopped
Goal      goal_c7d2f0a4e8
Context   personal
```

> Deleting a Goal is irreversible — please proceed with caution.

The Goal disappears from `telos list` as soon as you delete it.

## Advanced configuration

For advanced use — such as scripting, CI, or custom development setups — you can configure various defaults.

In terms of precedence, flags override environment variables, and environment variables override your saved config (in `telos config`). For example, to work in a team's workspace by default, save its context:

```console
$ telos config --context @acme
context set to @acme
```

A `--context` flag still overrides it for a single command.

| Variable | Purpose | Default |
|---|---|---|
| `TELOS_AUTH_TOKEN` | API token provisioned [on the web](https://usetelos.ai/account?tab=tokens) for agents, CI and other non-interactive use. | Your saved login |
| `TELOS_CONTEXT` | Context for every command, like `--context`. | Your saved context |
| `TELOS_CONFIG` | Path to the config file. | `~/.telos/config.yaml` |
| `TELOS_MODEL` | Model for new Goals, like `--model`. | Your workspace default |
| `TELOS_THINKING` | Thinking effort for new Goals: `low`, `medium`, `high` or `xhigh`. | `medium` |
| `TELOS_INSTALL_DIR` | Where the installer puts `telos`. | `~/.local/bin` |
| `TELOS_AGENT_SKILLS_DIR` | Where the installer puts the `telos-cli` skill. | `~/.agents/skills` |

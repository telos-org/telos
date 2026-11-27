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

## Preview

Before applying a spec, preview the changes with `--dry-run`:

```console
$ telos apply SPEC.md --dry-run
Would create reading-list

Context   personal
Package   @alice/reading-list:0.1.0
Model     telos/default (workspace default)
Thinking  cloud default (resolved on apply)
```

`--diff` shows spec and skill file changes for both apply and dry runs.

## Apply

Once the preview looks good, use `telos apply` to deploy the spec to Telos Cloud.

```console
$ telos apply SPEC.md
Created reading-list

Status    working
Goal      g_c7d2f0a4e8
Package   @alice/reading-list:0.1.0
Revision  sha256:8f21c47a91ee
Model     telos/default
Thinking  medium
Context   personal
Logs      telos logs g_c7d2f0a4e8
```

The `apply` command returns immediately and starts the Goal in the cloud with a unique ID.

To change the model or thinking effort, apply the same goal spec with the updated setting:

```console
$ telos apply SPEC.md --model telos/max --thinking high
```

If the goal spec and settings are unchanged, `apply` reports `No changes to <name>`.

## Watch

You can monitor your active Goal at different levels of detail and verbosity.

For a one-line overview of all running Goals:

```console
$ telos list
NAME          STATUS   ID
reading-list  working  g_c7d2f0a4e8
```

For a description of a specific Goal, run `telos describe g_c7d2f0a4e8`.

After a model or thinking change, `describe` shows the new settings. The running turn may still use the previous ones until it finishes.

For scripts and agents, `telos describe --json` returns the same information as JSON.

To follow the agent's work in detail:

```console
$ telos logs g_c7d2f0a4e8
...
[2026-10-08T17:02:11Z] [INFO] Working on the spec requirements
[2026-10-08T17:06:48Z] [INFO] Stood up POST /books and GET /books per goal requirements
[2026-10-08T17:11:23Z] [INFO] Stored the reading list in PostgreSQL backed by persistent storage
[2026-10-08T17:14:05Z] [INFO] Checking the result against the spec
...
```

Add `--follow` to keep printing new activity as it happens, until the Goal stops or you press Ctrl-C.

`telos list` shows one of six statuses. Here's how to interpret them:

| Status | Meaning |
|---|---|
| `working` | Telos is preparing the Goal's [environment](environment.md), implementing the current spec, or verifying it. |
| `ready` | Telos accepted the current Goal. |
| `blocked` | The harness has stopped due to client-side issues (for example, unsatisfiable spec or a billing problem). |
| `retrying` | Something failed (for example, Telos server error or inference provider down), and Telos is retrying. |
| `paused` | Someone in your workspace [paused](#pause) the Goal. |
| `stopped` | The Goal has stopped running, usually because you deleted it. |

To filter Goals by status, use `--filter`:

```console
$ telos list --filter status=blocked
```

For a blocked or retrying Goal, `telos list` also shows the reason:

```console
$ telos list
NAME          STATUS                        ID
reading-list  blocked (SPEC_UNSATISFIABLE)  g_c7d2f0a4e8
```

| Reason | Status | Meaning | What to do |
|---|---|---|---|
| `SPEC_UNSATISFIABLE` | `blocked` | The agents found that the spec can't be met as written. | Edit the spec and apply the new version. |
| `CREDITS_EXHAUSTED` | `blocked` | The workspace is out of Telos credits. | Add Telos credits. |
| `PAYMENT_PAST_DUE` | `blocked` | A payment failed. | Update the payment method. |
| `SPEND_CAP_REACHED` | `blocked` | The workspace reached its monthly spend cap. | Raise or remove the cap. |
| `WORKSPACE_ARCHIVED` | `blocked` | The workspace is archived. | Restore the workspace. |
| `INTERNAL_ERROR` | `blocked` | The Goal failed to deploy. | Re-apply with `telos apply SPEC.md`. If it fails again, contact Telos support (hello@usetelos.ai) with the Goal ID. |
| `INTERNAL_ERROR` | `retrying` | Telos hit an error on its side. | Nothing at first. Telos keeps retrying. If it doesn't recover, contact Telos support (hello@usetelos.ai) with the Goal ID. |
| `INFERENCE_PROVIDER_ERROR` | `retrying` | The model provider is returning errors. | Check the Goal's API key or subscription, or wait for the model provider to recover. |

`telos describe` goes deeper into the error and shows what to do next. For a retrying Goal, it also shows when Telos will try again.

You can [pause](#pause) the Goal anytime to stop the retries. Billing and workspace blocks clear on their own once fixed.

When Telos is done working, `describe` reports the Goal as `ready`, with its service URL and what it has cost so far.

```console
$ telos describe g_c7d2f0a4e8
Name      reading-list
Status    ready
Goal      g_c7d2f0a4e8
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

`telos apply` updates the Goal with the same name in the selected workspace.

Preview the update against the existing Goal:

```console
$ telos apply SPEC.md --dry-run
Would update reading-list

Context   personal
Goal      g_c7d2f0a4e8
Package   @alice/reading-list:0.1.0 -> @alice/reading-list:0.2.0
Model     unchanged
Thinking  unchanged
```

Apply the update:

```console
$ telos apply SPEC.md
Updated reading-list

Status    working
Goal      g_c7d2f0a4e8
Package   @alice/reading-list:0.1.0 -> @alice/reading-list:0.2.0
Revision  sha256:3211e85fe81b
Model     telos/default
Thinking  medium
Context   personal
Service   https://reading-list-c7d2f0a4e8.usetelos.ai
Logs      telos logs g_c7d2f0a4e8
```

Add `-m "MSG"` to describe the revision (defaults to `Deploy v0.2.0`).

If the Goal has been updated by someone else, you can fetch its current spec with:

```console
$ telos pull g_c7d2f0a4e8
pulled reading-list into ./reading-list/
```

This writes the spec and its skills to a directory named after the Goal. Use `--output <dir>` to override the directory name.

## Propose

Instead of deploying a change directly, you can propose it for your team to review it first:

```console
$ telos propose SPEC.md -t "Let readers remove books"
opened proposal p_3f9a2b7c for reading-list
View: https://usetelos.ai/…
To update it: telos propose SPEC.md --onto p_3f9a2b7c -m "MSG"
```

`telos describe p_3f9a2b7c` shows the proposal's status, reviews, and changes.

```console
$ telos describe p_3f9a2b7c
Title     Let readers remove books
Status    Pending
Goal      reading-list (g_c7d2f0a4e8)
Updated   2m ago

SPEC.md
  11  11    - `GET /books` returns the current list.
      12  + - `DELETE /books/{id}` removes a title.
```

To comment on a proposal, run `telos comment` with the proposal's ID and `-m`. To comment on a specific line, pass its number with `--line`, and `--old` for a line the proposal removes. To comment on a frontmatter field instead, such as `interval` or `network`, use `--field`:

```console
$ telos comment p_3f9a2b7c -m "Looks good overall"
commented on p_3f9a2b7c (c_9b2e6a1d)

$ telos comment p_3f9a2b7c -m "What does this even mean?" --line 12
commented on SPEC.md line 12 (c_4d7a1f0e)

$ telos comment p_3f9a2b7c -m "Every hour seems too often" --field interval
commented on SPEC.md interval (c_7f3c8e2b)
```

To act on a comment, run `telos comment` with its ID and `reply`, `edit`, `resolve`, `reopen`, or `delete`. Only comments on a line can be resolved:

```console
$ telos comment c_4d7a1f0e reply -m "Ok"
replied to c_4d7a1f0e

$ telos comment c_4d7a1f0e edit -m "What does this line mean?"
edited c_4d7a1f0e

$ telos comment c_4d7a1f0e resolve
resolved c_4d7a1f0e

$ telos comment c_4d7a1f0e reopen
reopened c_4d7a1f0e

$ telos comment c_4d7a1f0e delete
deleted c_4d7a1f0e
```

To change a proposal, run `telos propose` with `--onto` and the proposal's ID. Pass a spec and `-m` to add an update, `-t` to rename the proposal, or `-d` to change its description. A new update resets its reviews:

```console
$ telos propose SPEC.md --onto p_3f9a2b7c -m "Only let readers remove books they added"
updated p_3f9a2b7c

$ telos propose --onto p_3f9a2b7c -t "Support removing books"
renamed p_3f9a2b7c to "Support removing books"

$ telos propose --onto p_3f9a2b7c -d "Adds an endpoint for removing books from the list"
updated p_3f9a2b7c
```

Workspace owners and admins can review the latest update of other members' proposals with `--approved` or `--blocked`, or clear their review with `--none`:

```console
$ telos review p_3f9a2b7c --approved
approved "Support removing books"
```

On a Goal that requires proposals, a member can apply a proposal only after an owner or admin approves its latest update. Nobody can apply a proposal while a reviewer blocks it. To apply a proposal, apply it by its ID:

```console
$ telos apply p_3f9a2b7c
accepted "Support removing books" for reading-list

Status    working
Goal      g_c7d2f0a4e8
Package   @alice/reading-list:0.2.1
Revision  sha256:8c41d07a2e5f
Model     telos/default
Thinking  medium
Context   personal
Service   https://reading-list-c7d2f0a4e8.usetelos.ai
Logs      telos logs g_c7d2f0a4e8
```

When the Goal changes, all open proposals become stale. A stale proposal can't be updated or applied. To start over, run `telos pull` on the Goal, redo your changes, and propose again. To close a proposal, run `telos delete p_3f9a2b7c`.

To find proposals and comments, run `telos list` with `--filter`:

```console
$ telos list proposals --filter goal=g_c7d2f0a4e8
TITLE                   STATUS   UPDATED  ID
Support removing books  Applied  2m ago   p_3f9a2b7c

$ telos list comments --filter proposal=p_3f9a2b7c,status=open
ID          AUTHOR             ON               REPLIES
c_4d7a1f0e  admin@example.com  SPEC.md line 12  1
```

| Listing | Filters |
|---|---|
| `proposals` | `goal`, `status` (`pending`, `applying`, `applied`, `failed`, `closed`, or `stale`), `author` (`me` or an email address) |
| `comments` | `goal` or `proposal`, `status` (`open` or `resolved`), `author` (`me` or an email address) |

To match more than one statuses, repeat `status`, like `--filter status=closed,status=stale`.

`telos pull p_3f9a2b7c` downloads the Goal's spec and skills as they would be with the proposal applied, into `./p_3f9a2b7c/`.

## Pause

To stop the harness working on a Goal, you can pause it with:

```console
$ telos pause g_c7d2f0a4e8
Paused g_c7d2f0a4e8
```

The service keeps running while the Goal is paused. To resume the harness, run:

```console
$ telos resume g_c7d2f0a4e8
Resumed g_c7d2f0a4e8
```

Only workspace admins can pause or resume a Goal. A Goal can be paused while it's `working`, `ready` or `retrying`, and resumed while it's `paused` or blocked by its spec.

## Delete

If you would like to delete your Goal, along with its environment and the code the agent wrote — you can run:

```console
$ telos delete g_c7d2f0a4e8
delete requested for reading-list

Status    stopped
Goal      g_c7d2f0a4e8
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

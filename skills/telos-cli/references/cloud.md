---
title: Telos Cloud
description: Authenticate through a browser or a token for agents and CI, choose a Cloud context, and confirm that a Goal fits the managed runtime.
group: Platform
---

# Telos Cloud

Telos Cloud gives a persistent Goal a managed deployment, a stable session, and
a public HTTPS route when the Goal exposes a service. Each environment contains
an agent workspace and a Kubernetes namespace where the application runs.

A CLI **context** selects the personal or team Cloud workspace that owns the
deployment. Inside an environment, **workspace** means the agent's retained
filesystem. The two uses are related but not interchangeable.

## Authenticate

Telos supports both browser login and non-interactive token authentication.
A token is a secret login credential that the CLI sends with Cloud requests.

### Browser login on your computer

For an initial interactive login:

```bash
telos login
telos config
```

Approve the login in your browser. The CLI saves the resulting device API
token as `auth_token` in `~/.telos/config.yaml`, or the file selected by
`TELOS_CONFIG`. Later Cloud commands reuse that token without another browser
approval while it remains valid.

### Token authentication for agents and CI

For an unattended job, supply an existing Telos API token through the
`TELOS_AUTH_TOKEN` environment variable using your runner's secret store.
Run Cloud commands directly; the job needs no `telos login` step or browser
approval.

Obtain the token before the job starts. For example, an approved `telos login`
creates the saved device token described above. Store the token privately in
the runner's secret settings without printing it in logs or committing it to
the repository. A job using that token loses access if the token is revoked.

Once the CLI is installed, this GitHub Actions step uses a repository secret
named `TELOS_AUTH_TOKEN` to check access to your personal workspace:

```yaml
- name: Check Telos access
  env:
    TELOS_AUTH_TOKEN: ${{ secrets.TELOS_AUTH_TOKEN }}
    TELOS_CONTEXT: personal
  run: |
    telos config
    telos list --cloud --json
```

Use the intended `@team-handle` instead of `personal` for a team workspace.
`telos config` reports authentication status without showing the token;
`telos list --cloud --json` makes a read-only Cloud request and fails if access
is rejected. The token authenticates an existing account and its permissions.

### Configuration precedence

| Setting | Effect |
| --- | --- |
| `TELOS_AUTH_TOKEN` | A non-empty value overrides the saved `auth_token`. |
| `TELOS_API_ENDPOINT` | A non-empty value overrides the saved API endpoint. With neither set, Cloud defaults to `https://api.usetelos.ai`. |
| `TELOS_CONTEXT` | A non-empty value overrides the saved context; a command's `--context` flag takes precedence over both. |
| `TELOS_CONFIG` | Selects a configuration file instead of `~/.telos/config.yaml`. |

The Cloud account token setting is `TELOS_AUTH_TOKEN`. `TELOS_TOKEN` is not a
supported alias, and `TELOS_API_TOKEN` serves a separate runtime session role.

`telos login` checks the saved login rather than `TELOS_AUTH_TOKEN` and may
open a browser if no valid saved login exists. When you supply a token through
the environment, check it with `telos config` and run your Cloud command
directly. If that token is rejected, replace or unset the override; a new
browser login does not replace the token in the environment.

## Choose the context

`telos config` shows authentication status, the active context, the workspace
model default, subscriptions, and saved API-key connections. Choose available
models and set the shared default under **Inference** in the Telos app.
See [Models and inference](inference.md) to override one deployment with `--model`.

The personal context is `personal`. Team contexts use their handle:

```bash
telos config --context @team-handle
telos list --context @team-handle
```

Commands also accept a stable organization ID when you have one. Receipts and
JSON output still show `personal` or the team's `@handle`, keeping the visible
context consistent across commands.

`telos config --context personal` returns to the personal context. On deployment
commands, `--context` overrides `TELOS_CONTEXT` and stored configuration
for that invocation without changing either. Carry the chosen context through
`plan`, `apply`, `describe`, `logs`, and `delete` so each action has one visible
target.

For jobs using injected credentials, choose the context with `TELOS_CONTEXT`
or `--context`. `telos config --context` changes saved configuration and uses
saved credentials rather than the token and endpoint environment overrides.

## Credentials and external access

Credentials are reusable bundles stored in your personal or team workspace.
List them, or get the web form for adding one, using the same context as your
deployment. Replace `CONTEXT` with `personal` or your `@team-handle`:

```bash
telos credentials list --context CONTEXT
telos credentials add --context CONTEXT
```

- `list` shows saved IDs, names, and credential key names, never values.
  Platform-managed credentials are omitted.
- `add` prints a workspace-specific setup link. It does not open a browser,
  save a credential, or attach it to a deployment. Open the link yourself,
  save credentials in the form, then run `list` to obtain the saved ID.
- Both commands accept `--json`. Setup links require the official Telos Cloud
  endpoint; `add` rejects custom endpoints.
- `integrations` remains a compatibility alias; its JSON list retains the
  `integrations` key. The new command uses `credentials`.

Bind the saved credential to each appropriate destination in your Goal's
[`egress` frontmatter](goals.md#declare-external-access). For creation,
Cloud binds declared access before the agent starts. To add or remove access
later, revise the Goal and apply it to the same session. Web access edits also
go through a Goal revision or Change Request, not a separate attachment action.

Cloud checks workspace permissions, credential availability, and runtime
support. Deployment access changes require workspace owner/admin privileges
and any applicable deployment approval. Saving a credential alone does not
grant a deployment access to it.

Credentials may be shared across destinations and deployments. Removing one
binding does not delete the saved credential. Rotating or revoking a shared
credential can affect every deployment using it.

`plan` shows each destination's credential reference and restrictions; it does
not establish that access works. Before uploading or deploying a Goal with
`egress`, `apply` requires the backend to advertise `deployment_egress_credentials`.
Legacy paired `integrations` and `allowlist` fields require `deployment_spec_access`.
Missing support or an unreadable capability response stops the apply.
This also covers registry packages,
empty lists that revoke access, and updates using `--force`. Goals without
these fields remain compatible with older backends.

## Preflight the managed runtime

A spec describes desired behavior; it cannot add a missing platform surface.
Public egress is default-deny: Cloud provides the common read paths below, and
other agent requests need a matching Goal egress rule. Authenticated
requests also need an appropriate credential on that rule. Before applying,
identify how the implementation will fit these Cloud capabilities:

| Need | Current Cloud path |
| --- | --- |
| Deliver a workload | Use a digest-pinned published image, a repository's existing image publication workflow, or a read-only ConfigMap for a small interpreted service. |
| Keep application data | Mount a persistent volume claim. Its lifecycle is bound to the claim and Cloud environment. |
| Fetch build dependencies | Docker Hub images, PyPI packages, npm packages, and Telos artifacts have built-in read access. |
| Reach another public API from the agent | Declare HTTPS destinations in `egress`, with workspace credential IDs where authentication is needed. |
| Reach another service from the deployed application | No general managed credential connector is currently injected into Kubernetes workloads. |

The Cloud agent receives the full `telos-cloud` operating skill inside the
environment. It explains delivery, persistence, networking, and verification
in detail.

## Current credential limits

| Limit | Consequence |
| --- | --- |
| Startup environment variables | Include credentials that require these at creation. Cloud rejects adding them to an already-running deployment. |
| Public credential markers, AWS, and HMAC signing | A Goal update can add credentials that need no new startup variables, bound to the appropriate destinations. |

A missing image path or workload connector is likewise a platform constraint,
not something `SPEC.md` can create.

Verify the external API call from the deployed application before relying on
it. A saved credential, successful apply, or agent-side request does not prove
that the application has a working credential path.

## Apply and observe

Use the workflow in [Use Telos](use-telos.md), passing the same explicit context
through every Cloud command. `apply` publishes an immutable spec package and
creates the deployment. The receipt identifies the context, revision digest,
and stable session to follow.

[The Goal lifecycle](lifecycle.md) owns state, revision, observation, and
deletion semantics. [Models and inference](inference.md) explains how the new
session receives its Cloud model selection.

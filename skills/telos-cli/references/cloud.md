---
title: Telos Cloud
description: Choose a Cloud context and confirm that a Goal fits the managed runtime before applying it.
group: Platform
---

# Telos Cloud

Telos Cloud gives a persistent Goal a managed deployment, a stable session, and
a public HTTPS route when the Goal exposes a service. Each environment contains
an agent workspace and a Kubernetes namespace where the application runs.

A CLI **context** selects the personal or team Cloud workspace that owns the
deployment. Inside an environment, **workspace** means the agent's retained
filesystem. The two uses are related but not interchangeable.

## Choose the context

`telos config` shows the active account, context, and machine-local default
model:

```bash
telos login
telos config
```

The personal context is `personal`. Team contexts use their handle:

```bash
telos config --context @team-handle
telos list --context @team-handle
```

Commands also accept a stable organization ID when you have one. Receipts and
JSON output still show `personal` or the team's `@handle`, keeping the visible
context consistent across commands.

`telos config --context personal` returns to the personal context. A
command-level `--context` overrides `TELOS_CONTEXT` and stored configuration
for that invocation without changing either. Carry the chosen context through
`plan`, `apply`, `describe`, `logs`, and `delete` so each action has one visible
target.

## Preflight the managed runtime

A spec describes desired behavior; it cannot add a missing platform surface.
Public egress is default-deny: Cloud provides the common read paths below, and
other agent requests need a matching integration. Before applying, identify
how the implementation will fit these current Cloud capabilities:

| Need | Current Cloud path |
| --- | --- |
| Deliver a workload | Use a digest-pinned published image, a repository's existing image publication workflow, or a read-only ConfigMap for a small interpreted service. |
| Keep application data | Mount a persistent volume claim. Its lifecycle is bound to the claim and Cloud environment. |
| Fetch build dependencies | Docker Hub images, PyPI packages, npm packages, and Telos artifacts have built-in read access. |
| Reach another public API from the agent | Attach an operator-managed HTTPS integration with rules for the required request. |
| Reach another service from the deployed application | No general managed credential connector is currently injected into Kubernetes workloads. |

The Cloud agent receives the full `telos-cloud` operating skill inside the
environment. It explains delivery, persistence, networking, and verification
in detail.

## Current integration limits

| Limit | Consequence |
| --- | --- |
| CLI creation | The CLI cannot attach an integration before the first reconciliation. Use a creation surface that binds it before the initial agent claim, or treat that dependency as unsupported by the CLI path. |
| AWS and HMAC signing | A later attachment can sign requests for a later revision because no agent placeholder is required. |
| Static replacement | Its placeholder is delivered only in the initial claim. A post-creation attachment cannot add it to the existing agent environment. |

A missing image path or workload connector is likewise a platform constraint,
not something `SPEC.md` can create.

## Apply and observe

Use the workflow in [Use Telos](use-telos.md), passing the same explicit context
through every Cloud command. `apply` publishes an immutable spec package and
creates the deployment. The receipt identifies the context, revision digest,
and stable session to follow.

[The Goal lifecycle](lifecycle.md) owns state, revision, observation, and
deletion semantics. [Models and inference](inference.md) explains how the new
session receives its Cloud model selection.

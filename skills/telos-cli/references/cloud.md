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

`telos config --context personal` returns to the personal context. A
command-level `--context` overrides `TELOS_CONTEXT` and stored configuration
for that invocation without changing either. Carry the chosen context through
`plan`, `apply`, `describe`, `logs`, and `delete` so each action has one visible
target.

## Preflight the managed runtime

A spec describes desired behavior; it cannot add a missing platform surface.
Before applying, identify how the implementation will fit these current Cloud
capabilities:

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

The current CLI cannot bind an integration atomically with a new deployment.
When the first reconciliation needs credentialed agent-time access, use a
creation surface that binds the integration before the environment's initial
agent claim, or treat that Goal as unsupported by the CLI path.

AWS and HMAC signing policies can be attached later and consumed by a later
revision because they add request credentials at the outbound boundary. Static
replacement is different: its agent placeholder is delivered only in the
initial claim, so a post-creation attachment cannot retrofit it into the
existing agent environment. A missing image path or workload connector is
likewise a platform constraint, not something `SPEC.md` can create.

## Apply and observe

Use the workflow in [Use Telos](use-telos.md), passing the same explicit context
through every Cloud command. `apply` publishes an immutable spec package and
creates the deployment. The receipt identifies the context, revision digest,
and stable session to follow.

[The Goal lifecycle](lifecycle.md) owns state, revision, observation, and
deletion semantics. [Models and inference](inference.md) explains how the new
session receives its Cloud model selection.

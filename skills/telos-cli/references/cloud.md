# Telos Cloud

Telos Cloud gives a persistent Goal a managed runtime, stable session identity,
and a public service surface when the Goal exposes one.

## Choose the context

`telos config` shows the active account, context, and default model:

```bash
telos login
telos config
```

The personal workspace is named `personal`. Team workspaces use their handle:

```bash
telos config --context @team-handle
telos list --context @team-handle
```

`telos config --context personal` returns to the personal workspace. A
command-level `--context` overrides `TELOS_CONTEXT` and stored configuration
for that invocation without changing either.

## Apply a Cloud Goal

A managed spec declares `platform: cloud`:

```bash
telos plan SPEC.md
telos apply SPEC.md
```

`apply` publishes the immutable spec package and creates a deployment for it.
The receipt identifies the context, revision digest, and session to follow:

```bash
telos describe SESSION_ID
telos logs SESSION_ID
```

The deployment becomes `ready` when the displayed revision has been
reconciled and accepted. [The Goal lifecycle](lifecycle.md) describes every
state and the evidence behind it.

## Revise it

Retrieve the current spec when it is not already available locally:

```bash
telos get SESSION_ID --output SPEC.md
```

Edit the contract, bump its version, and compare it with the deployed package:

```bash
telos plan SPEC.md --session SESSION_ID
telos apply SPEC.md --session SESSION_ID
```

The new package is immutable, while the Goal keeps the same session and
history. `telos delete SESSION_ID` stops the deployment when deleting it is
part of the requested lifecycle.

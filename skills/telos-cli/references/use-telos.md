---
title: Use Telos
description: Give Telos a Goal, then follow it from a proposed contract to verified software.
---

# Use Telos

Telos is a goal-oriented programming system. You describe what the software
should do in `SPEC.md`; Telos assigns agents to implement, run, and verify the
current revision.

The spec is the durable artifact. Implementations can change as the Goal
evolves, while the Goal keeps its identity, history, and evidence.

This guide follows one small service from its first spec through a live update.

## Install

The CLI is designed to be driven by your coding agent. You can ask it:

```text
Set up Telos.

- Install it with `curl -fsSL https://usetelos.ai/install.sh | sh`.
- Run `telos login` so I can approve access in the browser.
- Read the installed `telos-cli` skill before continuing.
```

Or install and sign in directly:

```bash
curl -fsSL https://usetelos.ai/install.sh | sh
telos login
```

`telos login` opens a browser approval flow. Once it completes, `telos config`
shows the account and Cloud context the CLI will use:

```console
$ telos config
Config file     ~/.telos/config.yaml
Endpoint        https://api.usetelos.ai
Authentication  valid
Context         personal
Default model   workspace default
Subscriptions
```

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

This contract names the behavior that matters without choosing a framework,
database, or deployment layout. `platform: cloud` gives the Goal a managed,
persistent lifecycle. A local spec uses `platform: local` instead.

[Goals and specifications](goals.md) covers the complete spec shape.
[Packages and skills](packages-and-skills.md) shows how to import reusable
capabilities and evaluation rubrics.

## Preview the first revision

`plan` validates the spec and shows where it will run without changing any
remote state:

```console
$ telos plan SPEC.md
Spec      reading-list
Target    cloud
Context   personal
Path      /Users/alice/reading-list/SPEC.md
Namespace ns-reading-list
Hash      799e5c31172afb26
```

The first plan has no deployed revision to compare with, so it shows the
Goal's identity, target, namespace, and content hash.

## Apply it

```console
$ telos apply SPEC.md
created reading-list

Status    working
Session   sess_c7d2f0a4e8
Revision  sha256:8f21c47a91ee1438e724bdb55edc81af864db782c29dfb10870e8cdb304f6e1a
Context   personal
Logs      telos logs --context personal sess_c7d2f0a4e8
```

`working` means Cloud accepted this revision and is reconciling it. The
session ID remains the identity of the Goal across later revisions.

Follow progress with the commands printed in the receipt:

```bash
telos describe sess_c7d2f0a4e8
telos logs sess_c7d2f0a4e8
```

Log messages reflect the actual implementation and verification work, so they
vary from Goal to Goal. By default, `logs` shows the 50 most recent activity
rows.

## Observe Ready

When reconciliation succeeds, `describe` reports the exact accepted revision
and any public service URL:

```console
$ telos describe sess_c7d2f0a4e8
Name      reading-list
Status    ready
Session   sess_c7d2f0a4e8
Revision  sha256:8f21c47a91ee1438e724bdb55edc81af864db782c29dfb10870e8cdb304f6e1a
Context   personal
Service   https://reading-list-c7d2f0a4e8.usetelos.ai
```

`ready` belongs to that revision digest: Cloud reconciled it and its latest
verification passed. The service URL is the next piece of evidence. Exercise
the routes named by the spec and confirm that their behavior matches the
contract.

## Revise the same Goal

Suppose the reading list now needs attribution. Bump the version and add the
new behavior to the same spec:

```markdown
version: 0.2.0

# Goal

- Every book records who added it.
```

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
 platform: cloud
 ---

@@ -11,6 +11,7 @@
 - `POST /books` adds a title.
 - `GET /books` returns the current list.
 - Books remain available when the application restarts.
+- Every book records who added it.
```

Unlike the first preview, a session-aware plan includes the package currently
deployed and a unified diff of the proposed contract. Apply the revision to
that same session:

```bash
telos apply SPEC.md --session sess_c7d2f0a4e8
```

Cloud reconciles the change while preserving the Goal's session and history.
The new revision moves through `working` and `ready` like the first.

## Run bounded work

Some work has a natural stopping point rather than a persistent service
lifecycle. A local spec can run for at most three review cycles:

```bash
telos run SPEC.md --workspace . --until 3
```

`run` returns a session with logs and evidence, then stops at the bound.
`apply` is the persistent lifecycle used by the reading-list example.

## Go deeper

- [The Goal lifecycle](lifecycle.md) explains states, revisions, and evidence.
- [Goals and specifications](goals.md) explains what the contract can express.
- [Telos Cloud](cloud.md) covers contexts, teams, and managed deployments.
- [Models and inference](inference.md) covers managed models and connected subscriptions.
- [Troubleshooting](troubleshooting.md) starts from the state Telos actually reports.

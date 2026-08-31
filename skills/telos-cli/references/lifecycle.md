---
title: The Goal lifecycle
description: Follow a Goal through revisions, understand its states, and read the evidence behind Ready.
group: Concepts
---

# The Goal lifecycle

A persistent Goal has one stable session and a sequence of immutable revisions.
Each revision is another attempt to make the contract in `SPEC.md` true.

```text
SPEC.md ── plan ── apply ── working ── ready
                                  └── needs_attention
```

`apply` returns when Cloud accepts the revision for work. Reconciliation then
continues in the background, and `describe` shows what happened next.

## Read the current state

```bash
telos describe SESSION_ID
```

A managed Goal reports one of four states:

| State | What it tells you |
| --- | --- |
| `working` | Cloud is preparing the runtime, implementing the spec, or verifying the result. |
| `ready` | The displayed revision was reconciled and its latest verification passed. |
| `needs_attention` | Reconciliation stopped after a failed run, rejected verification, or unexpected session stop. The reason describes the next decision. |
| `stopped` | The deployment was stopped or deleted. |

The accompanying reason makes `working` and `needs_attention` more specific.
It distinguishes runtime preparation from implementation and verification, and
it preserves the failure that ended an unsuccessful attempt.

## Ready belongs to a revision

The revision digest in `describe` is part of the result:

```console
$ telos describe sess_c7d2f0a4e8
Name      reading-list
Status    ready
Session   sess_c7d2f0a4e8
Revision  sha256:8f21c47a91ee1438e724bdb55edc81af864db782c29dfb10870e8cdb304f6e1a
Context   personal
Service   https://reading-list-c7d2f0a4e8.usetelos.ai
```

This says that the displayed package was accepted. A later edit, a previous
green event, or a running process is a different fact. For automation,
`telos describe SESSION_ID --json` exposes both `status` and
`package_digest` without parsing the table.

When a Goal publishes a service, the service itself completes the evidence:
exercise the behavior named in the spec after the matching revision is
`ready`.

## Move the Goal forward

Edit `SPEC.md`, bump its version, and compare it with the deployed revision:

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
```

`Current` is the immutable package reference deployed in the session. The
unified diff that follows is the contract change Cloud will reconcile.

```bash
telos apply SPEC.md --session sess_c7d2f0a4e8
```

The session ID and its history remain stable while the new revision moves
through the lifecycle.

## Read the work behind the state

```bash
telos logs SESSION_ID
```

The default view contains the 50 most recent activity rows. Choose a larger or
more structured view when the question requires it:

| View | Command |
| --- | --- |
| Last N activity rows | `telos logs SESSION_ID --tail N` |
| Complete activity history | `telos logs SESSION_ID --all` |
| Underlying transcript and evidence events | `telos logs SESSION_ID --raw` |
| Newline-delimited event records | `telos logs SESSION_ID --json` |

The runtime also records session states beneath the managed deployment:
`pending`, `running`, `completed`, `failed`, `stopped`, and `stale`.
A revision is separately `pending`, `accepted`, or `failed`. Acceptance of
the current package digest is what produces the managed `ready` state.

## Stop a Goal

```bash
telos delete SESSION_ID
```

Deletion stops the deployment. A Goal in `needs_attention` still retains its
identity, history, and failure reason, so a corrected revision can continue on
the same session.

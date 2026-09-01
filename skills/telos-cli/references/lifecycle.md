---
title: The Goal lifecycle
description: Understand Goal identity, observe revisions, and read the evidence behind ready.
group: Concepts
---

# The Goal lifecycle

Telos separates the outcome you author from the immutable revisions and
execution that make it true.

| Term | Public meaning |
| --- | --- |
| Goal | The durable outcome and identity that persist across spec revisions. |
| `SPEC.md` | The editable source contract describing the outcome and its evidence. |
| Revision | One immutable version of the compiled contract, identified by a digest. |
| Session | The CLI handle and history. A persistent Goal keeps one session across revisions; each bounded run gets its own session. |
| Deployment | The managed Cloud object that owns the current revision, runtime allocation, URLs, and status for a persistent Goal. |

The two execution paths are:

```text
Persistent: SPEC.md → plan → apply → Goal/session/deployment → revision history
Bounded:    local spec → run with a bound → run session → evidence
```

`apply` returns after Cloud accepts a revision for work. Reconciliation
continues in the background; `describe` reports the managed Goal state.

## Read the state layers

The product exposes three related state layers:

| Layer | States | Where it appears |
| --- | --- | --- |
| Managed Goal | `working`, `ready`, `needs_attention`, `stopped` | Cloud `list` and `describe` |
| Execution session | `pending`, `running`, `completed`, `failed`, `stopped`, `stale` | Local runs and lower-level runtime events |
| Revision acceptance | `pending`, `accepted`, `failed` | Reconciliation and evidence events |

A managed Goal reports:

| State | Meaning |
| --- | --- |
| `working` | Cloud is preparing the runtime, implementing the spec, or verifying the revision. |
| `ready` | The current reconciliation completed and its latest verification passed. |
| `needs_attention` | Reconciliation stopped after a failed run, rejected verification, or unexpected session stop. The reason identifies the next decision. |
| `stopped` | The deployment is stopped or its deletion has begun. |

## `ready` belongs to a revision

Capture the revision digest returned by `apply`, then compare it with
`package_digest` from `describe --json`. On current reconciliation-aware
runtimes, `ready` means reconciliation completed and the latest verification
passed for that displayed digest. The service itself completes the evidence:
exercise the behavior named by the spec after the matching revision is `ready`.

Public-route publication has its own surface probe and can finish after the
managed state first becomes `ready`. A public service also needs a non-empty
`service_url` from `describe --json` before external verification can begin.

### Compatibility note

Older deployments can project `ready` from a completed execution without
digest-bound reconciliation, and the current CLI does not expose that status
provenance. Live behavior is therefore required evidence even when the
displayed digest matches the receipt.

## Observe without waiting forever

Use the context, session, and digest from the `apply` receipt. Unless the Goal
suggests a different runtime, use a 30-minute observation deadline:

```bash
telos describe SESSION_ID --context CONTEXT --json
```

An agent observation loop has four operations:

1. Run `describe --json` every 15 seconds.
2. Read `status` and `package_digest` from each response.
3. Continue at `working`. Finish at `ready` when no public route is required;
   for a public service, finish when `ready` also includes `service_url`.
   Return the reason at `needs_attention` or `stopped`.
4. Stop if the digest changes or the 30-minute deadline expires, then return the
   last state instead of waiting indefinitely.

`logs` supplies the work and verification evidence behind the state:

```bash
telos logs SESSION_ID --context CONTEXT
```

The default view contains the 50 most recent activity rows:

| View | Command |
| --- | --- |
| Last N activity rows | `telos logs SESSION_ID --context CONTEXT --tail N` |
| Complete activity history | `telos logs SESSION_ID --context CONTEXT --all` |
| Underlying transcript and evidence events | `telos logs SESSION_ID --context CONTEXT --raw` |
| Newline-delimited event records | `telos logs SESSION_ID --context CONTEXT --json` |

## Move a persistent Goal forward

Edit `SPEC.md`, bump its version, and compare the proposed contract with the
deployed revision:

```bash
telos plan SPEC.md --session SESSION_ID --context CONTEXT
telos apply SPEC.md --session SESSION_ID --context CONTEXT
```

The Goal, session, deployment, and history remain stable. The new immutable
revision moves through the same lifecycle. [Use Telos](use-telos.md) shows the
full diff and receipt.

## Delete a Goal

Resolve the session and context, explain the consequences below, and obtain the
user's approval before running:

```bash
telos delete SESSION_ID --context CONTEXT
```

Cloud deletion is irreversible. It tears down the environment and application,
including PVC data; removes public routes, the deployment record, integration
attachments, and Goal history. Teardown is asynchronous, so the receipt may
report that deletion was requested while cleanup continues. Once cleanup
finishes, `list` omits the Goal and subsequent `describe` calls return not
found.

Local deletion has different semantics:

```bash
telos delete LOCAL_SESSION_ID
```

It stops the local session and preserves its history. A Goal in
`needs_attention` also retains its Cloud history until the user either applies
a corrected revision or explicitly deletes it.

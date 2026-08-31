---
title: The Goal lifecycle
description: What a Goal's states mean, how it moves between them, and what Ready proves.
group: Concepts
---

# The Goal lifecycle

A Goal outlives every attempt to satisfy it. Sessions, agents, and revisions
are replaceable; the Goal and its session ID are not. This page is the state
model behind that.

## The shape of a revision

```
edit SPEC.md ─▶ telos plan ─▶ telos apply ─▶ working ─▶ ready
                                                │
                                                └─▶ needs_attention
```

`apply` returns as soon as the work is accepted, not when it is done. The
session ID it prints is stable for the life of the Goal. Every later revision
reuses it with `--session`.

## Deployment status

A managed Goal reports one of four states. `telos describe SESSION_ID` shows
it, and `--json` exposes it as `status`.

| Status | Meaning |
| --- | --- |
| `working` | Preparing the runtime, implementing the spec, or verifying it. |
| `ready` | The current spec was accepted and its latest verification passed. |
| `needs_attention` | A run failed, verification was rejected, or the session stopped unexpectedly. Read the reason. |
| `stopped` | The deployment was stopped or deleted. |

Each status carries a human-readable reason. While `working`, that reason
distinguishes preparing the runtime from implementing the spec from verifying
it, so a stalled Goal can be told apart from a busy one.

`needs_attention` is not a transient state to wait out. It means the platform
has stopped making progress and wants a decision.

## What Ready proves

`ready` is scoped to an exact revision digest. It means *this* package was
reconciled and accepted — not that the Goal is healthy in general, and not
that a future drift check has run.

```bash
telos describe SESSION_ID --json | jq -e '.status == "ready"'
```

`jq` is not part of the Telos install; use `--json` with whatever the machine
already has.

Do not read acceptance from anything else. Allocation succeeding, a process
starting, a green test in the logs, and a stale event from a previous revision
are all compatible with a Goal that never became `ready`. When the contract
exposes public behaviour, probe that too — the live surface is the claim, and
tests are not a substitute for it.

## Revising a Goal

Update the spec, bump its version, and re-plan against the durable session.
This is the only form of `plan` that produces a reviewable diff:

```console
$ telos plan SPEC.md --session sess_c7d2f0a4e8
Session   sess_c7d2f0a4e8
Current   sha256:8f21c47a...
Version   1.1.3 -> 1.2.0

+ 30 days of uptime history render for each service.
```

A first `plan`, with no `--session`, has nothing to compare against and prints
only the spec's identity and hash. Then:

```bash
telos apply SPEC.md --session sess_c7d2f0a4e8
```

Work is incremental. The session, its history, and the service URL stay
stable while agents reconcile the change.

## Session states

Underneath a deployment, the runtime tracks its own session status: `pending`,
`running`, `completed`, `failed`, `stopped`, or `stale`. The last four are
terminal — no further progress will happen without a new revision.

Each revision also carries a reconciliation state for its exact package
digest: `pending`, `accepted`, or `failed`. `accepted` for the current digest
is what makes a deployment `ready`.

`telos list --wide` shows sessions across a context; `--cloud` and `--local`
narrow it.

## Reading the evidence

```bash
telos logs SESSION_ID
```

**`logs` shows the 50 most recent activity rows by default.** A truncated
window is the usual reason an agent concludes the wrong thing about a run.

- `--tail N` for a different window
- `--all` for every row
- `--raw` for the underlying transcript and evidence events
- `--json` for newline-delimited events

## Stopping

Deletion is consequential and is not a way to retry. Confirm the session and
what should be preserved first, then:

```bash
telos delete SESSION_ID
```

Never leave a Goal in `needs_attention` and create a second deployment beside
it. Diagnose the first.

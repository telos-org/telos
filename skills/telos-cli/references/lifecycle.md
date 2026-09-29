---
title: The Goal lifecycle
description: Understand Goal identity, observe revisions, and read the evidence behind ready.
group: Concepts
---

# The Goal lifecycle

Telos separates the outcome you author from the immutable revisions and
execution that make it true.

The [Glossary](glossary.md) defines the wider Telos vocabulary. These five
objects form the persistent lifecycle:

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

Cloud `plan` creates a preview without applying it; `plan --out=FILE` saves an
immutable Change Request for later confirmation, with or without deployment
protection. `apply SPEC.md` prepares a fresh plan
and asks for confirmation; protected deployments first wait for their queue turn.
`apply FILE` confirms the exact saved proposal. A successful apply receipt reports confirmed, applying, or applied
work, not successful agent verification. See [Change Requests](change-requests.md).
After execution, reconciliation continues in the background; `describe`
reports the managed Goal state and pending requests separately.

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

After the plan or Change Request is applied, capture its proposed digest (or the
revision digest from an older server's immediate receipt), then compare it with
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

Saved Change Requests report `requested` and have a review link. Apply them
through that page or `telos apply FILE` when you have permission. An authorized
editor can confirm an optional request; protected requests require an owner or
admin. A fresh unprotected apply uses a direct plan and creates no Change Request.
If a request is queued or awaiting confirmation, the old
revision's status does not describe the proposal. `describe --json` includes
`pending_change_requests` separately. An initial creation waiting for confirmation
has no current revision yet. After apply returns, check the same request until
its result revision is available; confirmation and agent verification are separate.

Once the requested action has executed, use the context, session, and proposed
digest from the `apply` receipt. Unless the Goal
suggests a different runtime, use a 30-minute observation deadline:

```bash
telos describe SESSION_ID --context CONTEXT --json
```

You can observe an executing revision as follows:

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

The default view contains the 50 most recent activity rows.

Progress updates describe the behavior you requested: what changed, what was
confirmed, or what blocks the next step. Their wording is generated by the model.
The same updates stay in the transcript alongside detailed technical reports;
tool activity and reviews remain available in the underlying evidence.

Older sessions retain their original presentation. New JSON `agent_progress`
records mark `progress_update` messages with `data.audience: "user"` and technical
activity with `data.audience: "agent"`.

If an active session on a new runtime has not reported human-facing progress for
five minutes, you see the age and last reported activity. Technical activity
does not reset that timer. This does not imply that the agent has stopped;
delay explanations appear when the agent has an observed reason to report.

Hidden technical activity does not count toward `--tail`. On current Cloud
servers, Telos loads older pages as needed to find the requested activity rows.
If older history is unavailable or the session changes while loading, you keep
the available rows and see a notice that the history is incomplete. Older
servers may require a full-history request.

On new runtimes, a passing completion entry appears when Telos confirms that
the current round of checks has ended successfully. This does not mean your
entire Goal is finished: it may still be waiting for other work. Use the current
revision's state and the evidence required by your spec to assess completion.

| View | Command |
| --- | --- |
| Last N activity rows | `telos logs SESSION_ID --context CONTEXT --tail N` |
| Complete activity history | `telos logs SESSION_ID --context CONTEXT --all` |
| Underlying transcript and evidence events | `telos logs SESSION_ID --context CONTEXT --raw` |
| Newline-delimited event records | `telos logs SESSION_ID --context CONTEXT --json` |

New automatic tool-activity notices, such as “Reading app/main.py,” are recorded
in the structured evidence log without being appended to the shared transcript.
You can inspect those notices with `logs --json`. The transcript still records
agent-written progress updates, implementation and evaluation reports, and
operator updates. Existing transcript entries are preserved.

## Move a persistent Goal forward

Edit `SPEC.md` and compare the proposed contract with the deployed revision.
A version bump is an optional label for a privately staged plan; publishing a
changed named package with `telos push` still requires a new version:

```bash
telos plan SPEC.md --session SESSION_ID --context CONTEXT
telos apply SPEC.md --message "Record book ownership" --session SESSION_ID --context CONTEXT
```

The Goal, session, deployment, and history remain stable. Protected submissions
enter the Change Request queue; unprotected updates apply directly after your
confirmation. Both create an immutable revision with the same lifecycle. [Use Telos](use-telos.md) shows the full diff and receipt.

## Delete a Goal

Check the session and context, and confirm that you want to remove the Cloud
environment and its data before running:

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

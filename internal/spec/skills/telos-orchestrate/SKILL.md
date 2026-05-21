---
name: telos-orchestrate
description: |
  Telos controller runtime. Use when operating a long-running Telos
  system through the session lineage — observing descendants,
  authoring maintenance as task sessions, continuing from
  checkpoints.
metadata:
  category: control-plane
  author: telos
allowed-tools: Bash(telos:*) Bash(kubectl:*) Bash(python3:*) Bash(git:*)
---

# Telos Orchestration

You are the controller — a cycle that compares a bundle's contract
to its live cluster state and keeps them aligned. You do not operate
a single service; you operate Telos itself.

## Role

A controller reads across the bundle's lineage, compares the live
world to the contract, and when the two disagree authors a spec
describing the move it wants to happen. The task session launched
from that spec does the work; the controller observes.

This is the controller's only mutation primitive. The service
account under which controllers run has read across the lineage and
write only inside its own `ns-ctrl-*` namespace. Mutation of any
component — Keycloak, Postgres, cloudflared — happens inside a task
session that targets the right operational surface. The controller's
authority does not transparently transfer; a mis-shaped task is a
bad move, not a runtime error to work around.

## Composition

Product specs are not building blocks. A product should have one
declarative contract, written in prose, and the controller decomposes
that contract at runtime. Do not search the catalogue for a base spec
to extend just because the product contains Postgres, Keycloak, a
tunnel, or a dashboard.

Runtime tasks are operational plans, not product contracts. When a
controller authors a task, the task should name the desired operation
and target surface in prose, then use the relevant skills to do the
work. The important boundary is the real operational surface:
Keycloak content goes through `keycloak-admin`, SQL through
`database-sql`, runtime shape through deployment skills, and
dashboard work through `build-dashboard`.

Legacy `extends:` still exists in the compiler while old fixtures are
being retired. Do not introduce new product dependencies on it.
Prefer one source-of-truth spec plus skills.

## Authority

Authority is runtime-owned, not a public spec knob. Do not put a
capability envelope in task specs. The substrate decides which service
account, namespace lineage, workspace, and API credentials a launched
session receives.

When authoring a move, make the intended operational surface explicit
in prose and skills: Keycloak content through `keycloak-admin`, SQL
through database skills, manifests through deployment skills, and
session lineage through the Telos CLI. If a task cannot reach the
surface it needs, reshape the move or launch context instead of
inventing public authority fields.

## Content vs. shape

Running software has two surfaces the controller's tasks can
reach for: its **shape** (the Kubernetes manifests — Deployments,
Services, PVCs, ConfigMaps) and its **content** (what the software
itself knows about — Keycloak realms, Postgres schemas, the data
behind the API).

Shape lives in workspace YAML and is committed as manifests. Content
lives inside the running service and is mutated through the
service's own API — `keycloak-admin` for Keycloak, `database-sql`
for Postgres. A move that wants to change content through shape
edits (initContainers patching realm config, env vars coercing user
state, sidecars writing into someone else's namespace) is operating
in the wrong surface. The component-specific skills carry the right
surface for each.

## The session lineage

Every task session is parented to the cycle that authored it, not
to the controller itself. The full descendant graph — cycles,
tasks, follow-up tasks — is the controller's frontier. The sessions
directory is shared state, readable directly from the controller's own
filesystem; no separate API is needed to review what a task did.

Paths:

- `$TELOS_SESSION_DIR` — sessions root (shared)
- `$TELOS_SESSION_ID` — this cycle
- `$TELOS_PARENT_SESSION_ID` — the outer controller, if any
- `$TELOS_SOURCE_DIR` — source tree

Each session directory holds a `spec.md` for each move it's
running (the parent manifest's own spec plus any generated
tasks), an `evidence.jsonl` stream of structured verification
events for each, per-turn subdirectories under `turns/` with
`task.md` and `pi-session.jsonl`, and a
`workspace.tar.gz` checkpoint once the move completes. The streams
are append-only — reading them tells you what a task tried, what
it observed, and where it ended up, at the granularity of
individual tool calls if needed.

The `analyze-runs` skill ships the canonical readers over this
shared state:

- `frontier.py --parent $TELOS_SESSION_ID` — descendants
  classified as passing, deepening, frontier, or failing; the
  right first view when surveying your own lineage
- `evidence.py <session_id>` — round-by-round implementation/evaluation
  progress updates, findings, and cost per turn for one session
- `workspace.py <session_id>` — what a task actually wrote:
  GitOps compliance, manifests, git log, file tree
- `scoreboard.py` — high-level status, cost, and round counts
  across many sessions

`telos list --json` is the session-registry view — status, round
counts, timestamps — for top-level enumeration; prefer `frontier.py`
when you already know you're asking about your own descendants,
since broad scans are expensive in large histories.

Your session's `generated/<timestamp>-<slug>/` directories hold
every move you've previously authored — prior moves are the nearest
analogy for the next.

## Artifacts

Artifacts carry the meaning of a cycle.

- **`decisions.md`** — intent. What the controller observed and why
  it made the move it made. The admin-event stream inside components
  captures *what happened*; `decisions.md` captures *why*.
- **Session checkpoints** — handoff. The workspace tarball and
  evidence stream a task produces, treated as the source of truth
  for what actually ran in that move.
- **Live cluster state** — the canonical outcome. Workspace
  manifests are proposals; only what lives in the cluster is real.

Each move's spec lives next to its artifacts under
`generated/<ts>-<slug>/`. One move, one directory, one spec, one
launched task — that's the trail.

## Launching a task

A move is always: write the spec under `generated/`, then run it
with the `telos` CLI. Inside a controller pod, `telos run` sees
`TELOS_API_TOKEN` and `TELOS_SESSION_ID`, calls the local cluster API, and
parents the task to this controller session.

```bash
telos run generated/20260420-rotate-telos-api/spec.md
```

The command returns immediately with the task's `session_id`; the task
runs detached on the same substrate, inheriting the controller's
image cache and namespace lineage. Observe completion through
`frontier.py --parent $TELOS_SESSION_ID` and the task's session
directory; don't block on a foreground run.

A move continuing a previous workspace uses:

```bash
telos run generated/20260420-rotate-telos-api/spec.md \
  --from-workspace /path/to/workspace.tar.gz
```

Outside the cluster, `telos run` remains the operator entrypoint for
launching controller sessions. The same verb means "controller" externally
and "internal task" inside a controller because the controller has the
internal session token and current session id in its environment.

## Related skills

Domain skills carry the operational depth for their respective
surfaces:

- `keycloak-admin` — content mutations against a Keycloak instance
- `database-sql` — PostgreSQL operations
- `k8s-deploy` — greenfield deployment patterns
- `analyze-runs` — choosing among checkpoints and session outcomes

## A worked example

The contract says the `telos-api` client secret rotates every 90
days. The controller observes the live rotation is 95 days old.

It writes
`generated/20260420-rotate-telos-api/spec.md`:

```
---
version: v0
name: rotate-telos-api
---

Rotate the `telos-api` client secret in the `telos` realm and
write the new value into the runtime credential consumed by the
Telos API. Record the rotation timestamp in `decisions.md`.
```

And launches:

```bash
telos run generated/20260420-rotate-telos-api/spec.md
```

The task uses the `keycloak-admin` skill to call the Admin REST API
and updates the runtime credential through the platform secret
surface. The controller observes completion through the task's
session state. `decisions.md` records the intent.

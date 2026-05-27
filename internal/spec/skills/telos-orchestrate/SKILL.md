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
allowed-tools: Bash(telos:*) Bash(python3:*) Bash(git:*)
---

# Telos Orchestration

You are the controller: the long-horizon manager of agent work toward the
spec's goal. Local software materializes as a repo output, binary, artifact,
and executable behavior. Cloud software materializes as a Kubernetes
environment and service behavior. You do not implement everything yourself;
you operate Telos itself.

## Role

A controller reads across the session lineage, compares the live software to
the goal, decides what bounded work is useful, and authors task specs for those
moves. The task sessions launched from those specs do the work; the controller
observes, compares, and delegates the next move.

Task sessions are the controller's durable work primitive. In cloud, the
runtime decides the service account, namespace lineage, and secrets. Locally,
the runtime decides the workspace clone, session root, and process environment.
A mis-shaped task is a bad move, not a runtime error to work around.

## Composition

Product specs are not building blocks. A product or benchmark should have one
declarative goal, written in prose, and the controller decomposes that goal at
runtime. Do not search the catalogue for a base spec to extend just because
the product contains Postgres, Keycloak, a tunnel, a parser, or a dashboard.

Runtime tasks are operational plans, not product goals. When a controller
authors a task, the task should name the desired operation and target surface
in prose, then use the relevant skills to do the work. The important boundary
is the software surface being changed:
Keycloak content goes through `keycloak-admin`, SQL through
`database-sql`, runtime shape through deployment skills, and
dashboard work through `build-dashboard`.

When the goal naturally decomposes, prefer independent child tasks over one
broad task. This is a manager move, not the default move. For a narrow repair,
launch one focused child task. Good splits have minimal shared files or
resources, clear local success evidence, and no need for children to
coordinate while running. For a repo or benchmark, this might mean separate
implementation hypotheses, separate subsystems, separate failure classes, or
probe/fuzz harnesses. For a cloud system, this might mean separate operational
surfaces such as identity, storage, routing, and observability.

After independent children complete, inspect their transcripts, evidence, and
workspace checkpoints. If more work is needed, launch a separate integration
task that merges, cherry-picks, or reconciles the best candidates. The
controller chooses and delegates integration; it does not directly edit the
delivered artifact.

`extends:` is runtime composition, not product hierarchy. In cloud it
targets the same namespace/runtime surface. In local runs it seeds the
child workspace from the parent spec's resolved workspace artifact. Use it
only when the task intentionally builds on a concrete runtime/artifact
lineage; otherwise prefer one source-of-truth spec plus skills.

## Authority

Authority is runtime-owned, not a public spec knob. Do not put a
capability envelope in task specs. The runtime decides which service
account, namespace lineage, workspace, and API credentials a launched
session receives.

When authoring a move, make the intended operational surface explicit
in prose and skills: Keycloak content through `keycloak-admin`, SQL
through database skills, manifests through deployment skills, and
session lineage through the Telos CLI. If a task cannot reach the
surface it needs, reshape the move or launch context instead of
inventing public authority fields.

## Content vs. shape

Running software has surfaces the controller's tasks can reach for. In cloud,
that often means **shape** (Kubernetes manifests such as Deployments,
Services, PVCs, ConfigMaps) and **content** (what the software itself knows
about, such as Keycloak realms, Postgres schemas, or data behind an API). In
local repo or benchmark runs, the surfaces are usually source files, tests,
generated artifacts, command behavior, and benchmark outputs.

Shape lives in workspace YAML and is committed as manifests. Content
lives inside the running service and is mutated through the
service's own API — `keycloak-admin` for Keycloak, `database-sql`
for Postgres. A move that wants to change content through shape
edits (initContainers patching realm config, env vars coercing user
state, sidecars writing into someone else's namespace) is operating
in the wrong surface. The component-specific skills carry the right
surface for each.

## The session lineage

Every task session is parented to the cycle that authored it. The full
descendant graph — cycles, tasks, follow-up tasks — is the controller's
frontier.

Use the CLI as the operator surface:

- `telos list --wide` — visible session registry, including child rows and
  parent/child topology.
- `telos list` — compact top-level view outside controller cycles.
- `telos describe <session-id>` — status, completion reason, evaluation result,
  artifact paths, cost, and round counts.
- `telos logs <session-id>` — Session Transcript; use `--raw` only for full
  protocol text.
- `telos run <spec>` — launch a bounded child task.

The filesystem is the handoff contract. If `TELOS_SESSION_DIR` is present, it
points at the shared sessions root. Each session directory holds metadata,
specs, transcript/evidence, per-turn `task.md` plus `pi-session.jsonl`, and a
`workspace.tar.gz` checkpoint once the move completes. For local repo tasks,
the workspace checkpoint includes git state, so it can be extracted and
inspected as a real candidate checkout. The streams are append-only — reading
them tells you what a task tried, what it observed, and where it ended up.

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
- **Live software** — the canonical outcome. In cloud, workspace manifests are
  proposals and the Kubernetes environment is real. Locally, artifacts,
  command behavior, benchmark output, and running processes are real.

Each move's spec lives next to its artifacts under
`generated/<ts>-<slug>/`. One move, one directory, one spec, one
launched task — that's the trail.

## Launching a task

A move is always: write the spec under `generated/`, then run it with the
`telos` CLI. Inside a controller session, `telos run` uses the controller's
session context and parents the task to this controller session.

```bash
telos run generated/<timestamp>-rotate-telos-api/spec.md
```

The command returns with the task's `session_id`; the task runs in the same
runtime with an isolated workspace and the right lineage. Observe completion
through `telos describe <session-id>`, `telos list`, and the task's session
directory.

If a previously launched child task is pending or running, waiting is the
controller move. Report the child session id and status, sleep for a bounded
interval, then re-check the child. While it remains active, do not launch more
work or broaden probes. Do not convert an active child into a new plan just to
show progress.

The child gets an isolated workspace. Its live workspace may disappear after
checkpointing; the durable handoff is the child session directory:
`session.json` for metadata, transcript/evidence for reasoning and tool
history, and `workspace.tar.gz` for the final filesystem result. In local repo
runs, extract that archive to inspect commits, diffs, tests, and files. Do not
assume the controller's checkout changed because a child task completed.

Outside a controller, `telos run` remains the operator entrypoint for bounded
task sessions and `telos apply` is the persistent controller entrypoint. Inside
a controller, `telos run` is the delegation primitive.

## Related skills

Domain skills carry the operational depth for their respective
surfaces:

- `keycloak-admin` — content mutations against a Keycloak instance
- `database-sql` — PostgreSQL operations
- `k8s-deploy` — greenfield deployment patterns
- `analyze-runs` — choosing among checkpoints and session outcomes

## A worked example

The goal says the `telos-api` client secret rotates every 90 days. The
controller observes the live rotation is 95 days old.

It writes
`generated/<timestamp>-rotate-telos-api/spec.md`:

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
telos run generated/<timestamp>-rotate-telos-api/spec.md
```

The task uses the `keycloak-admin` skill to call the Admin REST API
and updates the runtime credential through the platform secret
surface. The controller observes completion through the task's
session state. `decisions.md` records the intent.

---
version: v0
name: telos-runtime-mental-model
---

# Telos Runtime Mental Model

This document is the frozen mental model for Telos while the implementation
moves from Python to Go.

The Python runtime is the reference for product semantics. The Go runtime is
the durable implementation target. The goal is not a line-by-line port; the goal
is one product model that works locally and in managed cloud environments.

## One Sentence

Telos turns a declarative `SPEC.md` into a durable agent session that keeps
operating until the live world satisfies the spec.

## Product Surface

The public CLI stays small:

```text
telos plan SPEC.md
telos apply SPEC.md
telos run SPEC.md
telos list
telos describe SESSION
telos logs SESSION
telos stop SESSION
telos login
telos --version
```

`apply` is desired-state management. It creates or updates a controller session.

`run` is bounded work. In a controller, `run` creates a child task session. For
local development, `run` also remains the simple way to launch a one-off task.

There is no public daemon command tree, runtime command tree, environment
selection command tree, or explanation command. Runtime and daemon mechanics are
private process plumbing.

## Core Nouns

```text
SPEC.md
  Declarative desired state. It says what must be true, not how to mutate the
  substrate step by step.

Session
  Durable execution record under <root>/sessions/<session_id>. The session is
  the unit users list, describe, log, stop, and update.

Controller
  Persistent long-horizon manager for one spec goal. It observes the live
  world, decides what bounded agent work is useful, writes task specs when work
  is needed, launches tasks through Telos, inspects their artifacts, and keeps
  running on an interval.

Task
  Bounded session. It runs once, produces evidence, and exits.

telos
  Public client binary.

telosd
  Sessions API daemon and session worker runtime. The same implementation runs
  locally and inside a cloud environment.

Control plane
  Managed Telos service for accounts, billing, environment allocation, and user
  environment metadata. It is not the environment-local Sessions API.
```

## System Shape

```text
telos CLI / frontend / controller
        |
        v
Telos Sessions API
        |
        v
telosd
        |
        +-- FileStore at <root>/sessions
        |
        +-- Launcher
              |
              +-- local process worker
              +-- Kubernetes worker pod
        |
        v
session worker
        |
        v
agent loop -> Pi subprocess -> transcript + evidence + workspace
```

Local and cloud differ by adapters:

```text
Local
  transport: Unix socket or loopback HTTP
  auth: local trust
  root: $TELOS_OUTPUT_ROOT/execroot/<workspace-id>
  marker: source-workspace/.telos symlink to that execroot
  launcher: local process
  workspace: session-owned clone/snapshot

Cloud environment
  transport: HTTPS
  auth: bearer tokens
  root: /telos-state
  launcher: Kubernetes worker pod
  workspace: mounted durable volume
```

The API shape does not change between the two.

## Spec Shape

Public specs are markdown with small YAML frontmatter:

```markdown
---
version: v0
name: postgres
platform: cloud
interval: 4h
skills:
  - k8s-deploy
  - database-sql
  - verify-engineering*
---

# Postgres

Run a managed PostgreSQL service...
```

The public spec surface is intentionally small:

```text
version
name
platform
interval
skills
tags
extends        runtime composition: cloud namespace lineage, local artifact seed
```

Public specs do not declare `capabilities`. Authority comes from session kind,
caller role, and internal runtime policy.

For local sessions, `extends` resolves the parent spec's content hash to a
completed local workspace artifact and records the exact session/archive in
`session.json.workspace.extends`. For cloud sessions, `extends` targets the
same namespace lineage/runtime surface.

`skills` are operating and verification guidance. A skill suffixed with `*`
means the evaluator must grade against it as a required rubric.

## Session Kinds

`session_kind` is explicit and persisted:

```text
controller
task
```

Creation rules:

```text
telos apply SPEC.md                    -> controller
telos apply SPEC.md, existing name      -> update existing controller
telos run SPEC.md                       -> task
controller-originated run with parent   -> child task
```

`parent_session_id` is lineage only. It is not how kind is derived.

Controllers are persistent. Tasks are bounded. This distinction is the main
runtime primitive.

## Authority Model

Authority is internal. Users should not reason about Kubernetes verbs or runtime
capability envelopes when writing product specs.

```text
operator caller
  user or control-plane authority for an environment
  can create/update/list/read/stop sessions according to product policy

controller caller
  session-scoped token for one controller
  can read itself and descendants
  can launch bounded child task sessions
  should not directly patch product resources as its normal mutation primitive

task caller
  session-scoped token for one task
  can read itself and write its own evidence/runtime state
```

Substrate authority follows the same model:

```text
controller worker
  observes live state
  launches task sessions through the Sessions API
  runs controller agent cycles

task worker
  performs the bounded operation requested by its task spec
  writes evidence and exits
```

The clean model is "controller owns the goal, task sessions perform bounded
work." Any implementation path that gives a controller broad direct
product-mutation authority is transitional and should be tightened.

## Sessions API

The Sessions API is the stable boundary spoken by the CLI, frontend, and
controllers:

| Method | Route | Meaning |
| --- | --- | --- |
| `GET` | `/api/healthz` | server health |
| `POST` | `/api/sessions` | create a session from raw spec markdown |
| `GET` | `/api/sessions` | list visible sessions |
| `GET` | `/api/sessions/{id}` | describe one session |
| `GET` | `/api/sessions/{id}/spec` | read mutable controller spec |
| `PUT` | `/api/sessions/{id}/spec` | replace mutable controller spec |
| `POST` | `/api/sessions/{id}/stop` | stop a session |
| `GET` | `/api/sessions/{id}/transcript` | read transcript text |
| `GET` | `/api/sessions/{id}/events` | read or stream evidence events |
| `GET` | `/api/sessions/{id}/workspace/{spec}` | read workspace archive |

Create accepts raw markdown:

```json
{
  "spec_markdown": "SPEC.md contents",
  "session_kind": "controller",
  "parent_session_id": "optional lineage pointer",
  "model": "claude-opus-4-6",
  "thinking": "medium",
  "until": 5,
  "max_cost_usd": 25.0,
  "agent_timeout_sec": 0
}
```

Product catalogue lookup, local file reading, and UI spec selection happen
before this request. The Sessions API receives the exact spec text to run.
Local workspace selection is a launcher concern; it is recorded in
`session.json.workspace`, not accepted as a Sessions API config field.

`session_kind` is explicit for new callers: `controller` for persistent desired
state and `task` for bounded work. If omitted, the server may apply
compatibility defaults for old clients.

## Session State

The filesystem is authoritative:

```text
<root>/sessions/<session_id>/
  session.json
  runner.log
  workspace/
  specs/<spec_name>/
    spec.md
    evidence.jsonl
    transcript-<session_id>.md
    workspace.tar.gz
    turns/
```

`session.json` stores identity, kind, runtime, lineage, config, access, specs,
epochs, and runner identity.

The public `Session` response is a projection of persisted state plus derived
facts such as status, artifact existence, and latest descendant.

## Status Model

```text
pending
running
completed
failed
stopped
stale
```

Terminal statuses:

```text
completed
failed
stopped
stale
```

Status is derived from epochs and runner identity:

```text
no epochs                         -> pending
open local epoch with live pid     -> running
open cloud epoch with runner pod   -> running
open epoch with missing runner     -> stale
last result completed             -> completed
last result failed                -> failed
last result stopped               -> stopped
```

The live system and evidence remain the source of truth for product progress.

## Controller Updates

`PUT /api/sessions/{id}/spec` replaces the controller's primary `spec.md`.

Rules:

- valid only for controller sessions;
- spec name is immutable;
- exact markdown is persisted;
- spec version increments;
- the controller is woken or relaunched with update context;
- existing live product state is not reset by the update itself.

This is the API shape behind `telos apply` when a controller already exists.

## Runtime Artifact

The fast-moving Telos runtime is a versioned native binary:

```text
telos-darwin-arm64
telos-darwin-amd64
telos-linux-arm64
telos-linux-amd64
```

It contains:

```text
public CLI
telosd server
session worker
agent-loop runtime
Pi adapter
prompt and skill data
```

`telos-agent` is separate. It is the slow-moving cloud tool substrate:

```text
pi
kubectl
helm
opentofu
node
browser tooling
database clients
system inspection tools
```

Cloud workers stage the current Telos binary into the `telos-agent` pod and run
that binary there. The heavy tool image should not be rebuilt for ordinary runtime
or CLI changes.

## Control Plane Boundary

The managed control plane owns:

```text
accounts
auth
billing
environment allocation
environment lifecycle
environment metadata
API tokens
```

An environment-local `telosd` owns:

```text
session create/list/get/update/stop
transcripts
events
workspace artifacts
session worker launch
environment-local topology/product handles
```

The cloud repo can keep the control plane and frontend. The runtime path should
move toward the Go `telosd` implementation.

## Python To Go Freeze

Freeze these Python semantics:

- raw spec markdown is the session create input;
- public specs have no `capabilities`;
- `session_kind` is explicit;
- `parent_session_id` is lineage;
- controller sessions are mutable through spec update;
- task sessions are bounded and immutable;
- local and cloud speak the same Sessions API routes;
- transcripts, evidence, and workspace paths keep their existing shape;
- controllers launch child tasks through Telos, not by directly editing product
  resources as their normal path.

Port these semantics into Go. Do not preserve Python package boundaries,
namespace layout, or incidental implementation details.

## Current Transitional State

Today the system is partly through the cutover:

```text
Python OSS runtime
  reference semantics for local CLI/runtime behavior

Go runtime
  portable CLI, telosd, local runtime, cloud client, worker runtime, release
  artifact path

Cloud repo
  control plane, frontend, environment provisioning, current Python
  cluster-api, and managed product adapters
```

The final product model is `apply` for persistent controllers and `run` for
bounded tasks. Any cloud path where top-level `run` still creates a controller
is compatibility during the cutover, not the model to preserve.

The target state:

```text
Go telosd
  local Sessions API
  cloud environment Sessions API
  session worker runtime
  Kubernetes worker launcher

Cloud repo
  control plane
  frontend
  provisioning
  billing
  managed product UX
```

## Design Invariants

- One mental model: spec -> session -> worker -> evidence.
- One user-facing CLI.
- One environment-local Sessions API.
- One durable session filesystem shape.
- One runtime enum: `local` or `cloud`.
- No public `capabilities`.
- No public daemon or worker command surface.
- No product catalogue dependency inside the Sessions API.
- No per-turn implementation/evaluation Kubernetes pods in the final cloud path.
- No Python runtime bundle as the long-term product substrate.

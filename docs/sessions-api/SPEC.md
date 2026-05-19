# Telos Sessions API

## Goal

Telos has one session contract.

The public `telos` CLI speaks the Sessions API. Local and cloud runtimes both
implement that API through `telosd`. The implementation substrate may differ,
but the client-facing shape should not.

```text
telos CLI / frontend / controllers
          |
          v
Telos Sessions API
          |
          v
telosd + FileStore
          |
          v
<root>/sessions/<session_id>/
```

## Boundaries

`internal/sessionapi` owns:

- JSON request and response models for the Sessions API.
- Route registration for the HTTP API.
- The persisted `session.json` manifest shape.
- Conversion from persisted manifest state into public `Session` responses.
- File-backed session storage for local and cloud `telosd` deployments.

`internal/sessionapi` does not own:

- CLI rendering.
- Cloud control-plane environment provisioning.
- Kubernetes deployment logic.
- Product catalogue lookup.
- Agent execution internals beyond persisted runner identity.

## Process Shape

`telos` is the public client binary.

`telosd` is the runtime binary. It is configured by one YAML file:

```text
telosd --config /etc/telos/telosd.yaml
```

With no config, `telosd` uses a local developer default:

```yaml
kind: telosd.config.v1
mode: local
root: .telos
auth:
  type: local
server:
  transport: unix
  socket: .telos/run/telosd.sock
  idle_seconds: 300
```

Cloud deployments set `mode: cloud` in config:

```yaml
kind: telosd.config.v1
mode: cloud
root: /telos-state
auth:
  type: bearer
server:
  transport: http
  listen: 0.0.0.0:8000
```

The current server registers the same Sessions API routes for both modes.
Mode changes storage root, default transport, runtime labeling, and access
policy. It should not change the API shape.

Auth semantics:

- `auth.type: local` trusts the caller. It is intended for `0600` Unix sockets.
- `auth.type: bearer` requires `Authorization: Bearer <token>`.
- The operator token comes from `auth.token`, falling back to `TELOS_API_TOKEN`
  for the current cloud deployment bridge.
- Session-scoped tokens are stored in `session.json` under `access` and carry
  explicit scopes.
- Billing and metering attach to accepted API calls and runtime usage later;
  they do not change the bearer-token validity rules.

## API Routes

The canonical route set is:

| Method | Route | Response |
| --- | --- | --- |
| `GET` | `/api/healthz` | `{"ok":"true"}` |
| `POST` | `/api/sessions` | `Session` |
| `GET` | `/api/sessions` | `SessionListResponse` |
| `GET` | `/api/sessions/{id}` | `Session` |
| `GET` | `/api/sessions/{id}/spec` | `SessionSpecResponse` |
| `PUT` | `/api/sessions/{id}/spec` | `Session` |
| `POST` | `/api/sessions/{id}/stop` | `Session` |
| `GET` | `/api/sessions/{id}/transcript` | plain text |
| `GET` | `/api/sessions/{id}/events` | `SessionEventsResponse` or SSE |
| `GET` | `/api/sessions/{id}/workspace/{spec}` | workspace archive |

`POST /api/sessions` accepts JSON only. Request bodies are capped at 4 MiB and
unknown request fields are rejected.

## Create Request

`SessionCreateRequest` is the create-session input:

```json
{
  "spec_markdown": "SPEC.md contents",
  "parent_session_id": "optional lineage pointer",
  "model": "claude-opus-4-6",
  "thinking": "medium",
  "max_rounds": 20,
  "max_cost_usd": 25.0,
  "agent_timeout_sec": 1800,
  "workspace": "optional runtime workspace path"
}
```

Create semantics:

- `spec_markdown` is required.
- The server compiles the markdown through a temporary `SPEC.md`.
- The server persists the exact markdown into the session as
  `<root>/sessions/<session_id>/specs/<spec_name>/spec.md`.
- Product catalogue lookup, local file reading, and UI spec selection happen
  before this API call. They are not Sessions API concerns.
- `parent_session_id` is lineage only. It is not a kind discriminator.

## Public Session Shape

`Session` is the public projection returned by create, get, list, and stop:

```json
{
  "session_id": "local_20260518_120000_00",
  "session_kind": "task",
  "parent_session_id": null,
  "spec_name": "redis",
  "status": "running",
  "created_at": "2026-05-18T16:00:00.000Z",
  "runtime": "local",
  "launcher": "local",
  "session_spec_path": "/repo/.telos/sessions/.../specs/redis/spec.md",
  "session_dir": "/repo/.telos/sessions/local_...",
  "config": {
    "model": "claude-opus-4-6",
    "thinking": "medium",
    "max_rounds": 20,
    "agent_timeout_sec": 1800,
    "workspace": "/repo/.telos/sessions/.../workspace"
  },
  "provenance": {
    "mode": "local"
  },
  "specs": [],
  "epochs": [],
  "spec_versions": []
}
```

Required public concepts:

- `session_id`: stable identity.
- `session_kind`: `controller` or `task`.
- `status`: lifecycle state.
- `runtime`: `local` or `cloud`.
- `specs`: compiled or persisted spec artifacts.
- `epochs`: run attempts.
- `config`: user/runtime knobs that are safe to expose.
- `provenance`: small public origin metadata.

`runtime` must be `local` or `cloud`. `hosted` is not part of the current
contract.

## Enums

Session status:

```text
pending
running
scheduled
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

Runtime:

```text
local
cloud
```

Session kind:

```text
controller
task
```

## Public Spec Shape

Each public `SessionSpec` describes one spec artifact inside a session:

```json
{
  "index": 0,
  "name": "redis",
  "dir_name": "redis",
  "session_spec_path": "/repo/.telos/sessions/.../specs/redis/spec.md",
  "content_hash": "sha256-or-current-compiler-hash",
  "evidence_path": "/repo/.telos/sessions/.../specs/redis/evidence.jsonl",
  "evidence_exists": true,
  "transcript_path": "/repo/.telos/sessions/.../pvg-transcript-local_....md",
  "transcript_exists": true,
  "workspace_path": "/repo/.telos/sessions/.../specs/redis/workspace.tar.gz",
  "workspace_exists": true,
  "interval_seconds": 21600
}
```

Existence fields are derived at read time from the filesystem.

## Events

`GET /api/sessions/{id}/events` returns evidence-derived events:

```json
{
  "events": [
    {
      "event": "agent_complete",
      "spec_index": 0,
      "spec_name": "redis",
      "spec_dir_name": "redis",
      "data": {}
    }
  ]
}
```

With `Accept: text/event-stream`, the same event objects stream as Server-Sent
Events:

```text
data: {"event":"agent_complete","spec_name":"redis","data":{}}

: keep-alive
```

The current stream implementation polls the file-backed store every second,
emits newly observed events by index, and stops after the session reaches a
terminal status.

## State Root

The file-backed store uses:

```text
<root>/sessions/<session_id>/
  session.json
  runner.log
  workspace/
  specs/<spec_name>/
    spec.md
    evidence.jsonl
    pvg-transcript-<session_id>.md
    workspace.tar.gz
    turns/
```

For local `telosd`, `<root>` is `.telos` by default.

For cloud `telosd`, `<root>` is `/telos-state` by default.

The manifest path is always:

```text
<root>/sessions/<session_id>/session.json
```

## Persisted Manifest Shape

`session.json` is the durable state record. It is not identical to the public
`Session` response; the public response is derived from it.

```json
{
  "session_id": "local_20260518_120000_00",
  "session_kind": "task",
  "runtime": "local",
  "created_at": "2026-05-18T16:00:00.000Z",
  "launcher": "local",
  "parent_session_id": null,
  "session_spec_path": "/repo/.telos/sessions/.../specs/redis/spec.md",
  "spec_name": "redis",
  "config": {
    "model": "claude-opus-4-6",
    "thinking": "medium",
    "max_rounds": 20,
    "max_cost_usd": 25.0,
    "agent_timeout_sec": 1800,
    "workspace": "/repo/.telos/sessions/.../workspace"
  },
  "provenance": {
    "mode": "local"
  },
  "specs": [],
  "epochs": []
}
```

Known `config` fields are typed:

```text
model
thinking
max_rounds
max_cost_usd
agent_timeout_sec
workspace
```

Unknown config fields are preserved across decode/encode for forward
compatibility.

## Persisted Spec Shape

Each manifest spec is persisted as:

```json
{
  "index": 0,
  "name": "redis",
  "dir_name": "redis",
  "session_spec_path": "/repo/.telos/sessions/.../specs/redis/spec.md",
  "content_hash": "compiler-content-hash",
  "evidence_path": "/repo/.telos/sessions/.../specs/redis/evidence.jsonl",
  "transcript_path": "/repo/.telos/sessions/.../specs/redis/pvg-transcript-local_....md",
  "workspace_path": "/repo/.telos/sessions/.../specs/redis/workspace.tar.gz",
  "interval_seconds": 21600
}
```

The first spec is currently the execution target for worker loops and transcript
lookup.

## Epoch Shape

An epoch is one run attempt:

```json
{
  "id": 1,
  "started_at": "2026-05-18T16:00:00.000Z",
  "finished_at": null,
  "result": null,
  "error": null,
  "runner": {}
}
```

Completed epoch:

```json
{
  "id": 1,
  "started_at": "2026-05-18T16:00:00.000Z",
  "finished_at": "2026-05-18T16:05:00.000Z",
  "result": "completed",
  "error": null,
  "runner": {}
}
```

Result values currently used:

```text
completed
failed
stopped
```

## Runner Shape

Local detached worker:

```json
{
  "kind": "local-subprocess",
  "pid": 12345,
  "pgid": 12345,
  "log_path": "/repo/.telos/sessions/.../runner.log",
  "in_cluster": false,
  "started_at": "2026-05-18T16:00:00.000Z"
}
```

Cloud/in-cluster worker:

```json
{
  "kind": "kubernetes-pod",
  "pid": 1,
  "pgid": 1,
  "log_path": "/telos-state/sessions/.../runner.log",
  "in_cluster": true,
  "hostname": "controller-abc",
  "pod_name": "controller-abc",
  "pod_namespace": "ns-ctrl-abc",
  "started_at": "2026-05-18T16:00:00.000Z"
}
```

Runner identity is persisted so stop/status derivation can reason about the
active executor without encoding substrate details into the public API.

## Status Derivation

Public status is derived from the manifest:

| Manifest state | Public status |
| --- | --- |
| No epochs | `pending` |
| Open epoch with `runner.in_cluster=true` | `running` |
| Open epoch with live local `runner.pid` | `running` |
| Open epoch without a live local runner | `stale` |
| Last epoch result `completed` | `completed` |
| Last epoch result `failed` | `failed` |
| Last epoch result `stopped` | `stopped` |
| Closed epoch without result | `completed` |

Current cloud status treats an open in-cluster runner as running based on the
manifest identity. It does not query Kubernetes from `sessionapi`.

## Stop Semantics

`POST /api/sessions/{id}/stop`:

- Returns the current session unchanged if it is already terminal.
- If an epoch is open, attempts to terminate the local runner process group.
- Marks the latest epoch as:

```json
{
  "finished_at": "now",
  "result": "stopped",
  "error": "stopped by operator"
}
```

If no epoch exists, stop creates a closed stopped epoch.

The current stop implementation is local-process-aware. Cloud pod termination is
expected to be handled by the cloud substrate around `telosd`, not by
`sessionapi` itself.

## Store Interface

The storage contract is:

```go
type Store interface {
    Create(req SessionCreateRequest) (*Session, error)
    Spec(id string) (*SessionSpecResponse, error)
    UpdateSpec(id string, req SessionSpecUpdateRequest) (*Session, error)
    List() ([]Session, error)
    Get(id string) (*Session, error)
    Stop(id string) (*Session, error)
    Transcript(id string) (string, error)
    Events(id string) ([]SessionEvent, error)
    WorkspacePath(id string, specName string) (string, error)
}
```

`FileStore` is the current implementation. It is mutex-protected inside one
process and uses atomic manifest writes via `session.json.tmp` then rename.

## Client Behavior

The public CLI resolves sessions in this order:

1. Local `FileStore`.
2. Controller session context, when running inside a controller.
3. Cloud environment Sessions API, when `--env` is provided or cloud config is
   present.

Cloud environment clients call the same session routes. Environment lookup and
environment access token recovery happen through the control plane before
talking to the environment-local Sessions API.

## Invariants

- `sessionapi` is the canonical shape for session JSON.
- Local and cloud runtimes use the same route set.
- The persisted manifest is the source of truth for session identity, lineage,
  specs, config, epochs, and runner identity.
- The live system and evidence files are the source of truth for progress.
- Public `Session` responses are projections; they may include derived fields
  such as status and artifact existence.
- `parent_session_id` is lineage only.
- `session_kind` is explicit and should not be inferred from parentage.
- `runtime` is `local` or `cloud`.

## Non-Goals

- `sessionapi` should not become a Kubernetes client.
- `sessionapi` should not provision environments.
- `sessionapi` should not render CLI output.
- `sessionapi` should not know product catalogue semantics.
- The public `telos` CLI should not expose daemon/worker subcommands.

## Current Design Pressure

- Cloud stop semantics need a substrate-level owner for terminating worker pods.
- Event streaming is intentionally simple polling over evidence files; it may
  later become offset-aware or notification-driven.
- The first-spec assumption is still present in transcript and worker interval
  handling.
- `Session.Epochs` remains a public loose JSON projection even though persisted
  epochs are now typed.

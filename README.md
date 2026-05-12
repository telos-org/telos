# telos-go

First locally runnable Go implementation of the portable Telos runtime core.

This is the clean foundation for the eventual single Telos binary that runs
both locally and inside hosted worker pods.

## What works today

- **Sessions API** - HTTP handlers implementing the canonical Telos Sessions API
  contract: create, list, get, stop, transcript, events, and workspace.
- **File-backed store** - Sessions persisted as JSON manifests under
  `.telos/sessions/{id}/session.json`, matching the Python on-disk format.
- **CLI** - `telos run`, `telos list`, `telos logs`, `telos describe`,
  `telos stop` backed by the local file store.
- **Status derivation** - Session status (`pending`, `running`, `completed`,
  `failed`, `stopped`) is derived from epoch state, matching the Python runtime.
- **Python manifest compatibility** - Manifests written by the Python runtime
  can be read by the Go store and vice versa.

## How it maps to the Python implementation

| Go package | Python equivalent | Notes |
|---|---|---|
| `internal/sessionapi` types | `telos.session.api` | Same JSON contract: `Session`, `SessionSpec`, `SessionEvent`, etc. |
| `internal/sessionapi` store | `telos.session.manifest` + `telos.local.sessions` | File-backed store with manifest read/write and status derivation |
| `internal/sessionapi` server | `telos.cluster.sessions` + `telos.cluster.artifacts` | Route handlers for the Sessions API |
| `cmd/telos` | `telos.cli` | CLI entrypoint with `run/list/logs/describe/stop` |

## Routes

```
POST /api/sessions                         Create a session
GET  /api/sessions                         List all sessions
GET  /api/sessions/{id}                    Get a session
POST /api/sessions/{id}/stop               Stop a session
GET  /api/sessions/{id}/transcript         Get PVG transcript
GET  /api/sessions/{id}/events             Get evidence events
GET  /api/sessions/{id}/workspace/{spec}   Get workspace archive
```

## Build and test

```bash
go build ./...
go test ./...
```

## Run the CLI

```bash
go run ./cmd/telos run path/to/SPEC.md
go run ./cmd/telos list
go run ./cmd/telos describe SESSION_ID
go run ./cmd/telos logs SESSION_ID
go run ./cmd/telos stop SESSION_ID
```

## Not yet implemented

- PVG loop (prover/verifier game execution)
- Pi process adapter (subprocess execution)
- Worker supervision and process launching
- Kubernetes launcher
- Self-update
- Auth adapters (local trust, hosted tokens)

These are scoped for future work. The current slice establishes the Sessions API
contract and file-backed store as the foundation that local and hosted
deployments share.

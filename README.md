# Telos

Telos turns `SPEC.md` into verified operated software.

A spec describes the desired outcome. Telos runs agents against the live system,
records evidence, and keeps iterating until the observable behavior satisfies
the spec.

This repository contains the public source-available runtime: one portable
`telos` binary for local execution, hosted session submission, and
environment-local Sessions API serving through `telosd`.

## Install

```bash
curl -fsSL https://usetelos.ai/install.sh | sh
```

The installer downloads the latest checksummed release artifacts for your
platform and installs `telos` and `telosd` into `~/.local/bin` by default.

## Use

Write a `SPEC.md` that states the outcome you want:

```markdown
# Postgres

Run PostgreSQL with durable storage, a public dashboard, and evidence that the
database accepts authenticated SQL connections.
```

Launch it:

```bash
telos apply SPEC.md --env env_123
```

Inspect the session:

```bash
telos list
telos describe <session-id>
telos logs <session-id>
telos stop <session-id>
```

For local execution, use `run`:

```bash
telos run SPEC.md --workspace .
```

Local live runs require `pi` on `PATH` and model credentials configured for Pi.
Hosted runs require `telos login`.

## Runtime Model

```text
SPEC.md -> telos CLI -> telosd -> session -> PVG -> live system
```

The same spec acts as both the outcome contract and the grading rubric. The
prover tries to make the world satisfy the spec; the verifier reads the same
spec as judgment criteria and rejects weak satisfaction.

Sessions persist evidence, transcripts, workspaces, product handles, status,
and progress events. The live system remains the source of truth.

The runtime mental model is documented in
[`docs/sessions-api/SPEC.md`](docs/sessions-api/SPEC.md).

## Develop

Use Bazel as the canonical build and release path:

```bash
bazel test //...
bazel build //cmd/telos:telos //cmd/telosd:telosd
scripts/build-release.sh v0.0.0
```

Native Go commands are useful for quick local checks:

```bash
go test ./...
go build ./cmd/telos ./cmd/telosd
```

## Package Map

| Go package | Purpose |
|---|---|
| `cmd/telos` | Public CLI |
| `cmd/telosd` | Sessions API daemon and session runtime |
| `internal/spec` | SPEC.md loading, skill resolution, prompt rendering |
| `internal/game` | PVG loop, state, transcript rendering |
| `internal/executor` | Pi executor and JSON event parsing |
| `internal/platform` | Local subprocess execution and workspace snapshots |
| `internal/evidence` | Evidence JSONL writer/reader |
| `internal/sessionapi` | Shared Sessions API types, store, HTTP routes |
| `internal/cloud` | Cloud Sessions API client |
| `internal/config` | `~/.telos` config compatibility |
| `internal/cli` | Local session creation and run orchestration |

## Boundary

The Go runtime is the canonical environment-local runtime. Managed Telos
environments run `telosd --config /etc/telos/telosd.yaml`; the cloud repo keeps
the hosted control plane, frontend, provisioning, and billing surface.

Python Telos remains useful as historical reference while the product surface
hardens, but new runtime work should land here.

## License

Functional Source License, Version 1.1, ALv2 Future License. See
[LICENSE](LICENSE).

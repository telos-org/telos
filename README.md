# telos-go

Standalone Go port of the Telos OSS runtime.

The goal is one portable `telos` binary for local execution and hosted
Sessions API compatibility. The Go runtime does not import or execute the
Python Telos package.

## What Works

- `telos plan`, `run`, `list`, `describe`, `logs`, `stop`, `login`, `version`.
- SPEC.md parsing with YAML frontmatter and markdown body.
- Built-in prompt and skill embedding with `go:embed`.
- Skill resolution, including emphasized verifier skills such as
  `verify-engineering*`.
- Prover-verifier game loop.
- Pi JSON-mode executor integration.
- Local process execution with process-group timeout handling.
- File-backed sessions under `.telos/sessions/<session_id>`.
- Evidence JSONL, PVG transcript, runner turns, and workspace checkpoints.
- Local Sessions API route handlers.
- Hosted Sessions API client models.

## Package Map

| Go package | Purpose |
|---|---|
| `cmd/telos` | CLI entrypoint |
| `internal/spec` | SPEC.md loading, skill resolution, prompt rendering |
| `internal/game` | PVG loop, state, transcript rendering |
| `internal/executor` | Pi executor and JSON event parsing |
| `internal/platform` | Local subprocess execution and workspace snapshots |
| `internal/evidence` | Evidence JSONL writer/reader |
| `internal/sessionapi` | Shared Sessions API types, store, HTTP routes |
| `internal/hosted` | Hosted Sessions API client |
| `internal/config` | `~/.telos` config compatibility |
| `internal/cli` | Local session creation and run orchestration |

## Build And Test

Use Bazel as the canonical build and release path:

```bash
bazel test //...
bazel build //cmd/telos:telos
scripts/build-release.sh 0.1.0
```

Native Go commands are still useful for quick local sanity checks:

```bash
go build ./cmd/telos
go vet ./...
go test ./...
```

## Run

```bash
go run ./cmd/telos plan path/to/SPEC.md
go run ./cmd/telos run path/to/SPEC.md --workspace .
go run ./cmd/telos list
go run ./cmd/telos describe SESSION_ID
go run ./cmd/telos logs SESSION_ID
go run ./cmd/telos stop SESSION_ID
```

Local live runs require `pi` on `PATH` and model credentials configured for Pi.
The test suite uses fake executors and does not require live model credentials.

## Current Caveats

This is a first full-port pass, not a release candidate. The core local runtime
is present, but it still needs human hardening before it should replace the
Python OSS CLI by default:

- hosted environment selection needs parity review against the Python CLI;
- CLI code should be split out of `cmd/telos/main.go` as it grows;
- ignored filesystem errors should be tightened in a few orchestration paths;
- release signatures/provenance should be added before broad public install.

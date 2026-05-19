# telos-go

Standalone Go port of the Telos OSS runtime.

The goal is one portable `telos` binary for local execution and cloud
Sessions API compatibility. The Go runtime does not import or execute the
Python Telos package.

## What Works

- `telos plan`, `apply`, `run`, `list`, `describe`, `logs`, `stop`, `login`, `--version`.
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
- Cloud Sessions API client models.
- Cloud environment selection with raw SPEC.md submission.

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

## Build And Test

Use Bazel as the canonical build and release path:

```bash
bazel test //...
bazel build //cmd/telos:telos //cmd/telosd:telosd
scripts/build-release.sh v0.0.0
```

Native Go commands are still useful for quick local sanity checks:

```bash
go build ./cmd/telos ./cmd/telosd
go vet ./...
go test ./...
```

## Run

```bash
go run ./cmd/telos plan path/to/SPEC.md
go run ./cmd/telos apply path/to/SPEC.md --env env_123
go run ./cmd/telos run path/to/SPEC.md --workspace .
go run ./cmd/telos list
go run ./cmd/telos describe SESSION_ID
go run ./cmd/telos logs SESSION_ID
go run ./cmd/telos stop SESSION_ID
go run ./cmd/telos --version
```

Local live runs require `pi` on `PATH` and model credentials configured for Pi.
The test suite uses fake executors and does not require live model credentials.

## Current Caveat

This is a first full-port pass, not a release candidate. The core local runtime
is present, but it still needs human hardening before it should replace the
Python OSS CLI by default:

- cloud cutover is still pending; the current Go `telosd` covers the
  local API and session-worker paths.

---
version: v0
name: telos-go-port
platform: local
skills:
  - backend-design*
  - verify-engineering*
---

# Telos Go Port

## Goal

Build a standalone Go implementation of the Telos OSS runtime.

The output is not a skeleton. The output is a locally runnable `telos` binary
that can replace the Python OSS CLI/runtime for the core local product loop:

```bash
telos plan SPEC.md
telos run SPEC.md
telos list
telos describe SESSION
telos logs [-f] SESSION
telos stop SESSION
telos login
telos version
```

The Go binary must not import, execute, or require the Python Telos package at
runtime.

## Scope

All writes must stay inside this `telos-go` workspace.

Use these sibling repositories only as references:

- `/Users/rohangupta/Desktop/Developer/telos-org/telos`
- `/Users/rohangupta/Desktop/Developer/telos-org/cloud`
- `/Users/rohangupta/Desktop/Developer/telos-org/spec.md`

Do not edit either Python repository.

## Required Outcome

Produce a Go module with a binary entrypoint at `cmd/telos`.

The binary must implement the OSS Telos local runtime end to end:

- SPEC.md loading with YAML frontmatter and markdown body.
- Spec validation and stable compiled spec metadata.
- Skill resolution from embedded built-in skills and explicit filesystem paths.
- Emphasized verifier skills using the existing trailing `*` syntax.
- Prompt rendering for prover and verifier using embedded prompt templates.
- The prover-verifier game loop.
- Pi executor integration using JSON mode.
- Local workspace execution with process-group stop and deadline handling.
- Session persistence under `.telos/sessions/<session_id>`.
- Evidence JSONL persistence.
- PVG transcript rendering.
- Workspace checkpointing.
- Local Sessions API routes.
- CLI commands for plan, run, list, describe, logs, stop, login, and version.
- Hosted Sessions API client compatibility for the same commands when an
  environment is selected.
- Hosted configuration compatibility with the Python OSS CLI:
  `~/.telos/config.yaml`, `~/.telos/environments.yaml`, `TELOS_CONFIG`,
  `TELOS_ENVIRONMENTS_CONFIG`, `TELOS_API_ENDPOINT`, and `TELOS_AUTH_TOKEN`.

The implementation must preserve the public product model:

- A session is the public unit of work.
- A spec is the desired-state contract.
- The transcript is the primary human and agent-readable log.
- Evidence JSONL is the replay/debug stream.
- Local and hosted differ by adapters, not by product semantics.

## Runtime API

Implement the canonical Sessions API in Go with JSON shapes matching the
Python OSS `telos.session.api` models:

```text
POST /api/sessions
GET  /api/sessions
GET  /api/sessions/{id}
POST /api/sessions/{id}/stop
GET  /api/sessions/{id}/transcript
GET  /api/sessions/{id}/events
GET  /api/sessions/{id}/workspace/{spec}
```

The local implementation may run in-process for `telos run`, but the API
contract must be real and testable. The hosted adapter must use the same
request/response models.

## Packaging And Hermeticity

The binary should be suitable for `curl | sh` distribution later:

- Prefer the Go standard library.
- Use `go:embed` for built-in prompts and built-in skills.
- Do not require Python, uv, Bazel, Node, Docker, Kubernetes, or cloud
  credentials for local execution.
- Do not shell out to the Python Telos implementation.
- Keep runtime state under `.telos` in the selected workspace.
- Build with `go build ./cmd/telos`.

External model execution may use the `pi` executable when present on PATH. If
`pi` is missing, the CLI must fail with a clear error that says how execution is
blocked. Tests must not require live model credentials.

## CLI Semantics

Implement these user-facing commands:

```text
telos plan SPEC.md [--json]
telos run SPEC.md [--workspace DIR] [--env ENV] [--model MODEL]
  [--thinking EFFORT] [--max-rounds N] [--max-cost-usd USD]
  [--agent-timeout-sec SEC] [--json]
telos list [--env ENV] [--limit N] [--all] [--wide] [--local] [--hosted] [--json]
telos describe SESSION [--env ENV] [--json]
telos logs [-f] SESSION [--env ENV]
telos stop SESSION [--env ENV] [--json]
telos login [--endpoint URL] [--token TOKEN] [--no-prompt]
telos version
```

The human output should be direct and agent-friendly. Machine output should be
JSON only when `--json` is passed.

Do not add a public runtime command tree. Internal service modes may exist, but
they must not become the product UX.

## Minimum Local Behavior

A local run must:

1. Create an isolated session workspace.
2. Copy or materialize the submitted spec under the session directory.
3. Compile the spec, resolve skills, and render prover/verifier prompts.
4. Execute the prover through Pi.
5. Execute the verifier through Pi.
6. Continue until verifier concession, failure, max rounds, max cost, stop, or
   timeout.
7. Persist session manifest, evidence JSONL, transcript, runner log, and
   workspace checkpoint.
8. Let `telos logs`, `telos describe`, `telos list`, and `telos stop` inspect
   or mutate that session without Python.

## Tests

Add deterministic tests that do not call live models:

- Spec/frontmatter parsing.
- Skill resolution, including emphasized `skill*`.
- Prompt rendering includes the right skills in the right roles.
- Pi JSON event parsing and malformed event handling.
- PVG loop behavior with fake prover/verifier executors.
- Session persistence and status derivation.
- Local API route JSON compatibility.
- CLI command behavior against a fake executor.
- Deadline and stop behavior for local subprocess execution.
- Workspace checkpointing.

The test suite must pass with:

```bash
go test ./...
go vet ./...
go build ./cmd/telos
```

## Verification

The verifier should reject the result unless:

- The binary builds without Python.
- `go test ./...` passes.
- `go vet ./...` passes.
- The implementation includes the PVG loop, not only the Sessions API.
- The implementation includes Pi executor integration, not only a fake executor.
- A deterministic fake-executor CLI smoke test proves `run -> describe -> logs
  -> list -> stop` behavior.
- Session artifacts match the Python OSS artifact shape closely enough for a
  human to inspect them the same way.
- Hosted and local share one Sessions API model in code.
- No file outside `telos-go` was created or modified.

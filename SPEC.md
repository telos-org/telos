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

Create the first locally runnable Go implementation of the portable Telos
runtime core in this workspace.

This is not a rewrite of every Python feature. It is the clean foundation for
the eventual single Telos binary that can run locally and inside hosted worker
pods.

## Scope

All writes must stay inside this `telos-go` workspace.

Use the sibling Python repositories only as references:

- `/Users/rohangupta/Desktop/Developer/telos-org/telos`
- `/Users/rohangupta/Desktop/Developer/telos-org/cloud`
- `/Users/rohangupta/Desktop/Developer/telos-org/spec.md`

Do not edit either Python repository.

## Required Outcome

Produce a Go module that is easy to run, test, and extend:

- `go.mod` at the workspace root.
- `cmd/telos` as the binary entrypoint.
- `internal/sessionapi` containing the canonical JSON request and response
  types for the Telos Sessions API.
- `internal/sessionapi` route registration for the core session routes:
  `POST /api/sessions`, `GET /api/sessions`,
  `GET /api/sessions/{id}`, `POST /api/sessions/{id}/stop`,
  `GET /api/sessions/{id}/transcript`,
  `GET /api/sessions/{id}/events`, and
  `GET /api/sessions/{id}/workspace/{spec}`.
- A small local file-backed session store under `.telos/sessions`.
- Tests proving the JSON shape matches the Python Sessions API contract for
  create, list, get, stop, transcript, events, and workspace metadata.
- A short `README.md` explaining the current runnable slice and how it maps to
  the Python implementation.

## Design Constraints

Keep the implementation boring and hermetic:

- Prefer the Go standard library.
- Do not shell out to Python.
- Do not require Kubernetes, Docker, Bazel, Node, or cloud credentials.
- Do not introduce a public runtime command tree.
- Keep public UX centered on `telos run`, `telos list`, `telos logs`,
  `telos describe`, and `telos stop`.
- Internal server/worker roles may exist in code, but they should not become
  product-facing commands.

## Runtime Direction

The Go binary should be shaped so it can eventually serve both deployments:

- local loopback `telosd`;
- hosted environment Sessions API.

Local and hosted should differ by adapters for auth, store, launcher,
workspace, and transport. They should not diverge at the product model.

## Non-Goals

Do not port the PVG loop yet.

Do not implement Pi execution yet.

Do not implement self-update yet.

Do not create a Kubernetes launcher yet.

Do not add placeholder abstraction layers that only rename one call.

## Verification

The verifier should reject the result unless:

- `go test ./...` passes.
- The route and JSON contract is visible in tests, not only in prose.
- The module can be inspected without reading the Python repos first.
- The implementation is small enough that the core shape is obvious.
- No file outside `telos-go` was created or modified.

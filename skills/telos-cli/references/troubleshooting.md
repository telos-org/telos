---
title: Troubleshooting
description: Start from the observed symptom, inspect the decisive state, and return useful evidence.
group: Reference
---

# Troubleshooting

Start with the command that can distinguish the observed symptom:

| Symptom | First check | Decisive evidence |
| --- | --- | --- |
| `telos` is unavailable | `command -v telos` | Binary path or missing installation |
| A local run will not start | `telos plan SPEC.md` | Platform or spec validation error |
| Cloud authentication or target is wrong | `telos config` | Authentication and active context |
| An agent or CI job waits for browser login | Check the job's authentication setup against [token authentication](cloud.md#token-authentication-for-agents-and-ci) | A supplied `TELOS_AUTH_TOKEN` lets the job run Cloud commands directly |
| A spec is rejected | `telos plan SPEC.md` | First validation error |
| A skill publish is rejected | Read the original `push` error and inspect the local frontmatter | Invalid bundle input or immutable-version conflict |
| A deployment is not `ready` | `telos describe SESSION_ID --context CONTEXT --json` | Status, digest, and reason |
| A nested run is rejected | `telos plan CHILD_SPEC.md` plus the original run error | Child platform/spec error or unavailable parent capability |

## Command not found

If `command -v telos` returns nothing, verify that
`${TELOS_INSTALL_DIR:-$HOME/.local/bin}` is on `PATH`. Re-run the checksummed
installer when the binary is absent or not the intended release.

## Local run cannot start

`telos plan` identifies malformed frontmatter or a platform mismatch. A local
run requires `platform: local`, `pi` on `PATH`, and an authenticated provider.
`telos run --help` shows the model, thinking, cycle, time, and cost flags
supported by the installed release.

## Cloud authentication or context is wrong

`telos config` shows whether authentication is valid and which context the CLI
will use without displaying the token. Pass the intended `--context`
explicitly through the Cloud workflow.

For an agent or CI job, supply a valid Telos API token as `TELOS_AUTH_TOKEN`
and run the Cloud command directly. `TELOS_TOKEN` is not a supported alias.
Running `telos login` can still request browser approval because it checks
saved credentials rather than the environment token. See
[Cloud authentication](cloud.md#authenticate) for setup and a CI example.

A non-empty `TELOS_AUTH_TOKEN` takes precedence over the saved login. If it
is rejected, replace or unset that override before retrying; signing in again
will not change the environment value. Without an injected token, use
`telos login` when a person can approve it in a browser. An unattended job
needs its token supplied before it can proceed.

## Plan or publish rejects a spec or skill

The first validation error usually identifies the contract boundary: YAML
frontmatter, semantic version, non-empty instructions, safe file paths, or an
exact registry ref. A rejected `push` is already the evidence; retry after
correcting its cited input. If a registry version exists with different
content, publish the changed bytes under a new version.

## Deployment is not `ready`

Compare `package_digest` with the revision from the `apply` receipt, then read
the status reason and logs. This separates active work, revision rejection,
runtime failure, and public-service failure. Continue the same session when it
contains an actionable failure; create another deployment only for a distinct
Goal.

The lifecycle's [compatibility note](lifecycle.md#compatibility-note)
explains why some older deployments lack digest-bound status provenance.
Regardless of provenance, verify the live behavior promised by every service
spec.

## Nested run is rejected

Nested execution supports `telos run`, not `telos apply`. The original run
error identifies whether the parent capability or a bound was rejected;
`telos plan CHILD_SPEC.md` checks the child's frontmatter and platform without
launching it. The child file must be reachable in the parent's workspace and
declare `platform: local`.

## Return diagnostic evidence

Use `--json` when exact fields matter. Return the installed version, context,
session ID, revision digest, status, reason, and the smallest relevant log
slice. Credential files, bearer tokens, and unrestricted environment dumps are
not diagnostic evidence.

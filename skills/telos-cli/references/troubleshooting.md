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
| A local run will not start | `telos plan SPEC.md`, then `git status --short` in a Git source | Invalid spec, dirty source, or missing local pi setup |
| Cloud authentication or target is wrong | `telos config` | Authentication and active context |
| An agent or CI job waits for browser login | Check the job's authentication setup against [token authentication](cloud.md#token-authentication-for-agents-and-ci) | A supplied `TELOS_AUTH_TOKEN` lets the job run Cloud commands directly |
| A Cloud status or log check loses its connection | Repeat the read with the same session and context | Connection error and the next successful status or log response |
| A spec is rejected | `telos plan SPEC.md` | First validation error |
| A skill publish is rejected | Read the original `push` error and inspect the local frontmatter | Invalid bundle input or immutable-version conflict |
| A deployment is not `ready` | `telos describe SESSION_ID --context CONTEXT --json` | Status, digest, and reason |
| A Cloud update is rejected | Read the original `apply` error | Inference override, missing snapshot, or another update conflict |
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

A successful plan does not validate the source checkout or provider login.
If the run reports a dirty Git source, include the intended changes in a
commit or select a clean checkout containing the source you want to run.
A newly created spec inside the repository also counts as an untracked file;
you can keep it outside the source checkout and pass its path instead.
[Bounded runs](bounded-runs.md) explains source preparation and result retrieval.

## Cloud authentication or context is wrong

Check `telos config` for authentication status and the active context, then
pass the intended `--context` explicitly.

If an injected `TELOS_AUTH_TOKEN` is rejected, replace or unset it; browser
login does not change the override. If an unattended job waits for browser
approval, supply a valid token and run the Cloud command directly. See
[Cloud authentication](cloud.md#authenticate) for setup, precedence, and a CI
example.

## Cloud status or log checks lose their connection

Telos automatically retries brief connection interruptions while reading Cloud
session lists, session details, logs, and account information for your context.
Each read makes up to three attempts with short, increasing, randomized waits.
The attempts and waits share the normal 30-second timeout for that read; a
command can perform more than one read. This recovery also covers a connection
that drops partway through a response.

If you still receive a connection error, repeat the read with the same session
and context. A failed status or log check does not by itself mean the remote
session stopped. Authentication failures and other API error responses are
returned without retrying. Session creation, updates, deletion, and login token
claims are not automatically resubmitted by this recovery mechanism.

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

## Cloud update is rejected

An update keeps the session's inference settings. If the error reports a model
or thinking override, clear environment overrides for that invocation:

```bash
telos apply SPEC.md --session SESSION_ID --context CONTEXT \
  --model "" --thinking ""
```

These flags preserve the existing selection. See
[Models and inference](inference.md#update-and-inspect-a-deployment).

If the error reports a missing snapshot, the current revision continues
serving. Continuing without that snapshot means you cannot restore its exact
workspace and runtime state. To accept that loss and proceed, retry the same
update with `--force`:

```bash
telos apply SPEC.md --session SESSION_ID --context CONTEXT --force
```

`--force` only bypasses the missing-snapshot gate for an existing Cloud session.
Active operations, authorization, runtime availability, and stale-revision
protection still apply. For any other rejection, follow the original error
before retrying.

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

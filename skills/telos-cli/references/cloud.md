# Telos Cloud

Authenticate once, inspect the active target, and select a team only when the
user intends one:

```bash
telos login
telos config
telos config --context @team-handle
```

Use `personal` to return to the personal context. Never guess the target from a
repository name or prior session.

A Cloud spec declares `platform: cloud` and is launched with:

```bash
telos plan SPEC.md
telos apply SPEC.md
```

`apply` publishes the exact package and creates the managed deployment. Record
the returned package ref, digest, and session ID. New deployments use the
platform's current default runtime unless the control plane explicitly pins a
different release; existing deployments retain their stored runtime version
and digest.

Inspect Cloud state with:

```bash
telos list --cloud
telos describe SESSION_ID
telos logs SESSION_ID
```

Cloud readiness is a projection of telosd reconciliation. A deployment becomes
ready only after the runtime has reconciled the matching package digest and the
verifier has accepted it. Do not interpret allocation success, an HTTP process
starting, or stale events as goal acceptance.

To update an existing deployment, retrieve or edit its spec, bump the immutable
package version, inspect the diff, and apply to the same session:

```bash
telos get SESSION_ID --output SPEC.md
telos plan SPEC.md --session SESSION_ID
telos apply SPEC.md --session SESSION_ID
```

Deletion is consequential. Confirm the exact session and preservation intent
before `telos delete SESSION_ID`; do not use existing deployments as rollout
canaries.

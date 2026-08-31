# Telos Cloud

Authenticate once, inspect the active target, and select a team only when the
user intends one:

```bash
telos login
telos config
telos config --context @team-handle
telos list --context @team-handle
```

Use `personal` to return to the personal context. For one invocation, `--context` takes
precedence over `TELOS_CONTEXT` and stored configuration without mutating either.

A persistent Cloud Goal declares `platform: cloud` in its spec and is launched
with:

```bash
telos plan SPEC.md
telos apply SPEC.md
```

`apply` publishes the exact package and creates the managed deployment. Record
the returned revision digest and session ID.

Inspect Cloud state with:

```bash
telos list --cloud
telos describe SESSION_ID
telos logs SESSION_ID
```

A deployment becomes `ready` only after the matching revision has been
reconciled and accepted. Do not interpret allocation success, an HTTP process
starting, or stale events as Goal acceptance. `Ready` is revision scoped: it
confirms the current package, not a future drift check or repair.

For an agent-readable acceptance check:

```bash
telos describe SESSION_ID --json | jq -e '.status == "ready"'
```

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

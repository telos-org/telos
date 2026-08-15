# Goals and execution modes

## Write the contract

A new `SPEC.md` should usually have this shape:

```markdown
---
name: short-stable-name
version: 0.1.0
platform: local
---

# Goal

State the observable outcome, what existing behavior or state must survive,
and the evidence that proves success.
```

Use a DNS-compatible lowercase name and semantic version. Set `platform` to
`local` for work on the current machine or repository, and `cloud` for a
managed deployment.

Keep the goal declarative. Specify stable interfaces, security boundaries,
persistence, compatibility, and failure behavior when they matter. Avoid a
step-by-step implementation recipe.

## Choose run or apply

Use `telos run` for bounded work with a natural finish, such as implementing a
feature, investigating a bug, or producing an artifact. Give it an explicit
cycle, time, or cost bound when practical.

Use `telos apply` for a controller that should retain identity and reconcile a
desired state over time. Cloud specs are started and updated with `apply`.

Before launch:

```bash
telos plan SPEC.md
```

For an existing controller, compare the proposed contract to the deployed one:

```bash
telos plan SPEC.md --session SESSION_ID
telos apply SPEC.md --session SESSION_ID
```

Do not use parentage to infer task versus controller behavior. `run` creates a
bounded task; `apply` creates or updates a controller. A parent session ID is
lineage only.

## Observe acceptance

The command returning a session ID means the work was accepted, not completed.
Use:

```bash
telos describe SESSION_ID
telos logs SESSION_ID
```

Treat implementation completion plus verifier acceptance as the goal result.
For a service, also probe its public behavior. Tests or logs do not substitute
for the live contract they claim to verify.

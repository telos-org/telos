# Goals, specs, and execution modes

A Goal is the outcome that should remain true. `SPEC.md` expresses that Goal as
an executable contract. Sessions and agents are replaceable attempts to satisfy
the current revision of the contract.

## Write the contract

A new `SPEC.md` should usually have this shape:

```markdown
---
name: short-stable-name
version: 0.1.0
platform: cloud
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

## Choose apply or run

Use `telos apply` for a persistent Goal that should retain identity across
implementation attempts and spec revisions. Cloud specs are started and
updated with `apply`.

Use `telos run` as the bounded execution subsystem. For a human, it performs
one imperative piece of work within a limit. For an agent, it satisfies a
bounded declarative subgoal and returns evidence. Give it an explicit cycle,
time, or cost bound when practical.

Before launch:

```bash
telos plan SPEC.md
```

For an existing controller, compare the proposed contract to the deployed one:

```bash
telos plan SPEC.md --session SESSION_ID
telos apply SPEC.md --session SESSION_ID
```

## Observe acceptance

The command returning a session ID means the work was accepted, not completed.
Use:

```bash
telos describe SESSION_ID
telos logs SESSION_ID
```

Require the observed session state to confirm acceptance of the current
revision. For a managed Goal, require Cloud to report `ready`. For a service,
also probe its public behavior. Tests or logs do not substitute for the live
contract they claim to verify.

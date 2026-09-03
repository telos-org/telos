---
title: Write a SPEC.md
description: Express a Goal as an observable contract and choose the execution lifecycle it needs.
group: Concepts
---

# Write a `SPEC.md`

A Goal is the outcome that should remain true. `SPEC.md` is its authored
contract: frontmatter tells Telos how to run it, while the Markdown body tells
agents what to make true and how success can be observed.

The Telos installer includes a conversational `telos-spec` skill. Invoke it
explicitly with `$telos-spec` in Codex or `/telos-spec` in Claude Code when you
want your coding agent to create, improve, or review this contract with you.

## Start with a complete minimal contract

```markdown
---
name: short-stable-name
version: 0.1.0
platform: cloud
---

# Goal

State the observable outcome and the behavior that must remain true.

# Acceptance

- Name the evidence that demonstrates the outcome.
```

This file is valid as written. Add the other frontmatter fields only when the
Goal uses them:

| Field | Meaning |
| --- | --- |
| `name` | Required lowercase, DNS-compatible identity. Keep it stable across revisions. |
| `version` | Required semantic version for this immutable revision. Bump it when the contract changes. |
| `platform` | `cloud` for a managed persistent Goal or `local` for a bounded run. Omitted values currently resolve to Cloud; explicit is clearer. |
| `skills` | A path or YAML list of paths and exact registry refs. Relative paths resolve from the spec directory. A trailing `*` makes a skill an acceptance rubric. |
| `interval` | A positive duration ending in `s`, `m`, or `h`, such as `30m` or `6h`, carried as the contract's reconciliation interval. |
| `tags` | A YAML list of string labels. The default is an empty list. |

For example:

```yaml
skills:
  - path/to/local-skill
  - "@scope/readiness:1.0.0*"
interval: 6h
tags:
  - production
```

The body is Markdown, not a fixed form schema. `# Goal` and `# Acceptance` are
useful conventions rather than specially parsed fields. Add sections for
interfaces, compatibility, data lifecycle, security boundaries, failure
behavior, or evidence when they change what a correct result means.

## Express the contract, not an implementation recipe

A useful spec names observable behavior and leaves implementation choices open
where several designs would satisfy it. For example:

```markdown
# Goal

Run a public reading-list service. Books remain available when the application
restarts.

# Acceptance

- Add a book, restart the application, and retrieve the same book.
```

That contract permits the agent to choose an appropriate framework and
datastore. A framework, schema, deployment shape, or compatibility requirement
belongs in the spec when it is itself part of the promised outcome.

A spec can select a lifecycle, import capabilities and rubrics, and describe
the desired state. It does not grant credentials, network access, registry
permissions, or a platform capability. Confirm those surfaces separately
before applying a Cloud Goal; [Telos Cloud](cloud.md) describes that preflight.

## Choose `apply` or `run`

| Need | Spec | Command |
| --- | --- | --- |
| A managed outcome that keeps its identity and evolves | `platform: cloud` | `telos apply` |
| One local result with a stopping bound | `platform: local` | `telos run` |

Follow [Use Telos](use-telos.md) for a persistent service or
[Bounded runs](bounded-runs.md) for a local result. The identity created by each
path is explained in [The Goal lifecycle](lifecycle.md).

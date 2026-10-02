---
title: Write a SPEC.md
description: Express a Goal as an observable contract and choose the execution lifecycle it needs.
group: Concepts
---

# Write a `SPEC.md`

A Goal is the outcome that should remain true. `SPEC.md` is its authored
contract: frontmatter tells Telos how to run it, while the Markdown body tells
agents what to make true and how success can be observed.

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
| `egress` | HTTPS destinations with `host`, optional `credentials` ID, and optional `methods` and `paths` restrictions. |

For example:

```yaml
skills:
  - ./skills/product
  - "@scope/readiness:1.0.0*"
interval: 6h
tags:
  - production
```

Use local directories containing `SKILL.md` or exact registry refs. The `*`
marks a required acceptance rubric; directory names have no rubric semantics.
See [Packages and skills](packages-and-skills.md) for the skill format and
how local references are packaged.

The body is Markdown, not a fixed form schema. `# Goal` and `# Acceptance` are
useful conventions rather than specially parsed fields. Add sections for
interfaces, compatibility, data lifecycle, security boundaries, failure
behavior, or evidence when they change what a correct result means.

## Declare external access

For a Cloud Goal that calls external services, add destinations to its frontmatter:

```yaml
egress:
  - host: public.example.com
  - host: api.stripe.com
    credentials: sec-stripe-example
    methods: [GET]
    paths: [/v1/customers, /v1/customers/*]
```

Replace the example ID with a saved ID from `telos credentials list` in the
deployment's workspace. Names and secret values stay in the workspace credential
store, not in this file. Existing `sec_` IDs remain valid alongside new `sec-` IDs.

- `host` is a DNS hostname without a URL scheme. Credentials require an exact
  hostname, not a wildcard.
- `credentials` selects authentication for that destination. Omit it for
  network-only access. The same ID can be used on multiple approved hosts.
- `methods` and `paths` restrict that destination, not the shared credential.
  Omitting either, or using `[]`, means all methods or all paths. Do not write
  a literal `*` list item for these defaults.
- Use uppercase HTTP methods. Credential paths are exact paths or prefixes
  ending in `/*`. A goal cannot expand the credential's provider-side permissions.

`egress` describes the complete desired deployment access. Applying a revision
removes entries left out. Remove all goal-declared access with:

```yaml
egress: []
```

This does not delete reusable workspace credentials or remove platform-managed
inference access. A new Goal with no custom access can omit the declaration.
When updating a deployment with custom access, declare the desired access
explicitly. Nonempty declarations require a Cloud Goal.

Older goals using paired `integrations` and `allowlist` fields remain supported.
Do not mix those fields with `egress`. Their separate lists do not identify
which credential belongs to which host; choose the bindings explicitly when
converting them.

See [Telos Cloud](cloud.md#credentials-and-external-access) for credential
setup, backend support, permissions, and runtime limits.

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

A Goal can select a lifecycle, import skills and rubrics, and declare deployment
credentials and network access. On a supporting Cloud backend, applying the
revision configures that access after authorization checks. The declarations
do not contain secret values, grant registry permissions, or create missing
platform capabilities. [Telos Cloud](cloud.md) describes those boundaries.

## Choose `apply` or `run`

| Need | Spec | Command |
| --- | --- | --- |
| A managed outcome that keeps its identity and evolves | `platform: cloud` | `telos apply` |
| One local result with a stopping bound | `platform: local` | `telos run` |

Follow [Use Telos](use-telos.md) for a persistent service or
[Bounded runs](bounded-runs.md) for a local result. The identity created by each
path is explained in [The Goal lifecycle](lifecycle.md).

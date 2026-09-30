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
| `integrations` | Workspace integration IDs, or `{ id, name }` entries. Declare together with `allowlist`. |
| `allowlist` | HTTPS rules with `host`, optional `methods`, and optional `paths`. Declare together with `integrations`. |

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

## Declare integrations and network access

For a Cloud Goal that calls an external API, add both fields to its frontmatter:

```yaml
integrations:
  - id: sec_stripe_example
    name: Stripe Production
allowlist:
  - host: api.stripe.com
    methods: [GET]
    paths: [/v1/customers, /v1/customers/*]
```

Replace the example ID and name with values from `telos integrations list` in
the deployment's workspace. The ID selects the integration; the name is a
readable label that Cloud checks against the saved name. ID-only entries such
as `integrations: [sec_stripe_example]` also work. Secret values stay in the
workspace integration, not in this file.

Use a DNS host without a URL scheme and uppercase HTTP methods. Restrict paths
to the requests your Goal needs. Omitting `methods` or `paths`, or setting
either to `[]`, leaves that part of the rule unrestricted. An allowlist rule
permits a request; it does not supply credentials. The integration's credential
policy must also support an authenticated request.
Use `integrations: []` when the request needs no credentials.

These lists describe the complete desired deployment access, not additions to
an earlier list. Applying a revision removes entries left out of the lists.
Use both empty lists to remove all Goal-declared integrations and network rules:

```yaml
integrations: []
allowlist: []
```

This does not delete the reusable workspace integrations or remove
platform-managed inference credentials. A new Goal with no custom access can
omit both fields. When updating a deployment that has custom access, Cloud
requires both fields explicitly. Nonempty declarations require a Cloud Goal.

See [Telos Cloud](cloud.md#integrations-and-network-access) for credential
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
integrations and network access. On a supporting Cloud backend, applying the
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

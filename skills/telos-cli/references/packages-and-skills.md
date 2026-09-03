---
title: Packages and skills
description: Publish immutable Telos artifacts, consume exact refs, and use skills as capabilities or rubrics.
group: Platform
---

# Packages and skills

Telos stores exact, versioned inputs so a revision can be reconstructed and
verified later.

| Term | Meaning |
| --- | --- |
| Package | An immutable compiled spec and its locked skill dependencies. |
| Ref | A human-readable registry identity and version, such as `@scope/name:1.0.0`. |
| Digest | The content identity of the exact bytes used by a revision. |
| Rubric | A skill that an independent verifier must use when deciding whether the revision passes. |

## Publish

Resolve the intended scope, name, and immutable version, present them to the
user, and obtain approval before publishing.

Publish a spec package:

```bash
telos push SPEC.md --scope your-scope
```

Publish a skill directory containing `SKILL.md`:

```bash
telos push path/to/skill --scope your-scope
```

The release installer may place a root `.telos-managed` ownership marker in a
bundled skill. `telos push` excludes that local installer metadata from the
published skill.

The version can come from frontmatter or an explicit `--version`. Registry
versions are immutable; changed bytes receive a new version. Capture the
returned ref and digest.

## Consume

Import an exact registry skill from `SPEC.md`:

```yaml
skills:
  - "@scope/skill-name:0.1.0"
```

The skill guides implementation. Add a trailing `*` when the same skill is
also part of acceptance:

```yaml
skills:
  - "@scope/service-readiness:1.0.0*"
```

Here, an independent verifier evaluates the result using
`service-readiness`; the revision becomes accepted only if that rubric passes.

Pull an immutable package for inspection or reuse:

```bash
telos pull @scope/package-name:0.1.0
telos apply @scope/package-name:0.1.0 --context CONTEXT
```

Use `telos get SESSION_ID --context CONTEXT` when the starting point is a
session rather than a known registry ref. Telos verifies registry digests
before materializing packages and skills. `apply` deploys an exact registry
package without materializing or republishing it. [The Goal lifecycle](lifecycle.md)
explains how that package digest identifies a revision.

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

## Author local skills

`SPEC.md` defines your Goal and imports skills. Each skill is a directory with
its own `SKILL.md`, following the [Agent Skills format](https://agentskills.io/specification).
Use `skills/<name>/SKILL.md` as the convention for new local skills. For example,
`skills/readiness/SKILL.md` can contain:

```markdown
---
name: readiness
description: Verify the service's public behavior and persistence before acceptance.
---

Check the public endpoint and confirm saved records survive a restart.
```

Keep supporting files inside the skill directory and reference them relative
to that directory, such as `references/checks.md`. The skill's `name` must
match its directory name.

Import local skills from your `SPEC.md`:

```yaml
skills:
  - ./skills/product
  - "./skills/readiness*"
```

Paths resolve relative to `SPEC.md`. Other directory layouts are supported;
the trailing `*`, not a folder named `rubrics`, makes passing a rubric required.
The `skills:` imports and `*` marker belong to Telos's spec contract, not the
Agent Skills format.

When local imports need relocation, Telos rewrites the bundled spec's imports
to exact skill refs for registry packages or `./skills/<name>` for embedded
packages. Required-rubric markers and other frontmatter values are preserved,
including values shared through YAML aliases. Already-portable specs keep
their original bytes so republishing unchanged inputs retains their digest.
Your authored files and the contents of each skill remain unchanged. You do
not need to rearrange your source directories to match the package layout.

## Publish

Choose the intended scope, name, and immutable version before publishing.

Publish a spec package:

```bash
telos push SPEC.md --scope your-scope
```

Publish a skill directory containing `SKILL.md`:

```bash
telos push path/to/skill --scope your-scope
```

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
explains how that package digest identifies a revision. `telos get` retrieves
the immutable input package; use `telos download SESSION_ID` when you need the
files from the agent's saved Cloud workspace instead.

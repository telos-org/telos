# Telos Spec Schema

The Go parser in `internal/spec` is authoritative. Inspect it when repository
code and this reference disagree.

## Recommended Frontmatter

```yaml
---
name: short-dns-compatible-name
version: 0.1.0
platform: local
---
```

- `name` is required, begins with a lowercase letter, contains only lowercase
  letters, digits, and hyphens, and is at most 63 characters.
- `version` is required semantic versioning, including optional prerelease or
  build metadata.
- `platform` is `local` or `cloud`. The compiler currently defaults an omitted
  platform to cloud, but new specs should state it explicitly.

Optional fields:

```yaml
interval: 6h
tags:
  - infrastructure
extends: ../base/SPEC.md
skills:
  - implementation-skill
  - "@telos/verify-engineering:0.1.0*"
```

- `interval` is a positive integer followed by `s`, `m`, or `h`.
- `tags` is a YAML list.
- `extends` is a path to another spec, resolved relative to this spec.
- `skills` accepts one string or a list of local paths, package-local names, or
  supported pinned registry references.
- A trailing `*` marks a skill as a required evaluation rubric.

`package_version` is retired; use `version`. `schema` may appear in older
catalogue specs but is not part of the current parser's behavioral contract.
Do not add either field to new specs.

## Body

The Markdown body must be non-empty. The parser does not require a heading, but
new user-authored specs should use one H1, normally `# Goal`.

Frontmatter is package and scheduling metadata. Put behavioral requirements in
the body rather than inventing frontmatter keys the parser will ignore.

## Skills

A package-local skill can be referenced by name when it lives under a
`skills/<name>/SKILL.md` directory next to the spec. Use exact registry versions
for published skills when reproducibility matters.

Starred skills change evaluation behavior. Use them only when passing the
skill's rubric is part of the intended definition of success.

## Validation

Use the intended binary, session, and context:

```bash
telos plan path/to/SPEC.md
telos plan path/to/SPEC.md --session sess_...
```

For a private or organization context, supply the context required by the
current CLI configuration.

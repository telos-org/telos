# Packages and skills

Telos publishes immutable, versioned artifacts. The source file or skill
directory remains the authoring source; the registry stores the exact bundle
and content digest used by managed runtimes.

Publish a spec package:

```bash
telos push SPEC.md --scope your-scope
```

Publish a skill directory containing `SKILL.md`:

```bash
telos push path/to/skill --scope your-scope
```

The skill version may come from its frontmatter or an explicit `--version`.
Never reuse a version for different bytes. Capture the returned ref and digest.

Consume an exact registry skill from `SPEC.md`:

```yaml
skills:
  - "@scope/skill-name:0.1.0"
```

A trailing `*` makes the skill a required verifier rubric. Use it only when
passing that rubric is part of the contract.

Pull an immutable package for inspection or reuse:

```bash
telos pull @scope/package-name:0.1.0
telos apply @scope/package-name:0.1.0
```

Use `telos get SESSION_ID` when the starting point is a session rather than a
known registry ref. Telos verifies registry digests before materializing
packages and skills. `apply` verifies and deploys an exact registry package
without materializing or republishing it.

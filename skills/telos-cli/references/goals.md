---
title: Goals
description: An overview of the framework behind Telos — the goal specification, skills, rubrics, and packages.
group: Platform
---

Telos is built on top of Goals as the durable unit of work. But a Goal is not *just* a markdown file. There are a few components to the programming system — the goal specification, skills, rubrics, and packages — designed to make it flexible and maximally useful to the end user.

## The goal specification

The goal specification (`SPEC.md`) is the entrypoint to a Telos program. We'll walk through an example below:

```markdown
---
name: a-descriptive-name
version: 0.1.0
skills:
 - foo-skill
 - bar-skill
 - baz-rubric*
interval: 6h
---

<your-goal-here>

```

### Frontmatter

The markdown frontmatter (YAML block at the top of `SPEC.md`) supports the following fields:

| Field | Meaning |
| --- | --- |
| `name` | Required lowercase, DNS-compatible identity. Keep it stable across revisions. |
| `version` | Required semantic version for this immutable revision. Bump it when the spec changes. |
| `skills` | A path or YAML list of paths and exact registry refs. Relative paths resolve from the spec directory. A trailing `*` makes a skill an acceptance rubric. |
| `interval` | The schedule on which the system wakes up to check the Goal against the live state. |
| `network` | [Outbound network configuration](environment.md#egress) for the Goal. |
| `tags` | A YAML list of string labels. The default is an empty list. |

### The entrypoint

Telos translates the goal specification to a live software service and continuously reconciles the service against the desired specification.

Within this file, describe the service you want. Focus on the high-level intent of the system, observable surfaces, API endpoints, functional and non-functional requirements, and other visible constraints.

Implementation details, such as choice of programming language, code structure, etc., should be intentionally omitted. The focus is entirely on system design.

Writing high-quality goal specs is a non-trivial problem! Our recommendation is for users to focus on describing externally-visible outcomes: API design, constraints, and behaviors, as opposed to implementation details.

## Skills

A single `SPEC.md` is often not enough to describe your Goal in full detail. Telos lets you modularize with [agent skills](https://agentskills.io/), specified in YAML frontmatter up top. Agent skills are great to provide the system with additional capabilities or expertise or alternatively simply organize information into modular components that could be reused.

A few examples include:
- a company design system specific skill (logos, assets, CSS, and prose)
- a third-party API reference skill (e.g., Salesforce, JIRA, GitHub)
- a submodule of the desired service (e.g., backend API system of a multi-component service)

In each case, the skills serve different purposes — bundling information, providing expertise, and organizing information.

## Rubrics

Rubrics are skills that are marked with a trailing asterisk (`*`). These skills are treated as required acceptance criteria that an independent evaluator agent can enforce and grade against. Rubrics are important for a few reasons:

- skills can provide additional context, but are ultimately primarily informational as opposed to binding
- agents are non-deterministic processes that may sometimes skip instructions or terminate early
- agents tend to grade their own work favorably and have blind spots

Independent evaluation lets the system work persistently until an arbitrary set of criteria — including the spec and rubrics — is verified.

Human organizations exhibit similar tendencies too — there's a reason students don't grade their own homework or why developer teams enforce peer code review!

## Packaging

Having defined the goal specification (`SPEC.md`), skills, and rubrics, a natural next question is the packaging and distribution of these artifacts. Telos supports two kinds of packages:

- skill packages, with just a skill
- spec packages, with a spec and its skills

The naming convention adopted for both is of the form `@<context>/<name>:<version>`.

Spec packages can reference skill packages. For example, suppose we have a skill `@telos/how-to-fish:1.0.2`, a Goal's frontmatter could reference it as follows:

```markdown
---
name: undersea-survival
version: 0.1.0
skills:
 - "@telos/how-to-fish:1.0.2"
 - another-skill
interval: 6h
---

...

```

Telos Cloud manages a hosted registry of packages of both kinds. The method for interaction with this hosted registry is simple:

Use `telos push <SPEC.md|skill-dir>` to push up your package, optionally with an explicit `--context` (that is otherwise derived from your default in `telos config`).

```console
$ telos push SPEC.md
pushed undersea-survival

Ref       @alice/undersea-survival:0.1.0
Digest    sha256:3158b3a4fbe9577b43bab574bb725f8ecebf4ec86c89bc896a0696a2ca17800d
Version   0.1.0
```

To fetch a package locally, for inspection or modification, you can run `telos pull @<context>/<name>:<version>`.

```console
$ telos pull @telos/how-to-fish:1.0.2
pulled @telos/how-to-fish:1.0.2

Package   @telos/how-to-fish:1.0.2
Digest    sha256:b474a99a2705e23cf905a484ec6d14ef58b56bbe62e9292783466ec363b5072d
Path      how-to-fish
```

A skill folder holds the skill's `SKILL.md` and any files it bundles:

```text
how-to-fish/
├── SKILL.md
└── references/
    └── knots.md
```

## A mental model

On framework design: you might notice some similarities to the C programming language. You could conceptualize a `SPEC.md` as equivalent to a `main.c` and skill files like `#include`d headers.

Framed this way, the purposes and shapes of the primitives in the Telos ecosystem should feel a lot more familiar, and you can even go as far as to treat skill packages as reusable "libraries" and spec packages as "binaries"!

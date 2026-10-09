---
title: Goals
description: "An overview of the framework behind Telos: the goal specification, skills, rubrics, and packages."
group: Platform
---

A Telos program is a goal specification. Applying it creates a Goal, a persistent object with an ID, a status, a cloud environment, and logs. You can update a Goal in place with `telos apply SPEC.md --goal <ID>`.

Telos has four primitives. The goal specification is the core, and the other three build on it:

| Primitive | What | Analog |
| --- | --- | --- |
| goal specification (`SPEC.md`) | the desired state of your service | the program's entry point |
| skills | context and expertise the agents load when they need it | imports, libraries |
| rubrics (`*`) | skills an independent evaluator must pass before `ready` | tests that must pass |
| packages | versioned specs and skills in a registry | published packages |

## The goal specification

The goal specification (`SPEC.md`) is the entry point to a Telos program.

```markdown
---
name: reading-list
version: 0.1.0
skills:
  - api-guidelines                     # a skill
  - "@acme/service-readiness:1.0.0*"   # a rubric
interval: 6h                           # scheduled wake-up cycle
---

# Goal

Run a public service for a shared reading list.

- `POST /books` adds a title.
- `GET /books` returns the current list.
- Books remain available when the application restarts.

# Acceptance

- Add a title, restart the application, and confirm that `GET /books` still
  returns it.
```

### Frontmatter

The markdown frontmatter (YAML block at the top of `SPEC.md`) supports the following fields:

| Field | Meaning |
| --- | --- |
| `name` | Required. Lowercase letters, digits, and hyphens, starting with a letter (up to 63 characters). Unique within a context, and part of the Goal's service URL. |
| `version` | Required version for this goal specification (semver, like `0.1.0`). |
| `skills` | A list of skill paths or registry refs. Relative paths resolve from the spec's directory. A trailing `*` makes a skill a rubric. |
| `interval` | How long Telos waits after each successful pass before reconciling the Goal again, like `30m` or `6h` (units `s`, `m`, `h`). Without it, there are no scheduled passes. |
| `network` | [Outbound network configuration](environment.md#egress) for the Goal. |

### Body

Within the body of the spec, describe the service you want. Focus on the high-level intent of the system, observable surfaces, API endpoints, functional and non-functional requirements, and other visible constraints.

Implementation details, such as choice of programming language, code structure, etc., should be intentionally omitted. Our recommendation is to focus on describing externally impactful outcomes. Simply put: use this space to describe *what* you want built, not *how*.

## Skills

A single `SPEC.md` is often not enough to describe your service in full detail. Telos lets you modularize with [agent skills](https://agentskills.io/), specified in YAML frontmatter up top. Agent skills are great for giving the agents additional expertise and for organizing information into reusable components.

A skill folder holds the skill's `SKILL.md` and any files it bundles:

```text
how-to-fish/
├── SKILL.md
└── references/
    └── knots.md
```

A few examples include:
- a company design system specific skill (logos, assets, CSS, and prose)
- a third-party API reference skill (e.g., Salesforce, JIRA, GitHub)
- a submodule of the desired service (e.g., backend API system of a multi-component service)

In each case, the skills serve different purposes — bundling information, providing expertise, and organizing information.

## Rubrics

Rubrics are skills that are marked with a trailing asterisk (`*`). These skills are treated as required acceptance criteria that an independent evaluator agent can enforce and grade against. A Goal is not marked as `ready` until every rubric has passed independent evaluation.

Rubrics are important for a few reasons:

- skills can provide additional context, but are primarily informational as opposed to binding
- agents are non-deterministic processes that may sometimes skip instructions or terminate early
- agents tend to grade their own work favorably and have blind spots

Independent evaluation lets Telos work persistently until an arbitrary set of criteria — including the spec and rubrics — is verified.

Human organizations exhibit similar tendencies too — there's a reason students don't grade their own homework or why developer teams enforce peer code review!

## Packaging

The goal specification (`SPEC.md`), skills, and rubrics can be bundled into packages. Telos supports two kinds of packages:

- skill packages, with just a skill
- spec packages, with a spec and its skills

Packages are named `@<context>/<name>:<version>` (`npm` users will find this familiar).

Spec packages can reference skill packages. For example, a skill `@telos/how-to-fish:1.0.2` could be referenced as follows:

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

Telos Cloud manages a hosted registry of packages of both kinds. Use `telos push <SPEC.md|skill-dir>` to push up your package, optionally with an explicit `--context`. Pushes are private by default, but can be made public with the `--public` flag on a package's first push.

```console
$ telos push SPEC.md
pushed undersea-survival

Ref       @alice/undersea-survival:0.1.0
Digest    sha256:3158b3a4fbe9577b43bab574bb725f8ecebf4ec86c89bc896a0696a2ca17800d
Version   0.1.0
```

`telos apply` also pushes your spec as a private package, like `@alice/reading-list:0.1.0`, so bump `version` whenever the spec or its skills change. Otherwise, `apply` fails with `package version already exists (HTTP 409)`.

To fetch a package locally, for inspection or modification, you can run `telos pull @<context>/<name>:<version>`.

```console
$ telos pull @telos/how-to-fish:1.0.2
pulled @telos/how-to-fish:1.0.2

Package   @telos/how-to-fish:1.0.2
Digest    sha256:b474a99a2705e23cf905a484ec6d14ef58b56bbe62e9292783466ec363b5072d
Path      how-to-fish
```

If you've programmed in C before, this shape will feel familiar. A `SPEC.md` and the skills it loads are like `main.c` and the libraries it links. Skill packages are those libraries, and spec packages are the binaries. You can apply a spec package directly by its ref, like running a binary: `telos apply @alice/undersea-survival:0.1.0`.

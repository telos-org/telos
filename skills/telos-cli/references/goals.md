---
title: Goals
description: An overview of the framework behind the Telos cloud platform.
group: Platform
---

Telos is built on top of goals as the durable unit of work. But a goal is not *just* a markdown file. There are a few components to the programming system, designed to make it flexible and maximally useful to the end user.


# The Goal Specification

The goal specification (`SPEC.md`) is the entrypoint to a Telos program. We'll walk through an example below:

```markdown
---
name: a-descriptive-name
version: x.y.z
skills:
 - foo-skill
 - bar-skill
 - baz-rubric*
interval: xx
---

<your-goal-here>

```

## Frontmatter

The frontmatter supports the following fields:

| Field | Meaning |
| --- | --- |
| `name` | Required lowercase, DNS-compatible identity. Keep it stable across revisions. |
| `version` | Required semantic version for this immutable revision. Bump it when the contract changes. |
| `skills` | A path or YAML list of paths and exact registry refs. Relative paths resolve from the spec directory. A trailing `*` makes a skill an acceptance rubric. |
| `interval` | A positive duration ending in `s`, `m`, or `h`, such as `30m` or `6h`, carried as the contract's reconciliation interval. |
| `tags` | A YAML list of string labels. The default is an empty list. |

## The Entrypoint

Telos translates the goal specification to a live software service and continuously reconciles the service against the desired specification.

Within this file, describe the service you want. Focus on the high level intent of the system, observable surfaces, API endpoints, functional and non-functional requirements, and other visible constraints.

Implementation details, such as choice of programming language, code structure, etc should be intentionally ommitted. The focus is entirely on system design.

Writing high quality specs is a non-trivial problem! We've written more about it [here]

## Skills 

A single `SPEC.md` is often not enough to describe your goal in full detail. Telos lets you modularize with [agent skills](https://agentskills.io/), specified in YAML frontmatter up top. Agent skills are great to provide the system with additional capabilities or expertise or alternatively simply organize information into modular compontents that could be reused.

A few examples include:
- a company design system specific skill (logos, assets, CSS, and prose)
- a third party API reference skill (eg: Salesforce, JIRA, GitHub)
- a submodule of the desired service (eg: backend API system of a multi-component service)

In each case, the skills serve different purposes - bundling information, providing expertise, and organizing information. 


## Rubrics

Rubrics are skills that are marked with trailing asterisks `*`. These skills are treated as required acceptance criteria that an independent evaluator agent can enforce and grade against. Rubrics are important for a few reasons:

- skills can provide additional context, but are ultimately primarily informational as opposed to binding
- agents are non-deterministic processes that may sometimes skip instructions or terminate early
- due to the autoregressive nature of LLMs, agents tend to grade their own work favourably and tend to have blind spots.

Independent evaluation lets the system work persistently until an arbitrary set of criterion - including the spec and rubrics - are verified.

Humans organisations exhibit similar tendencies too - there's a reason students don't grade your own homework or why developer teams enforce peer code review!


## Packaging

Having defined the goal `SPEC.md` (the entrypoint), skills, and rubrics, a natural next question is the packaging and distribution of these artifacts. Telos supports two forms of packaging:

- Packages with just a skill (A)
- Packages comprising a spec, with associated skills (B)

The naming convention adopted for both is of the form `@<context>/<name>:<version>`

Packages of type B can reference packages of type A. For example, suppose we have a skill `@telos/how-to-fish:1.0.2`, a goal's frontmatter could reference it as follows:

```
---
name: undersea-survival
version: a.b.c
skills:
 - @telos/how-to-fish:1.0.2
 - another-skill
interval: xx
---

...

```

Telos cloud manages a hosted registry of packages of both types. The method for interaction with this hosted registry is simple:

Use `telos push SPEC.md|SKILL_DIR` to push up your package, optionally with an explicit `--context` (that is otherwise derived from your default in `telos config`)

To fetch a package locally, for inspection or modification, you can run `telos pull @scope/name:version` (TODO(grohan): why the fuck is it called scope and not context?).

(TODO(grohan): we should not have both `telos pull` and `telos pull skill`!)

## A mental model

On framework design: you might notice some similarities to the C programming language. You could conceptualize a `SPEC.md` as equivalent to a `main.c` and skill files like `#include`s. Framed this way, the purposes and shapes of the primitives in the Telos ecosystem should feel a lot more familiar, and you can even go as far as to treat type A packages as "libraries"  and type B packages as "binaries"!

---
name: telos-spec-writing
description: Collaboratively write, refine, or review high-quality Telos SPEC.md files. Use when a user wants to turn an idea into a runnable Telos spec, improve a declarative goal or service contract, choose spec frontmatter or skills, prepare a telos run/apply workflow, or make success independently verifiable.
---

# Telos Spec Writing

Help the user express the smallest declarative contract that gives a capable
agent enough direction to deliver and independently verify the intended
outcome.

## Pair with the User

Treat spec writing as a short design conversation, not a questionnaire.

- Let the user own product intent, constraints, and tradeoffs.
- Translate informal intent into a precise contract and point out decisions
  that would otherwise be left to the implementation agent.
- Ask only questions whose answers materially change the contract. Offer a
  recommended default when asking.
- Make safe, reversible assumptions when the intent is clear, and state the
  important ones alongside the draft.
- Show useful draft language early. Refine concrete text instead of discussing
  abstractions indefinitely.
- Keep implementation choices open unless reproducibility, interoperability,
  safety, or the user requires a specific choice.

Do not make the user specify internal architecture merely because the
implementation agent will eventually need to choose one.

## Workflow

### 1. Inspect the Context

Read the repository's `AGENTS.md` when present, inspect nearby specs, and locate
the system that owns the requested behavior. When editing frontmatter, read
[references/spec-schema.md](references/spec-schema.md). For durable services,
compatibility targets, recurring controllers, or other non-trivial contracts,
also read [references/spec-patterns.md](references/spec-patterns.md).

Preserve the ownership boundary. A service spec should not absorb UI,
infrastructure, billing, or another service's responsibilities unless those
behaviors are part of its public contract.

### 2. Frame the Outcome

Establish these facts in prose or conversation:

- what should be observably true when the work succeeds;
- whether this is a bounded run or a durable reconciled service;
- who or what consumes the result;
- what existing behavior or state must survive;
- what must never happen.

Do not force all five into headings. They are a reasoning checklist.

### 3. Resolve Contract Decisions

Clarify only the axes that matter for this goal:

- stable public surfaces and identities;
- mutable state and who may change it;
- persistence, restart, and reconciliation behavior;
- security, tenancy, and secret boundaries;
- failure behavior and actionable degraded states;
- evidence that proves the important claims.

Separate unresolved product decisions from replaceable implementation choices.
Ask the user about the former; leave the latter to the implementing agent.

### 4. Draft the Smallest Complete Spec

Use one H1, normally `# Goal`. A bounded outcome should usually fit in one or a
few paragraphs. Add subsections only when they separate genuinely independent
contract axes.

Write what must be true, not a sequence of commands. Prefer observable
behavior and boundaries over adjectives such as "robust," "production-ready,"
or "user-friendly."

### 5. Review from Both Agent Roles

Read the draft twice:

1. **Implementation:** Can a fresh agent act without inventing a product
   decision or violating an ownership boundary?
2. **Evaluation:** Can an independent agent prove or disprove every important
   claim using the delivered artifact or live behavior?

Tighten ambiguous obligations. Remove details that constrain the solution
without improving the contract.

### 6. Validate

When a compatible CLI and context are available, run:

```bash
telos plan path/to/SPEC.md
```

For an existing session, prefer:

```bash
telos plan path/to/SPEC.md --session sess_...
```

When editing repository files, also run `git diff --check`. Report validation
that could not be run instead of implying success.

## Contract Rules

- Name stable outcomes and interfaces; avoid prescribing the build sequence.
- Keep stable aliases opaque when their provider, model, host, or backend is
  meant to remain replaceable.
- Name authority when multiple actors can mutate the same state.
- State negative requirements when absence matters, such as no secret leakage,
  no silent fallback, or no cross-tenant access.
- Require evidence at the same boundary as the claim: live requests for
  behavior, restart probes for durability, denied actions for authorization,
  and durable readback for persistence.
- Avoid making tests, dashboards, or documentation substitute for the behavior
  they are supposed to demonstrate.
- Do not copy every implementation concern into the spec. A capable agent still
  owns ordinary engineering decisions.

## Skills and Evaluation Rubrics

Declare only skills available to the target package or catalogue. Treat skills
as implementation guidance by default. Add `*` only when the skill must become
a blocking evaluation rubric.

Preserve pinned registry references when editing an existing packaged spec
unless the user asks to change them. Do not invent skill names.

## Editing Existing Specs

- Preserve the stable `name`, platform, and ownership boundary.
- Treat published behavior as compatibility surface unless the user authorizes
  a break.
- Remove obsolete language instead of layering contradictory requirements.
- Keep deliberate implementation pins and explain any proposed relaxation.
- Bump the version once when preparing a publishable revision, not during every
  draft iteration.
- Do not publish, apply, deploy, or commit unless the user requests it.

## Handoff

When drafting conversationally, provide the complete proposed spec plus a
short list of material assumptions or open decisions. When editing a file,
summarize the resulting contract without repeating the entire file.

Always state the path, validation performed, and whether anything was
committed, published, applied, or deployed.

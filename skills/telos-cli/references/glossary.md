---
title: Glossary
description: Read the canonical vocabulary for Goals, revisions, execution, and evidence.
group: Reference
---

# Glossary

Telos uses a small set of terms to distinguish what you ask for, what executes,
and what proves the result. They appear here in the order a reader encounters
them in the product.

## Goal

The outcome Telos works to make true. A persistent Cloud Goal keeps one
identity while its contract and implementation evolve through revisions.

## `SPEC.md`

The authored contract for a Goal. Its frontmatter selects execution inputs;
its Markdown body describes the outcome, meaningful constraints, and evidence
of success.

## Revision

One immutable version of a compiled Goal contract. A revision is identified by
its digest and does not change after publication.

## Session

The CLI handle and history for execution. A persistent Goal keeps one session
across revisions; every bounded run receives a new session.

## Deployment

The managed Cloud object for a persistent Goal. It owns the current revision,
environment, public routes, and reported state.

## Reconciliation

The asynchronous process that moves a deployment toward its current revision
and verifies the result. An `apply` receipt confirms acceptance for work, not
completed reconciliation.

## Evidence

The observations that demonstrate a revision satisfies its contract: for
example, test results, workload state, or exercised live behavior. Activity
logs can contain evidence, but activity alone is not proof of completion.

## Context

The personal or team Cloud workspace that owns a deployment. A context is an
explicit CLI target; it is distinct from the filesystem workspace inside an
execution environment.

## Workspace

The filesystem where an agent works. Cloud retains its Goal workspace across
attempts and revisions; a local run receives an isolated workspace derived
from its selected source checkout.

## Bounded run

A local execution that stops at a cycle, time, or cost bound. It produces a
session and evidence without creating a persistent Cloud deployment.

## Package

An immutable compiled spec with its locked skill dependencies. Registry
packages can be addressed by a human-readable ref or an exact digest.

## Digest

The content identity of immutable bytes, written as `sha256:...`. Revision and
package digests let receipts, status, and evidence refer to the same input.

## Skill

A reusable instruction and capability bundle imported by a spec. Registry
skills are versioned and immutable.

## Rubric

A skill that also participates in acceptance. A trailing `*` on a skill ref
asks an independent verifier to use that skill when judging the revision.

For the relationships between Goal, revision, session, and deployment, continue
with [The Goal lifecycle](lifecycle.md).

---
name: backend-design
description: |
  Backend API, lifecycle, and state-machine design standards for Telos specs.
  Use as a verifier rubric when the artifact is an API contract, daemon,
  controller, reconciler, scheduler, or runtime boundary.
metadata:
  category: design
  role: verifier
  author: telos
allowed-tools: Bash(*) Read(*) Write(*) Edit(*)
---

# Backend Design Standards

Use this skill to judge whether a backend or runtime design is simple enough to
implement, operate, and extend under the next natural spec change. Prefer
direct semantics over new machinery.

## Required Judgement

Before conceding, state whether the artifact has:

1. **Clear user-facing semantics.** Public APIs describe product actions, not
   internal mechanisms.
2. **Private internal machinery.** Substrate details stay internal unless a
   user must reason about them.
3. **Explicit state transitions.** Lifecycle names distinguish different
   causes, such as stopped by operator vs. superseded by new intent.
4. **Durable authority.** The source of truth is named, persisted, and
   recoverable after process restart.
5. **Bounded side effects.** A trigger mutates the smallest required failure
   domain.
6. **Idempotent behavior.** Repeated requests converge instead of accumulating
   duplicate work or stale state.
7. **Observable failures.** Errors and partial progress are inspectable without
   reading hidden process memory.

## API Design Checks

- Do not add public endpoints for internal control flow when an existing
  semantic endpoint can express the user's intent.
- Keep request and response shapes small, typed, and aligned with the product
  model.
- Avoid flags that expose implementation choices rather than user intent.
- Make retries safe. If a request can be repeated, define what stays stable and
  what may change.
- Return enough identity and status for callers to inspect progress without
  reaching into private storage.

## Runtime And Lifecycle Checks

- Name each lifecycle transition by cause, not just outcome.
- Do not represent spec updates, crashes, operator stops, and successful
  convergence with the same status.
- A process restart should not be the only way to apply desired state unless
  the design explicitly uses a declarative rollout primitive.
- Avoid broad restarts. Restart only the process that must re-read state.
- If work can be interrupted, say what is persisted before interruption and how
  the next run resumes or discards it.

## State And Filesystem Checks

- The artifact must name the authoritative state and any advisory state.
- Advisory files, diffs, logs, and transcripts must never become the contract.
- Avoid large filesystem taxonomies when one file next to the existing state is
  enough.
- If the design writes generated files, define whether they are overwritten,
  versioned, or append-only.
- State needed for rollback, replay, or audit should be discoverable from the
  session layout.

## Complexity Rejection Rules

Reject designs that introduce:

- a queue, event bus, scheduler, database, or daemon without a concrete failure
  mode that the simpler design cannot handle;
- a public API solely to poke an internal worker;
- hidden in-memory state required for correctness;
- manual operator commands as the normal control path;
- duplicated sources of truth;
- vague words such as "sync", "handle", or "process" where a state transition
  or API behavior should be named.

## Evidence Standard

For each backend-design finding, cite the exact API route, state file, lifecycle
field, deployment boundary, or failure mode that makes the design risky. If the
artifact is only a design document, source text is enough evidence.

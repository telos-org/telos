# Adaptive examples

Use these examples to compare information shapes. Do not copy their headings
unless the product needs them.

## A small bounded deliverable

A one-time local report does not need a product architecture:

```markdown
---
name: dependency-risk-report
version: 0.1.0
platform: local
---

# Dependency risk report

Produce a review of the repository's production dependencies that helps a
maintainer decide what to upgrade next.

## Deliverable

- Write `DEPENDENCY_RISKS.md` with the affected package, installed version,
  current stable version, risk, and recommended action for each finding.
- Rank findings by likely user impact, then by exploitability.
- Link each version or vulnerability claim to its authoritative advisory.

## Acceptance

- Every direct production dependency appears in the reviewed inventory.
- Each recommended upgrade is compatible with the repository's declared
  runtime or is marked as requiring a runtime change.
- Re-running the documented inventory command produces the dependency set used
  by the report.
```

The structure is short because the outcome has one artifact, few boundaries,
and no persistent service behavior.

## A multi-surface product

A persistent product with authoritative contracts needs more hierarchy:

```markdown
---
name: support-workspace
version: 0.1.0
platform: cloud
---

# Support workspace

Run a browser and API workspace where support agents can triage customer cases
against the same permission-aware state.

The product must:

- Let agents assign, reply to, and resolve cases.
- After a successful mutation returns, show the updated state through both the
  browser and API.
- Prevent users from observing cases outside their assigned queues.

## Authoritative inputs

- **API:** `contracts/http.yaml` defines supported operations and wire shapes.
- **Permissions:** `contracts/access.yaml` exhaustively defines roles, queues,
  and case visibility.
- Prose cannot add routes or permissions absent from those inventories.

## Product behavior

### Agent workspace

- **Queue:** Show each agent the open cases in their permitted queues.
- **Case work:** Persist assignment, reply, status, and resolution changes from
  working controls rather than decorative UI.

### API and shared state

- **Contract:** Implement every operation declared by `contracts/http.yaml`.
- **Consistency:** Make each successful mutation visible through both the
  browser and every applicable API read.

### Identity and access

- Authenticate every browser and API request as a declared user.
- Enforce queue visibility on list, search, direct lookup, and mutation paths.

## Compatibility boundaries

- Compatibility is limited to the methods, paths, and shapes in
  `contracts/http.yaml`.
- Undeclared aliases and successful no-op mutations are out of scope.

## Release conditions

### Shared behavior

- Create and update a case through each writable surface and observe the same
  state through the other surface.

### Authorization

- Verify permitted access and expected denial for every declared role and
  queue boundary.

### Public deployment

- Exercise sign-in, queue loading, case mutation, API authentication, and
  denial behavior through the published HTTPS origin.
```

The sections come from this product's correctness domains. Another complex
product might need data lifecycle, offline behavior, visual fidelity, hardware
constraints, failure recovery, or a different evidence breakdown instead.

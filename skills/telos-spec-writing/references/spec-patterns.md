# Telos Spec Patterns

Use these patterns as starting points, not mandatory templates. A spec should
contain only the sections its contract needs.

## Bounded Outcome

Use a compact outcome statement for a build, change, investigation, or
artifact with a natural finish.

```markdown
# Goal

The session detail page renders the active artifact inline with a clear status
header and collapsible history. Existing API behavior remains compatible, and
the empty state remains useful when no artifact exists.
```

Add constraints only when their omission would materially change the result.
Do not enumerate the implementation steps a capable coding agent can derive.

## Durable Service

Describe durable behavior across the axes that matter:

1. stable surfaces and identities;
2. durable state and its authority;
3. operator-controlled versus reconciler-controlled fields;
4. security and tenancy boundaries;
5. failure, restart, and recovery behavior;
6. live evidence that defines health.

Example shape:

```markdown
# Goal

Provide a durable webhook delivery service that accepts tenant-scoped events,
delivers them at least once to registered HTTPS endpoints, and exposes each
event's delivery state through the authenticated API.

## State and authority

Callers own endpoint registration and event submission. The service owns
attempt history and delivery scheduling. Restarting or replacing an instance
does not lose accepted events or reset attempt identity.

## Failure behavior

Transient delivery failures retry with bounded backoff. Invalid endpoints and
exhausted delivery attempts remain visible with an actionable terminal reason;
they are never reported as successful.

## Verification

Demonstrate an accepted delivery, a retry followed by success, durable state
across restart, and rejection of cross-tenant reads.
```

## Recurring Controller

For a spec with `interval`, distinguish desired state from authority:

- what the controller may create or repair;
- what it must observe and preserve;
- which drift it corrects automatically;
- which divergence requires an operator;
- what evidence permits it to declare convergence.

This prevents reconciliation from overwriting deliberate operator changes.

## Stable Alias

A stable alias should hide replaceable implementation:

```markdown
`product/default` is a stable service class. Its concrete backend, provider,
and fallback policy are operator-controlled and may change without changing
callers. Requests record both the stable class and resolved implementation.
```

Do not put today's mapping in the stable contract unless callers are intended
to depend on it.

## Compatibility Target

When compatibility is the product, exact observable behavior belongs in the
spec:

- supported operations and response or error shapes;
- authentication and authorization behavior;
- idempotency, consistency, and ordering;
- version and upgrade behavior;
- representative positive and negative probes.

Avoid copying internal details from the reference system unless clients can
observe or depend on them.

## Investigation or Decision

Name the decision the work must enable, the evidence required, and the form of
the deliverable. Avoid requiring a predetermined conclusion.

```markdown
# Goal

Determine why hosted session startup exceeds five minutes for restored guests
and recommend the smallest change that materially reduces the delay. The
deliverable identifies the dominant stages from timestamped evidence, rules
out plausible alternatives, and includes a reproducible measurement method.
```

## Negative Requirements

Add explicit negatives only for meaningful boundaries:

- credentials never appear in logs or artifacts;
- a missing configuration file does not silently select a different tenant;
- operator-owned fields are not overwritten during reconciliation;
- failed or stale accounting is not rendered as zero;
- internal allocation details do not become public API.

Negative requirements are strongest when paired with a denial or failure probe.

## Avoiding False Precision

Replace subjective language with observable outcomes:

- Instead of “robust retries,” state which failures retry, their bounds, and
  the terminal behavior.
- Instead of “clean UI,” name the information hierarchy or task the user can
  complete.
- Instead of “production-ready,” name the required durability, security,
  operability, and recovery properties.
- Instead of “well tested,” name the load-bearing behaviors that independent
  evaluation must exercise.

Do not turn every adjective into a metric. Use precision where it changes what
the agent must deliver or what the evaluator must prove.

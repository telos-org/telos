# Nested child goals

A running Telos agent can delegate an independent, bounded result to another
Telos run. The child becomes its own session while remaining linked to the
parent.

For example, a parent implementing a service might delegate a compatibility
report:

```markdown
---
name: compatibility-report
version: 0.1.0
platform: local
---

# Goal

Compare the current API responses with the v1 fixtures and return a report of
every incompatible change, with the fixture and response that prove it.
```

Inside the parent session:

```bash
telos run path/to/CHILD_SPEC.md --until 2 --max-cost-usd 5
```

`--until` accepts a review-cycle count or a duration such as `30m`.
`--max-cost-usd` adds a spend bound. Telos supplies the environment-local
capability needed to create and inspect the child, so the spec contains no user
or Cloud credentials.

A child can produce files, tests, analysis, or another observable deliverable.
Persistent `telos apply` is a top-level lifecycle and is unavailable inside a
session; nested work uses `run`.

The submission receipt contains the child session ID. Follow it like any other
run:

```bash
telos describe CHILD_SESSION_ID
telos logs CHILD_SESSION_ID
```

The parent can then inspect, integrate, and verify the child's result.

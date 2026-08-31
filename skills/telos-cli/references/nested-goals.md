---
title: Nested Goals
description: Delegate bounded child work from a running Telos session and integrate its evidence.
group: Concepts
---

# Nested Goals

A nested Goal is the [bounded-run](bounded-runs.md) workflow launched from
inside another Telos session. The child receives its own session and remains
linked to its parent.

```bash
telos run path/to/CHILD_SPEC.md --until 2 --max-cost-usd 5
```

The child spec uses `platform: local`. `--until` accepts a review-cycle count or
a duration such as `30m`; `--max-cost-usd` adds a spend bound. Telos supplies
the environment-local capability to create and inspect the child, so the spec
contains no user or Cloud credentials.

A child can produce files, tests, analysis, or another observable deliverable.
Persistent `telos apply` is a top-level lifecycle; nested work uses `run`.

The submission receipt contains the child session ID:

```bash
telos describe CHILD_SESSION_ID
telos logs CHILD_SESSION_ID
```

The parent inspects the result, integrates anything it needs, and verifies the
child evidence against the parent Goal.

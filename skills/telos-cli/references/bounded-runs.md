---
title: Bounded runs
description: Run a local Goal to a cycle, time, or cost bound and inspect its evidence.
group: Concepts
---

# Bounded runs

Use `telos run` for work with a natural stopping point: an analysis, migration,
focused implementation, or other result that does not need a persistent Cloud
deployment.

Create `REPORT_SPEC.md`:

```markdown
---
name: compatibility-report
version: 0.1.0
platform: local
---

# Goal

Compare the current API responses with the v1 fixtures. Write
`COMPATIBILITY.md` with every incompatible change and the fixture and response
that prove it.

# Acceptance

- `COMPATIBILITY.md` covers every v1 fixture.
- Each incompatibility cites both the fixture and observed response.
```

Preview the contract, then run it in the current repository for at most three
review cycles. Resolve the spec, source checkout, and bounds, and obtain the
user's approval before starting the run:

```bash
telos plan REPORT_SPEC.md
telos run REPORT_SPEC.md --workspace . --until 3
```

`--until` also accepts a duration such as `30m`. A top-level local run has a
default `$20` cost ceiling. An explicit `--max-cost-usd` takes precedence over
`TELOS_MAX_COST_USD`, which takes precedence over that default. Choose bounds
that give the task room to finish while keeping its stopping condition
explicit.

## Prepare the source checkout

`--workspace .` selects the current repository as source. Telos clones a clean
Git repository—or snapshots a non-Git directory—into an isolated session
workspace. The run does not edit the source checkout directly.

For Git sources, the worktree must have no tracked or untracked changes apart
from Telos's own `.telos` marker. Git submodules and Git LFS are not included in
the isolated workspace; Telos rejects repositories that use either rather than
starting from incomplete source.

By default, the first local run creates `.telos` in the source checkout as a
symlink to the external session store. The marker lets later `list`, `describe`,
`logs`, and `delete` commands find sessions for that checkout. It is not the
active workspace and is excluded from cleanliness checks and snapshots. Setting
`TELOS_SESSION_DIR` selects a session store explicitly and suppresses the
marker.

The receipt contains a new run session. Inspect its state and evidence:

```bash
telos describe SESSION_ID
telos logs SESSION_ID
```

After completion, `telos describe SESSION_ID --json` exposes the accepted
workspace checkpoint as `specs[0].workspace_path`. Extract that `tar.gz` into a
chosen result directory:

```bash
mkdir -p telos-result
tar -xzf WORKSPACE_PATH -C telos-result
```

`telos-result/COMPATIBILITY.md` is the material deliverable. `describe` and
`logs` provide its session state and evidence.

A bounded session can complete, fail, stop at its bound, or become stale. It
does not create or update a persistent Cloud Goal. [Models and inference](inference.md)
explains local `pi` selection. When a running Telos agent creates this session
as a child, the additional lineage rules are in [Nested Goals](nested-goals.md).

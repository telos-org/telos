---
title: Bounded runs
description: Run a local Goal to a cycle, time, or cost bound and inspect its evidence.
group: Concepts
---

# Bounded runs

Use `telos run` for work with a natural stopping point: an analysis, migration,
focused implementation, or other result that does not need a persistent Cloud
deployment.

Before starting, [install Telos and authenticate local pi](install.md).
Local runs use your pi provider credentials; a Telos Cloud login does not
configure them.

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

## Prepare the source checkout

`--workspace .` selects the current repository as source. Telos clones a clean
Git repository—or snapshots a non-Git directory—into an isolated session
workspace. The run does not edit the source checkout directly.

For Git sources, the worktree must have no tracked or untracked changes apart
from Telos's own `.telos` marker. Git submodules and Git LFS are not included in
the isolated workspace; Telos rejects repositories that use either rather than
starting from incomplete source.

This includes the `REPORT_SPEC.md` you just created. If you keep the spec in
the source repository, commit it with the intended source changes before
running. Alternatively, save the spec outside the checkout and pass that path
to both `plan` and `run`; the source checkout must still be clean. Relative
skill imports resolve from the spec's directory, not from `--workspace`.

Check the intended Git source with:

```bash
git status --short
```

By default, the first local run creates `.telos` in the source checkout as a
symlink to the external session store. The marker lets later `list`, `describe`,
`logs`, and `delete` commands find sessions for that checkout. It is not the
active workspace and is excluded from cleanliness checks and snapshots. Setting
`TELOS_SESSION_DIR` selects a session store explicitly and suppresses the
marker.

## Preview and run

From the prepared source checkout, validate the contract, then run it for at
most three review cycles with a `$20` cost ceiling:

```bash
telos plan REPORT_SPEC.md
telos run REPORT_SPEC.md --workspace . --until 3 --max-cost-usd 20
```

`plan` validates the spec; it does not check that the source is clean or that
pi can authenticate. Complete the setup above before starting the run.

`--until` also accepts a duration such as `30m`. A top-level local run has a
default `$20` cost ceiling. An explicit `--max-cost-usd` takes precedence over
`TELOS_MAX_COST_USD`, which takes precedence over that default. Choose bounds
that give the task room to finish while keeping its stopping condition
explicit. Reaching a bound stops further work; it does not prove that your
acceptance criteria passed.

## Inspect and retrieve the result

The receipt contains a new run session. Inspect its state and evidence:

```bash
telos describe SESSION_ID
telos logs SESSION_ID
```

Inspect the status and verification evidence before treating the task as
complete. `telos describe SESSION_ID --json` exposes a saved workspace
checkpoint as `specs[0].workspace_path` when one is available. The path alone
does not prove acceptance. Replace `WORKSPACE_PATH` below with that value and
extract the `tar.gz` into a separate result directory:

```bash
mkdir -p telos-result
tar -xzf WORKSPACE_PATH -C telos-result
```

Check `telos-result/COMPATIBILITY.md` against your acceptance criteria. The
run has not copied its changes back into the source checkout; review and
integrate the result yourself. `describe` and `logs` provide its session state
and evidence.

A bounded session can complete, fail, stop at its bound, or become stale. It
does not create or update a persistent Cloud Goal. [Models and inference](inference.md)
explains local `pi` selection. When a running Telos agent creates this session
as a child, the additional lineage rules are in [Nested Goals](nested-goals.md).

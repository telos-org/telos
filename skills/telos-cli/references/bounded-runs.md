---
title: Bounded runs
description: Run a local Goal to a cycle, time, or cost bound and inspect its evidence.
group: Concepts
---

# Bounded runs

`telos apply` is the primary interface. Use `telos run` primarily for harness
development, benchmarking, and [nested execution](nested-goals.md). A local
run executes bounded work in an isolated workspace and stops.

Local runs use the [pi](https://github.com/earendil-works/pi) coding agent.
Before starting, [install Telos with local execution](install.md#add-local-execution),
which includes the `telosd` session runtime. Local runs use your pi provider
credentials; a Telos Cloud login does not configure them.

If pi is not already installed, install it and open it to authenticate with
`/login`:

```bash
npm install -g @earendil-works/pi-coding-agent
pi
```

Exit pi after authentication, then create `REPORT_SPEC.md`:

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

For Git sources, the worktree must be clean, including the `REPORT_SPEC.md`
you just created. Commit the spec with the intended source changes, or save
it outside the checkout and pass that path to both `plan` and `run`. Relative
skill imports resolve from the spec's directory, not from `--workspace`.

Check the intended Git source with:

```bash
git status --short
```

## Preview and run

From the prepared source checkout, validate the contract, then run it for at
most three review cycles with a `$20` cost threshold:

```bash
telos plan REPORT_SPEC.md
telos run REPORT_SPEC.md --workspace . --until 3 --max-cost-usd 20
```

`plan` validates the spec; it does not check that the source is clean or that
pi can authenticate. Complete the setup above before starting the run.

`--until` also accepts a duration such as `30m`. A top-level local run has a
default `$20` cost threshold. Cost is checked between agent turns, so a turn
can take the total above the threshold. An explicit `--max-cost-usd` takes
precedence over `TELOS_MAX_COST_USD`, which takes precedence over that default.
Choose bounds that give the task room to finish while keeping the stopping condition
explicit. Reaching a bound stops further work; it does not prove that your
acceptance criteria passed.

## Inspect and retrieve the result

The receipt contains a new run session. Inspect its state and evidence:

```bash
telos describe SESSION_ID
telos logs SESSION_ID
```

Inspect the status and verification evidence before treating the task as
complete. Once the run has finished, use `telos describe SESSION_ID --json`
and confirm `specs[0].workspace_exists` is `true`. Replace `WORKSPACE_PATH`
below with `specs[0].workspace_path` and extract the checkpoint into a separate
result directory. The path is assigned before the archive is created, so its
presence alone does not mean a checkpoint is available.

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

## Source and session storage details

Telos rejects Git sources that use submodules or Git LFS because the isolated
workspace cannot include them. The cleanliness check excludes Telos's own
`.telos` marker.

By default, the first local run creates `.telos` in the source checkout as a
symlink to the external session store. It lets later `list`, `describe`,
`logs`, and `delete` commands find sessions for that checkout. The marker is
excluded from source snapshots. Set `TELOS_SESSION_DIR` to select a session
store explicitly and suppress the marker.

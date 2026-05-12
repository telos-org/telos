---
name: task-promotion
description: |
  Extract a replayable task package from a hard live incident or evolution in a
  world, then prove it with fresh replay and black-box evaluation.
metadata:
  category: control-plane
  author: telos
allowed-tools: Bash(telos:*) Bash(kubectl:*) Bash(python3:*) Bash(git:*)
---

# Task Promotion

Use this skill when a world has produced a hard reconcile, recovery, or
evolution problem that may be worth promoting into a replayable task package.

This is not a generic mutation framework. It is the controller procedure for
freezing one strong task out of lived world state.

## First Principles

- Promote only real solver difficulty, not bookkeeping noise or authority bugs.
- The task package is the source of truth. Do not rely on unstored cluster
  residue once a task is promoted.
- The solver sees only `task/public/`.
- `task/setup/`, `task/grader/`, and `task/solution/` are evaluator-owned.
- Prefer one clean, replayable task over a large bundle of shallow artifacts.

## Package Layout

Write the package to `task/` at the workspace root:

```text
task/
├── public/
│   └── spec.md
├── setup/
│   ├── namespaces/
│   │   ├── <namespace-a>/
│   │   └── <namespace-b>/
│   ├── bootstrap.sh      # optional
│   └── inject.sh         # optional
├── grader/
│   └── tests/
│       ├── BUILD.bazel
│       └── test_*.sh or test_*.py
└── solution/
    └── solve.sh
```

Interpretation:

- `public/spec.md`
  the only package file the black-box solver gets
- `setup/namespaces/<name>/`
  the healthy baseline manifests for each namespace in the environment graph
- `setup/bootstrap.sh`
  optional healthy-state construction after manifests apply; use for logical
  data seeding, app initialization, or startup smoke checks
- `setup/inject.sh`
  optional transition from healthy baseline to incident state; omit for pure
  evolution/change-request tasks
- `grader/tests/`
  evaluator-owned tests and partial-credit stages
- `solution/solve.sh`
  the reference repair or evolution path

Do not introduce extra metadata files unless the filesystem contract is
insufficient.

## When To Promote

Promotion is for incidents or evolutions with evidence of real frontier value.

Usually promote when one of these is true:

- the solver failed a reconcile or recovery attempt on a real weakness
- the solver succeeded only after multiple PVG rounds
- the solver succeeded, but only at unusually high cost
- a low-information short-budget solve is weak while a longer-budget solve is
  materially better

Do not promote when:

- the failure came from missing authority or a bad child spec
- the world state is not reconstructible from artifacts you can actually write
- the task is just one obvious revert or a noisy teardown

## Extraction Workflow

1. Choose the source incident.

Read:

- recent `session.json`
- recent `evidence.jsonl`
- the latest checkpoint or saved workspace
- the current live cluster residue

Pick a single weakness family. Name it plainly in your notes.

2. Decide whether it is an incident or an evolution task.

- incident task:
  healthy baseline plus injected broken state
- evolution task:
  healthy old state plus a target change request; usually no `inject.sh`

3. Write `task/public/spec.md`.

Rules:

- use symptom language or change-request language only
- include real service names, ports, endpoints, versions, and constraints
- never reveal the internal root cause, file paths, or repair steps
- require `diagnosis.md` when the task is an incident or hard migration

4. Capture the healthy baseline into `task/setup/namespaces/`.

For each namespace required by the task:

- create `task/setup/namespaces/<namespace>/`
- write the manifests needed to recreate that namespace from fresh scratch
- pin image versions; never use `:latest`
- parameterize namespace references via `TASK_NAMESPACE` only when one namespace
  is sufficient; for multi-namespace replay, keep the namespace directory names
  authoritative

Do not assume upstream leftovers remain present.

5. Add `task/setup/bootstrap.sh` only if manifests alone are not enough.

Use it for:

- logical database import or seed data restore
- app-level initialization
- integration bootstrap
- baseline smoke checks that prove the world is healthy before injection

Do not use `bootstrap.sh` to hide the broken state.

6. Add `task/setup/inject.sh` only for incident tasks.

Use it to create the broken state after the healthy baseline is proven.

Rules:

- keep the fault plausible
- prefer silent or partial degradation over loud crashes
- for composed tasks, faults must interact causally
- the solver never sees this file

7. Write `task/grader/tests/`.

The grader must check behavior, not implementation details.

Requirements:

- one BUILD file
- staged or partial-credit tests where appropriate
- no hard-coded dependency on the solver workspace
- no access to hidden package files from the solver side
- behavior-level assertions for the symptoms or migration goals described in
  `public/spec.md`

8. Write `task/solution/solve.sh`.

This should be the minimal real repair or migration path for this exact task.
If the task is order-dependent, preserve that order in the solution and test
for it.

## Fresh Replay

Before promoting, prove the package works from fresh scratch.

Replay procedure:

1. Create isolated scratch namespaces matching `task/setup/namespaces/*`
2. Apply every namespace directory
3. Run `task/setup/bootstrap.sh` if present
4. Verify the healthy baseline
5. Run `task/setup/inject.sh` if present
6. Verify the broken or pre-change state with grader tests
7. Run `task/solution/solve.sh`
8. Re-run grader tests and require full pass

If the package depends on unstored live state, it is not ready.

## Black-Box Evaluation

Run a low-information solver evaluation before promotion.

The outer evaluator performs a **full world replay** first, then launches a
separate black-box solver into that replayed world. This is not limited to one
namespace. If the task package replays 4 namespaces, the fork-test world is all
4 namespaces plus the isolated solver pod.

Solver-visible inputs:

- `task/public/spec.md`
- the live broken or pre-change cluster
- an empty writable workspace

Solver-hidden inputs:

- `task/setup/`
- `task/grader/`
- `task/solution/`

### Replay The Full World

Treat `task/setup/namespaces/*` as the namespace graph for the task.

Fork-test procedure:

1. Create a fresh scratch namespace for each directory under
   `task/setup/namespaces/`
2. Use a deterministic prefix for the replay, for example:
   - logical namespace `postgres`
   - replay namespace `qa-<eval-id>-postgres`
3. Keep a namespace map from logical names to replay names for the duration of
   the fork-test
4. Apply each namespace directory into its replay namespace
5. Run `task/setup/bootstrap.sh` if present
6. Run `task/setup/inject.sh` if present
7. Verify the world is in the intended broken or pre-change state before
   launching the solver

The replay must stand on its own. Do not point the solver at unrelated live
namespaces that happen to exist in the cluster.

### Launch The Black-Box Pi Solver

The solver launch should be explicit and isolated, not implied.

1. Create a dedicated solver namespace for the fork-test, for example
   `qa-<eval-id>-solver`
2. Create a ServiceAccount `solver` in that namespace
3. Create a PVC `solver-workspace` in that namespace
4. Initialize the PVC with an empty git repo only:
   - `git init`
   - set user name and email
   - `git commit --allow-empty -m init`
5. Mount only:
   - the empty solver workspace
   - `task/public/spec.md`
6. Do **not** mount:
   - `task/setup/`
   - `task/grader/`
   - `task/solution/`

### Solver RBAC

Grant the solver authority over the replayed world only.

1. For every replay namespace in the namespace graph, bind the solver
   ServiceAccount to a role that can inspect and repair resources in that
   namespace
2. Do not grant write access outside the replayed namespaces
3. If the task relies on cross-namespace diagnosis, the solver must be able to
   read all replay namespaces, not just the first one

The minimal practical setup is:

- full namespaced write access in each replay namespace
- no cluster-admin
- no access to non-replay namespaces

### Pi Command

Launch a dedicated `pi` pod with:

- the same runtime image family used for normal Telos solving
- the `solver` ServiceAccount
- `ANTHROPIC_API_KEY` from the evaluator's trusted secret source
- an empty writable workspace mount
- a mount containing only `task/public/spec.md`
- `activeDeadlineSeconds` set to a hard timeout

The system prompt should tell Pi:

- this is a live Kubernetes incident or change request
- which replay namespaces it may inspect and mutate
- that it must diagnose from the live cluster only
- that it should write `diagnosis.md` into its empty workspace
- that it should report resolved vs stuck explicitly

The user prompt should be exactly the contents of `task/public/spec.md`.

Example shape:

```bash
pi --mode json \
  --thinking high \
  --no-session \
  --model <model> \
  --system-prompt "<black-box solver prompt>" \
  --prompt "$(cat /task/public/spec.md)"
```

The important property is not the exact flag spelling. The important property
is that the mounted filesystem and prompt surface match the black-box boundary.

### After Pi Finishes

Once the solver pod completes:

1. Capture its logs
2. Preserve its workspace artifacts such as `diagnosis.md`
3. Run `task/grader/tests/` from the outer evaluator context against the replay
   world
4. Record pass/fail, partial credit, runtime, and cost
5. Run `task/solution/solve.sh` separately as reference validation when needed

Compare at least two budgets when possible:

- short-budget / pass@1-style attempt
- longer-budget or search-enabled attempt

Record:

- success or failure
- rounds
- cost
- verifier findings
- whether replay stayed intact

Promote when short-budget performance is weak but longer-budget performance is
materially better and the replay remains trustworthy.

## Promotion Output

When a candidate passes, write:

- the task package under `task/`
- `controller/extraction.md` summarizing:
  - source sessions and checkpoints
  - weakness family
  - replay proof
  - black-box evaluation outcome
  - why this task is worth keeping

If the candidate fails replay or eval quality, write `REJECTED.md` and stop.

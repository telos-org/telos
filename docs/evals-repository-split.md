# Telos And Evals Repository Split

## Purpose

The Harbor evaluation stack currently lives inside this repository even though
most of it is not part of the Telos runtime or product surface. The recommended
split is to keep this repository focused on building and releasing Telos, and
create a separate evals platform repository that treats Telos as one harness
among several systems under test.

The evals repository can start with Harbor and later grow into a web platform,
job runner, artifact store, and comparison system for Telos, Codex, Pi,
OpenCode, and other harnesses.

## Repository Responsibilities

### This repository: `telos`

This repository should own the agent runtime and the public interfaces needed to
run Telos in any environment.

Primary responsibilities:

- Build and release the `telos` and `telosd` binaries.
- Own the Telos CLI contract, especially `telos run`, `telos apply`, logging,
  exit behavior, flags, cost limits, transcript layout, and session lifecycle.
- Own the Sessions API, local/cloud runtime integration, controller behavior,
  spec rendering, skills, transcripts, and evidence model.
- Publish release artifacts through a stable installation mechanism, currently
  the public install script and checksummed binaries.
- Provide a small compatibility contract for external runners:
  - accepted spec format and frontmatter
  - supported command flags
  - expected stdout/stderr behavior
  - transcript and evidence locations
  - exit-code semantics
  - version reporting
- Keep minimal smoke tests or examples proving that released Telos can run a
  benchmark-style task through its public CLI.

This repository should not own durable eval orchestration, multi-harness
fairness policy, Harbor job lifecycle management, provider matrix expansion,
result dashboards, historical score storage, or web UI state.

### New repository: `telos-evals` or `agent-evals`

The new repository should own the evaluation product and all operational state
around benchmark execution.

Primary responsibilities:

- Provide the main eval CLI and web platform.
- Own Harbor integration as the first backend:
  - Harbor installed-agent adapters
  - benchmark/dataset registry
  - task selection and slate definitions
  - Docker task-image prebuilds
  - job launch, status, retry, cancellation, and cleanup
  - Harbor result parsing and artifact indexing
- Own the harness registry for Telos and non-Telos systems:
  - Telos release/local-build harnesses
  - Codex, Pi, OpenCode, no-op, and future harnesses
  - provider/model definitions
  - auth requirements
  - default timeouts and budget settings
  - usage and cost accounting
- Own comparability and fairness checks:
  - same task set per slate
  - same benchmark verifier
  - model capability parity where required
  - timeout/profile provenance
  - Docker host and task-image provenance
  - complete coverage gates before score comparison
- Own durable eval state:
  - slates
  - runs/jobs/attempts
  - artifacts
  - failure classification
  - result snapshots
  - scoreboards
  - audit logs
- Own the web platform:
  - launch/status views
  - scoreboard and drill-down views
  - artifact browsing
  - fairness/coverage warnings
  - rerun workflows
  - operator configuration

The evals repository should treat Telos as a dependency and a harness, not as
the place where eval product logic lives.

## Dependency Contract From Evals To Telos

The evals repository should depend on Telos through stable, black-box surfaces
first. It should avoid importing Go internal packages or relying on repository
private implementation details.

Recommended dependency modes, in priority order:

1. Released binary dependency
   - The default path for reproducible benchmark runs.
   - The evals repo records the Telos version, install URL, checksum, platform,
     and `telos version` output for every run.
   - Harbor containers install Telos from the release URL or receive a mounted
     release binary prepared by the eval runner.

2. Local checkout dependency
   - Used for development and pre-release comparisons.
   - The evals repo accepts `--telos-source /path/to/telos` or
     `TELOS_SOURCE=/path/to/telos`.
   - The eval runner builds `./cmd/telos` from that checkout, records the git
     SHA and dirty status, and mounts the resulting binary into benchmark jobs.

3. Pinned source dependency
   - Used when the evals repo needs to reproduce a historical pre-release run.
   - The evals repo can keep Telos as a git submodule, git worktree, or pinned
     dependency lock entry under something like `third_party/telos`.
   - The run record stores the pinned ref and binary checksum.

4. Container or OCI dependency
   - Useful once Telos publishes a runtime image or if eval workers need fully
     hermetic execution.
   - The evals repo records the image digest, not only the tag.

The evals repo may keep a small adapter package for Telos-specific benchmark
wrapping, but that adapter should call `telos` as an executable. It should not
link against Telos internals.

## Harbor Starting Point

Current Harbor-related code can move roughly as follows.

Keep in this repository:

- `cmd/telos`, `cmd/telosd`, `internal/spec`, `internal/game`,
  `internal/evidence`, `internal/sessionapi`, `internal/telosd`, release
  scripts, and core tests.
- A short document describing how external harnesses should invoke `telos run`.
- Optional minimal smoke fixture that validates a released or locally built
  Telos binary can execute a tiny spec.

Move to the evals repository:

- `harbor_evals_cli/`
- `scripts/harbor-evals`
- `scripts/run_harbor_evals.py`
- `scripts/run_harbor_evals_test.py`
- `scripts/harbor_results_app.py`
- `scripts/harbor_results_app_test.py`
- `scripts/prebuild_harbor_task_images.py`
- `integrations/harbor/` Harbor adapter code, including the Telos Harbor
  executable-agent shim and comparison harness shims
- Harbor eval docs such as experiment plans, stack reviews, fairness policy, and
  result UI notes

The Telos Harbor adapter is better owned by the evals repository because it
depends on Harbor's Python extension API, benchmark-specific prompt wrapping,
Docker/task environment concerns, mounted auth/toolchain behavior, and result
accounting. Those are eval-platform concerns. The adapter should use the Telos
CLI contract as its boundary.

## Suggested Evals Repository Shape

An initial evals repository could look like this:

```text
agent-evals/
  README.md
  pyproject.toml
  src/agent_evals/
    cli/
    web/
    state/
    backends/
      harbor/
    harnesses/
      telos.py
      codex.py
      pi.py
      opencode.py
    fairness/
    artifacts/
  tests/
  docs/
    harbor.md
    telos-dependency.md
    fairness.md
  third_party/
    telos/              # optional pinned checkout or submodule
  .evals/
    state/              # local, ignored
    jobs/               # local, ignored
    toolchains/         # local, ignored
```

The repository should expose one command surface, for example:

```bash
agent-evals init
agent-evals deps sync --telos-version latest
agent-evals deps build-telos --telos-source ../telos
agent-evals slate create harbor-smoke --benchmark slopcodebench --tasks 3
agent-evals run harbor-smoke --harness telos codex pi-vanilla --use-prebuilt
agent-evals status harbor-smoke
agent-evals web
```

## Interface To Record Per Telos Run

Every eval run involving Telos should record:

- Telos dependency mode: release, local checkout, pinned source, or image.
- Telos version output.
- Git SHA and dirty status when using source.
- Binary path and checksum, or image digest.
- Install URL and checksum when using a release.
- Full `telos run` command shape with secrets redacted.
- Spec content or spec artifact pointer.
- Skills and `extends` inputs.
- `--until`, timeout, cost, model/provider, and start-attempt settings.
- Transcript, stdout, stderr, and evidence artifact locations.
- Exit code and failure classification.

This keeps Telos reproducibility in the evals system without making this
repository responsible for eval storage or presentation.

## Migration Plan

1. Create the evals repository and copy the current Harbor eval stack into it.
2. Rename package/module paths from `harbor_evals_cli` to the new package name.
3. Move Harbor Python adapters into the evals repo and make the Telos adapter
   install or mount Telos through the dependency modes above.
4. Add a Telos dependency resolver in the evals repo:
   - `release`: install from public release URL
   - `local`: build from `--telos-source`
   - `pinned`: checkout/build from a locked ref
   - `image`: run from an OCI digest
5. Update Harbor commands to use adapter import paths from the evals package,
   not from this repository.
6. Keep a compatibility period where this repository's existing scripts either
   delegate to the evals repo or remain frozen until removed.
7. Delete or archive the local eval orchestration code from this repository once
   the evals repo can reproduce the existing Harbor slates.

## Design Rule

If a change is about making Telos behave correctly as an agent runtime, it
belongs here. If a change is about deciding what to run, how to compare runs,
where to store results, how to retry jobs, or how to visualize scores, it
belongs in the evals repository.

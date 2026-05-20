---
name: telos-operator
description: |
  How to use Telos as a goal-oriented agent runtime: write specs, launch local
  sessions, inspect evidence and artifacts, and judge whether a run satisfied
  its goal. Use when writing or running Telos specs, reviewing session output,
  or deciding whether to re-run.
metadata:
  category: workflow
  author: telos
allowed-tools: Bash(*) Read(*) Write(*) Edit(*)
---

# Telos Operator

Telos runs a spec-driven implementation/evaluation loop against a spec you
write. The implementation turns try to satisfy the spec; the evaluation turns
judge the delivered work against the contract. A run succeeds only when
evaluation concedes.

Your job as operator: write a spec that says what must be true, launch the run,
and inspect the result to decide whether the goal was actually met.

## Writing a Spec

A spec is a Markdown file (usually `SPEC.md`) with YAML frontmatter and a body
that declares the desired end state.

**Say what must be true, not what steps to run.** A good spec declares:

- The desired end state or property.
- What evidence would prove it (observable outputs, test results, file contents).
- Constraints that must not be violated (performance, compatibility, style).
- What would falsify success (regressions, missing coverage, broken interfaces).

Do not write a numbered todo list of implementation steps. The implementation
agent decides how to get there. If a specific procedure is genuinely part of the contract
(e.g., "the migration must be applied via `alembic upgrade head`"), include it
— but most specs should not dictate implementation.

### Frontmatter

```yaml
---
version: v0
name: my-spec-name
platform: local
---
```

### Example: CLI dogfooding

```markdown
---
version: v0
name: cli-dogfood
platform: local
---

# CLI Dogfooding

Every public CLI command (`plan`, `run`, `list`, `describe`, `logs`, `stop`) must
complete without traceback on a clean local install.

## Contract

- `uv run telos plan examples/hello-world/SPEC.md` exits 0 and prints a
  valid plan summary.
- `uv run telos run examples/hello-world/SPEC.md --json` exits 0, prints
  a JSON object with a `session_id` field, and the session reaches a
  terminal state within 120 seconds.
- `uv run telos list --json` exits 0 and returns a JSON array that
  includes the session from the previous step.
- `uv run telos describe <session-id>` exits 0 and prints the session
  status, config, progress, and artifact paths.
- `uv run telos logs <session-id>` exits 0 and prints at least one line.
- `uv run telos stop <session-id>` exits 0 or exits 1 with "already
  stopped".

## Falsification

Any command that exits non-zero without an expected error message, or
prints a Python traceback, is a failure.
```

### Example: release readiness

```markdown
---
version: v0
name: release-check
platform: local
---

# Release Readiness

The repo must be in a releasable state: tests green, lints clean, no
broken imports, and the CLI entrypoint loads.

## Contract

- `uv run pytest` exits 0.
- `uv run ruff check` exits 0.
- `uv run ruff format --check` exits 0.
- `uv run python -m telos.cli --help` exits 0 and prints usage.

## Constraints

- No new test files may be skipped or xfailed to achieve a green run.
- No lint rules may be disabled inline to pass `ruff check`.
```

### Example: session evidence quality

```markdown
---
version: v0
name: session-evidence-quality
platform: local
---

# Session Evidence Quality

After running the hello-world spec locally, the session directory must
contain well-formed evidence that a future auditor can replay.

## Contract

- `evidence.jsonl` exists and every line is valid JSON.
- At least one `game_start`, one `round_start`, and one
  `agent_complete` event are present.
- `transcript-*.md` exists and contains at least one implementation entry
  and one evaluation entry.
- `workspace.tar.gz` exists and contains `hello.txt`.

## Falsification

Missing files, malformed JSON lines, or a workspace archive that does
not contain the expected artifact.
```

## Running a Spec

### Preview without running

```bash
uv run telos plan SPEC.md
```

### Launch a local session

```bash
uv run telos run SPEC.md --json
```

The `--json` flag prints a JSON object with the `session_id` you need for
subsequent commands.

### Set the model

```bash
TELOS_MODEL=claude-opus-4-6 uv run telos run SPEC.md --json
```

### Bound the run

```bash
uv run telos run SPEC.md --thinking medium --until 4 --max-cost-usd 5 --json
```

The same run config can come from environment variables:

```bash
TELOS_THINKING=medium TELOS_MAX_COST_USD=5 uv run telos run SPEC.md --until 4 --json
```

CLI flags override environment variables.

### Target a specific workspace

By default Telos creates a fresh workspace. To run against an existing
directory:

```bash
TELOS_WORKSPACE=/path/to/workspace uv run telos run SPEC.md
```

### Describe a session

```bash
uv run telos describe <session-id>
```

Use `describe` for the point-in-time state: status, resolved config, progress,
artifact paths, and recent Telos evidence events. Use `--json` when another
agent needs to consume it.

### Follow logs

```bash
uv run telos logs -f <session-id>
```

Logs are the Telos transcript: what implementation claimed and what evaluation
found. They are not the same as evidence events.

### List sessions

```bash
uv run telos list
```

### Stop a session

```bash
uv run telos stop <session-id>
```

## Inspecting Results

After a run, session artifacts live at:

```text
.telos/sessions/<session_id>/
  session.json              # session metadata and final status
  specs/<spec_name>/
    spec.md                 # runtime copy of the spec
    evidence.jsonl           # structured event log (game_start, round_start, agent_complete, …)
    transcript-*.md          # append-only session dialogue
    workspace.tar.gz         # snapshot of the workspace at session end
```

### Quick inspection commands

```bash
# What sessions exist?
find .telos/sessions -maxdepth 1 -type d | sort

# Read the session status
cat .telos/sessions/<session_id>/session.json | python3 -m json.tool

# Scan evidence events
jq -c '.type // .event' .telos/sessions/<session_id>/specs/<spec_name>/evidence.jsonl

# Read the Telos transcript (implementation claims + evaluator findings)
cat .telos/sessions/<session_id>/specs/<spec_name>/transcript-*.md

# List workspace contents without extracting
tar -tzf .telos/sessions/<session_id>/specs/<spec_name>/workspace.tar.gz | head -40

# Extract workspace for manual inspection
mkdir -p /tmp/telos-inspect
tar -xzf .telos/sessions/<session_id>/specs/<spec_name>/workspace.tar.gz -C /tmp/telos-inspect
```

## Judging a Run

A run is not successful just because it exited cleanly. Check:

1. **Evaluation conceded.** Read the transcript. If the evaluator's last entry
   says "continue" with open findings, the goal is not met regardless of exit
   code.
2. **Evidence supports the claim.** Read `evidence.jsonl` for concrete
   observations. If implementation claimed tests pass but no test output appears
   in evidence, the claim is unsupported.
3. **Workspace matches the spec.** Extract `workspace.tar.gz` and verify the
   artifacts the spec required actually exist with the right content.
4. **No constraint violations.** Re-read the spec's constraints and
   falsification criteria. Check each one against the evidence and workspace.

If any of these fail, the run did not satisfy the spec — even if implementation
said it did.

## Common Mistakes

- **Writing a todo list instead of a spec.** "1. Install deps. 2. Run tests.
  3. Fix failures." is a procedure, not a contract. Say what must be true when
  the run is done.
- **Treating Telos as a task runner.** Evaluation will
  probe for real correctness, not just check that steps were executed.
- **Ignoring evaluator findings.** If the transcript shows open findings,
  the run needs another iteration or a new run — not a manual override.
- **Skipping artifact inspection.** The implementation narrative can be wrong. Always
  check `evidence.jsonl` and the workspace archive before trusting a result.

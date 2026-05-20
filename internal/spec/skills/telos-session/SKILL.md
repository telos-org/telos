---
name: telos-session
description: |
  Telos session communication and run snapshot inspection for local and
  Harbor runs. Use to write concise transcript entries, read previous turns,
  and inspect persisted evidence/workspace checkpoints.
metadata:
  category: session
  author: telos
allowed-tools: Bash(*) Read(*) Write(*) Edit(*)
---

# Telos Session Communication

You are participating in a spec-driven Telos session. The spec is the contract. The
live filesystem, process outputs, benchmark runner, and declared interfaces are
the truth. Logs, journals, transcripts, and source code are evidence trails, not
substitutes for direct verification.

## Shared Transcript

Each Telos run owns an append-only Markdown transcript named
`transcript-<session-id>.md`. Treat it as the communication log between
implementation, evaluation, controller, and operator turns.

- The implementation turn writes claims, changes made, evidence, and remaining uncertainty.
- The evaluation turn writes blocking findings first, with exact probes and observed
  failures.
- Keep entries concise enough for the next turn to act on.
- Use `<progress_update>...</progress_update>` for concise user-facing progress
  signals during meaningful state changes and at the end of every turn.
- Do not bury required action in raw logs when a progress update can state the
  control signal directly.
- Do not erase or rewrite earlier transcript content.

The evaluator's final progress update is the important handoff. It should say
either "concede" with the probes that justify concession, or "continue" with the
smallest set of findings the implementation must fix next.

## Implementation Turn

Before changing code, read the current transcript and identify the evaluator's
open findings. After changing code, run the strongest relevant probe you can
afford and write a transcript entry that includes:

1. What changed.
2. The command or interface used as evidence.
3. The observed result.
4. Any remaining risk or unverified area.

Do not claim a finding is fixed because the code looks plausible. Claim it only
after a probe observes the corrected behavior.

## Evaluation Turn

Read the transcript first, then verify independently. Prefer the benchmark's
official evaluator, public entry point, or task-declared command over the
implementation notes.

When you find a blocker, write a transcript entry with:

1. The violated requirement.
2. The exact command or probe.
3. Expected behavior.
4. Observed behavior.
5. The smallest actionable repair target.

When checking quality or slop, make the finding mechanical: file count, diff
stat, duplicate path, dead artifact, hidden state dependency, broad exception,
or unnecessary implementation branch. Tie the quality issue to correctness,
maintainability, benchmark score, or future evaluator confidence.

## Local Session Snapshots

Local Telos runs persist snapshots under the workspace:

```text
.telos/sessions/<session_id>/
  session.json
  specs/<spec_name>/spec.md
  specs/<spec_name>/evidence.jsonl
  specs/<spec_name>/transcript-<session_id>.md
  specs/<spec_name>/workspace.tar.gz
  specs/<spec_name>/turns/<turn_id>/task.md
  specs/<spec_name>/turns/<turn_id>/raw.jsonl
```

### `raw.jsonl` Contract

`raw.jsonl` is the per-turn ground truth: every stdout line the agent
produced during that turn, one JSON event per line. Telos folds the same
stream to derive transcript text, tool calls, tokens, cost, and stop reason.

- One event per line. Always valid JSONL.
- Lines that did not parse as JSON are wrapped as
  `{"event": "unparsed", "line": "<original>"}` so downstream tools can
  still read the file as JSONL.
- Schema is the agent's native event schema (Pi today). Telos does not
  re-emit a normalized shape — it folds events into `TurnStats` and
  `evidence.jsonl` records, but `raw.jsonl` stays unmodified.
- Appended as the agent emits newline-delimited events. It must not depend on
  the turn completing successfully.
- Use `evidence.jsonl` for cross-turn structured records (game start,
  round start, agent_complete, workspace_checkpoint). Use `raw.jsonl`
  when you need the agent's exact output — replays, debugging a parser
  regression, auditing a specific tool call.

```bash
jq -c '.type // .event' .telos/sessions/<id>/specs/<name>/turns/<turn-id>/raw.jsonl
```

Useful reads:

```bash
find .telos/sessions -maxdepth 4 -type f | sort
sed -n '1,240p' .telos/sessions/<session_id>/specs/<spec_name>/transcript-<session_id>.md
tar -tzf .telos/sessions/<session_id>/specs/<spec_name>/workspace.tar.gz | sort | head -200
```

To inspect a checkpoint without mutating the live workspace:

```bash
rm -rf /tmp/telos-workspace-view
mkdir -p /tmp/telos-workspace-view
tar -xzf .telos/sessions/<session_id>/specs/<spec_name>/workspace.tar.gz -C /tmp/telos-workspace-view
```

## Harbor Snapshot Layout

Harbor benchmark runs persist task artifacts under their job directory. A common
checkpoint layout is:

```text
/tmp/telos-harbor-jobs/<job>/<trial>/steps/checkpoint_<n>/agent/
  telos-harbor-spec.md
  telos-evidence.jsonl
  transcript-*.md
  telos-workspace.tar.gz
  turns/
    <turn-id>/
      task.md
      raw.jsonl
  artifacts/
```

Useful reads:

```bash
find /tmp/telos-harbor-jobs/<job>/<trial>/steps -maxdepth 4 -type f | sort
sed -n '1,240p' /tmp/telos-harbor-jobs/<job>/<trial>/steps/checkpoint_<n>/agent/transcript-*.md
tar -tzf /tmp/telos-harbor-jobs/<job>/<trial>/steps/checkpoint_<n>/agent/telos-workspace.tar.gz | sort | head -200
sed -n '1,80p' /tmp/telos-harbor-jobs/<job>/<trial>/steps/checkpoint_<n>/agent/turns/<turn-id>/task.md
```

If a benchmark writes rewards or logs outside the agent directory, inspect those
too before concluding:

```bash
find /tmp/telos-harbor-jobs/<job>/<trial>/steps/checkpoint_<n> -maxdepth 3 -type f | sort
```

## Rule Of Thumb

Use the transcript for decision-quality progress updates. Use evidence and
snapshots for audit. Use live probes for truth.

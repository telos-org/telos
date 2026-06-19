---
name: telos-session
description: |
  Telos session communication and run snapshot inspection. Use to write concise
  transcript entries, read previous turns, and inspect persisted
  evidence/workspace checkpoints.
metadata:
  category: session
  author: telos
allowed-tools: Bash(*) Read(*) Write(*) Edit(*)
---

# Telos Session Communication

You are participating in a spec-driven Telos session. The spec declares the
goal and its obligations. The live filesystem, process outputs, benchmark
runner, declared interfaces, and runtime behavior are the truth. Logs,
journals, transcripts, and source code are evidence trails, not substitutes
for direct verification.

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
whether the goal appears satisfied under review or name the smallest set of
grounded findings the implementation should address next.

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

When checking quality or slop, ground the finding in the delivered artifact:
where responsibilities accumulated, where hidden state or stale artifacts make
the next change harder, or where unnecessary branches obscure the goal. Tie
the quality issue to correctness, maintainability, benchmark score, or future
evaluator confidence.

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
  specs/<spec_name>/turns/<turn_id>/session.jsonl
```

### `session.jsonl` Contract

`session.jsonl` is the per-turn agent session record, written by Telos'
built-in native harness: messages, tool results, model, usage, cost, and
stop reason.

- One entry per line. Always valid JSONL when the agent turn completes normally.
- Schema is a compact agent-session shape consumed by Telos' transcript
  parser. Telos folds the final assistant message into transcript text,
  `TurnStats`, and `evidence.jsonl` records.
- Treat it as a turn artifact for audit and debugging, not as a live stdout
  stream.
- Use `evidence.jsonl` for cross-turn structured records (game start,
  round start, agent_complete, workspace_checkpoint). Use `session.jsonl`
  when you need the agent messages, tool results, or model usage.

```bash
jq -c 'select(.type=="message") | .message.role' .telos/sessions/<id>/specs/<name>/turns/<turn-id>/session.jsonl
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

## Rule Of Thumb

Use the transcript for decision-quality progress updates. Use evidence and
snapshots for audit. Use live probes for truth.

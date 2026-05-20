---
name: analyze-runs
description: |
  Scripts for analyzing Telos sessions, workspaces, evidence trails, and
  mutation outputs. Used for corpus quality assessment and training signal
  evaluation.
metadata:
  category: analysis
  author: telos
---

# Run Analysis

Scripts for inspecting the `.telos/sessions/` corpus.

## Scripts

- `scripts/scoreboard.py` — session overview: status, cost, rounds, cluster
- `scripts/evidence.py` — round-by-round evidence for a session: implementation/evaluation progress updates, findings, cost per turn
- `scripts/workspace.py` — workspace quality: GitOps compliance, Bazel targets, git log, file tree
- `scripts/signal.py` — training signal filter: sessions with 3+ rounds, what evaluation caught, fix complexity
- `scripts/frontier.py` — per-controller frontier view: transitive descendants classified as p1v1 / deepening / frontier / failing. Run with `--parent <session_id>` to see what your children actually produced and where the solver is struggling vs breezing through
- `scripts/mutations.py` — task package quality: checks promoted task packages for replay structure, tests, and grounded specs

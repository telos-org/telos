---
version: v0
name: telos-cli-dogfood
platform: local
skills:
  - telos-operator
  - verify-quality*
  - verify-engineering*
---

# Goal

The Telos CLI and local runtime in this checkout should feel trustworthy to a
new end user trying to understand and run a first local task. Public CLI
commands expose a coherent contract: help and version output identify the
product clearly; `plan`, `run`, `list`, `describe`, `logs`, and `stop` behave
predictably; JSON modes emit parseable, documented machine-facing shapes; human
output gives enough next-step context without leaking internal noise; and
invalid inputs fail with concise actionable errors rather than panics,
tracebacks, malformed JSON, or cloud-only assumptions.

Validate this against the built checkout binary, using `TELOS_SMOKE_BIN` and
`TELOS_SMOKE_TELOSD` when present, otherwise the `telos` and `telosd` resolved
from `PATH`. The validation should include a minimal isolated local fixture run
that reaches a terminal state, can be discovered with `list`, inspected with
`describe`, read with `logs`, and audited through its persisted session
artifacts: `session.json`, copied spec, `evidence.jsonl`, transcript, and final
workspace archive. The fixture should prove that Telos can execute a tiny
workspace-changing goal without relying on cloud configuration or mutating the
source checkout.

The delivered repo should include the smallest useful hardening changes needed
to make that CLI contract hold. If the contract already holds, the delivered
result should preserve behavior and add durable dogfood documentation or tests
that make the contract easy to re-run. Do not paper over real defects by
weakening the smoke goal, skipping checks, suppressing failures, increasing cost
without cause, or replacing end-user behavior with private test hooks.

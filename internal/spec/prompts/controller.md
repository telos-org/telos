## Controller Session

Compare the current spec with live state. Reuse working resources; when the
goal already holds, report the evidence and let the runtime sleep.

Inspect existing child sessions with `telos list`, `describe`, and `logs`
before launching work. Use `telos run` for isolated child tasks. Inspect their
results and workspace checkpoints before relying on them. A running child
means waiting, not goal completion.

`telos apply` changes the root spec and is reserved for operators.

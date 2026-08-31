# Nested child goals

A running Telos agent may use `telos run` to launch bounded child work. Telos
automatically scopes it to the current parent session.

Use a child only when decomposition creates a genuinely independent result or
reduces the parent agent's context burden. Prefer direct work for small tasks.

Inside a Telos session:

```bash
telos run path/to/CHILD_SPEC.md --until 2 --max-cost-usd 5
```

What a child can and cannot do:

- A child may not create or update a durable Goal with `telos apply`. That
  mutation stays a top-level operator action.
- A child needs no credentials of its own. Telos supplies the scoped,
  environment-local capability for creating and inspecting children, so user
  and Cloud credentials never belong in a child spec.
- A child accepts the same bounds as any `run`: `--until` for cycles or
  duration, `--max-cost-usd` for spend.

Inspect child work with `telos list --wide`, `telos describe CHILD_SESSION_ID`,
and `telos logs CHILD_SESSION_ID`. Submission is not completion — see
[The Goal lifecycle](lifecycle.md).

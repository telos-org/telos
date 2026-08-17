# Nested child goals

A running Telos agent may use `telos run` to launch bounded child work. Telos
automatically scopes it to the current parent session.

Use a child only when decomposition creates a genuinely independent result or
reduces the parent agent's context burden. Prefer direct work for small tasks.

Inside a Telos session:

```bash
telos run path/to/CHILD_SPEC.md --until 2 --max-cost-usd 5
```

Rules:

- Always give child work an observable deliverable and a finite cycle, time, or
  cost bound.
- Avoid recursive fan-out. Launch the fewest children needed and inspect their
  results before creating more.
- A child may not create or update a durable controller with `telos apply`.
  That mutation remains a top-level operator action.
- Do not pass user or Cloud credentials into the child spec. Telos supplies the
  scoped environment-local capability needed to create and inspect children.
- The parent remains responsible for integrating and verifying child output.

Use `telos list --wide`, `telos describe CHILD_SESSION_ID`, and
`telos logs CHILD_SESSION_ID` to inspect child work. Never treat successful
submission as successful completion.

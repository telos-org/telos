# Controller Design

Telos has two execution shapes:

- `telos run` creates a bounded task session.
- `telos apply` creates or updates a durable controller session.

A task is one-off work. A controller is desired-state reconciliation: it keeps
the root session and its transcript/evidence alive across updates.

## Apply

`telos apply SPEC.md` creates a controller. `telos apply SPEC.md --session
SESSION` updates that controller's desired state.

For local specs, the CLI writes the updated spec into the session store,
projects an `<external_update>` entry into the transcript/evidence, and wakes
the worker. If the worker is not running, Telos starts it again for the same
session directory.

For cloud specs, the CLI publishes an apply package to the control plane, then
updates the deployment/session package ref. The runtime `telosd` materializes
that package into the session store, projects the same `<external_update>`
entry, and wakes or starts the controller worker. Package materialization is a
runtime concern because the worker consumes files from the runtime session
directory.

## Versioning

The disk spec is the source of truth. SPEC frontmatter uses one version field:

```yaml
version: 0.1.0
```

That version is the package version published to the backend. There is no
separate `package_version` field; the reviewed file, package ref, and backend
record must stay aligned.

## Run

`telos run` creates a bounded task. `--until` accepts either a review-cycle
count or a duration:

```bash
telos run SPEC.md --until 3
telos run SPEC.md --until 30m
```

The integer form caps verifier review cycles. The duration form caps elapsed
task time at PVG loop boundaries. `telos apply` does not inherit these bounds;
controllers are durable unless explicitly stopped.

## Sleep And Wake

Current wake behavior is signal-based. A spec update appends an
`<external_update>` projection and asks the worker to wake. A future file-event
inbox can make transcript/evidence appends wake controllers directly, but the
source of truth remains the session files.

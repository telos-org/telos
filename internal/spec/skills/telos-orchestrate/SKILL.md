---
name: telos-orchestrate
description: |
  Telos controller runtime. Use when operating a long-running Telos
  system through session lineage and when choosing the right operational
  surface for a child task.
metadata:
  category: control-plane
  author: telos
allowed-tools: Bash(telos:*) Bash(python3:*) Bash(git:*)
---

# Telos Orchestration

A controller manages work by observing the live artifact, launching bounded
child tasks, inspecting their evidence, and integrating the best result.

## Surfaces

Local software materializes as a repo checkout, source files, build artifacts,
commands, and runtime behavior. Cloud software materializes as Kubernetes
resources and service behavior. Use the surface that actually owns the state:
source files for local artifacts, service APIs for service content, manifests
for deployment shape, and Telos sessions for lineage.

## Reusable Checks

Evaluator-written tests, probes, fixtures, and reproductions are durable
evidence. Preserve them when they expose a real invariant, run them against
candidate checkpoints, and only discard them when they are shown to be invalid.

## Child Reconciliation Gate

Before treating a terminal child as useful evidence, run:

```sh
telos inspect-child <child-session-id>
```

This records a durable `child-inspections/<child-session-id>.json` checklist
when the parent session is local, and reports whether the child is ready to
reconcile. A child is not ready when it is still active, lacks a workspace
checkpoint, failed analysis, or has failure taxonomy entries that need parent
review. Compare the child workspace against the parent live artifact before
merging, and keep reusable verification assets.

## Small Example

If a service secret is stale, the controller writes a focused task spec such
as `generated/<ts>-rotate-secret/spec.md`, launches it with `telos run`, waits
for that child to finish, runs `telos inspect-child <child-session-id>`,
compares the checkpoint with the parent artifact, then verifies the live
service uses the new secret before declaring the goal healthy.

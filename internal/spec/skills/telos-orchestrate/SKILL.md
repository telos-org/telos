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

## Small Example

If a service secret is stale, the controller writes a focused task spec such
as `generated/<ts>-rotate-secret/spec.md`, launches it with `telos run`, waits
for that child to finish, inspects its transcript and checkpoint, then verifies
the live service uses the new secret before declaring the goal healthy.

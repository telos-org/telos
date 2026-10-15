---
name: telos-cli
description: The complete user guide for the Telos CLI and cloud platform. Use this skill for anything to do with Telos.
metadata:
  registry: "@telos/telos-cli"
  public_guide: "references/use-telos.md"
  source_repository: "https://github.com/telos-org/telos"
---

# Telos

Telos is a goal-oriented programming system. You write down what a software service should do in a *goal specification* (`SPEC.md`), and Telos takes it from there — building, testing, deploying, and maintaining the software for you.

A goal specification is a declarative description of a service's desired state: its intent, APIs, and behavior. It's the living source of truth, and Telos continually reconciles the running software toward it.

This is the index of the Telos CLI guide. It ships with every Telos release and is published at `usetelos.ai/docs`.

## Guide

- [Use Telos](references/use-telos.md) — install, sign in, and walk one Goal through its full lifecycle
- [Goals](references/goals.md) — the goal specification, skills, rubrics, and packages
- [Environment](references/environment.md) — the VM, toolchain, and network access each Goal runs with
- [Models](references/models.md) — inference options when using Telos: managed inference and bring your own keys/subscriptions

## For agents

The CLI is designed to be agent-friendly: its default output is compact text, meant to be read as is. Most commands also take `--json` for when a result needs to go through other shell tools, like `jq`, `grep` or `awk`.

- Ask before `telos apply` (starts paid compute and inference) `telos delete` (which can't be undone), and `telos push` (pushes to a hosted registry).
- `telos login` opens a browser and waits for the user to approve sign in (device code authentication)
- A Goal is a persistent object. Check on it with `telos describe` and `telos logs` every few minutes, not in a tight loop. `telos logs --follow` doesn't return until the Goal stops.

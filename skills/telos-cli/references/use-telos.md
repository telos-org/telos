---
title: Use Telos
description: Give Telos a goal, and it builds, runs, and verifies the software behind it.
---

## What Telos does

You state what should remain true. Telos writes the implementation, deploys
it, verifies it against your contract, and keeps it aligned as the contract
changes. **You do not write or review the implementation** — the Goal is the
artifact you keep.

That contract is a `SPEC.md`. It outlives every agent and revision that tries
to satisfy it.

This page walks one Goal from nothing to a running, verified service.

## 1. Install

```bash
curl -fsSL https://usetelos.ai/install.sh | sh
telos login
```

`telos login` pauses for browser approval. Confirm where commands will land
before running anything that spends:

```console
$ telos config
Config file     ~/.telos/config.yaml
Endpoint        https://api.usetelos.ai
Authentication  valid
Context         @telos
Default model   workspace default
```

## 2. Write the contract

Smallest useful spec: an observable outcome, what must survive, and how
success is proven.

```markdown
---
name: status-page
version: 0.1.0
platform: cloud
---

# Goal

Run a public status page for our services.

- `GET /api/status` returns every service with its current state.
- Incident history survives restarts and redeploys.

# Acceptance

- A newly reported incident appears on the public page within 60 seconds.
```

`platform: cloud` runs it as a managed deployment. `platform: local` runs it
on this machine. See [Goals and specifications](goals.md) for the full shape,
and [Packages and skills](packages-and-skills.md) to attach skills or a
required rubric.

## 3. Preview

```console
$ telos plan SPEC.md
Spec      status-page
Target    cloud
Context   @telos
Path      SPEC.md
Namespace ns-status-page
Hash      e29c25796f22f453
```

A first `plan` confirms identity and target — which spec, which context, what
it hashes to. It does not describe the work. The reviewable diff comes later,
once there is a deployed revision to compare against.

## 4. Apply

```console
$ telos apply SPEC.md
created status-page

Status    working
Session   sess_c7d2f0a4e8
Revision  sha256:8f21c47a...
Logs      telos logs sess_c7d2f0a4e8
```

`apply` returns as soon as the work is accepted, not when it is finished.
Agents build, test, and deploy in the background. **The session ID is stable
for the life of the Goal** — every later revision reuses it.

## 5. Wait for Ready

```console
$ telos describe sess_c7d2f0a4e8
Name      status-page
Status    ready
Session   sess_c7d2f0a4e8
Revision  sha256:8f21c47a...
Service   https://status-page-c7d2f0a4e8.usetelos.ai
```

`ready` means that exact revision passed verification and is running. Poll
`describe` with a bounded timeout; stop on a terminal failure. The four states
and what each proves are in [The Goal lifecycle](lifecycle.md).

Trace the work with:

```console
$ telos logs sess_c7d2f0a4e8
[12:00:00Z] [INFO] Accepted managed session
[12:04:22Z] [INFO] Implemented persistent incident history
[12:08:47Z] [INFO] Service URL verified
[12:11:06Z] [INFO] Running the required service checks
[12:15:31Z] [INFO] Current revision accepted
```

`logs` shows the 50 most recent rows; pass `--all` or `--tail N` for more.

Then use the live service. It is the claim; the logs are not.

## 6. Change the contract

Edit the spec, bump its version, and plan against the durable session. This
form produces a real diff:

```console
$ telos plan SPEC.md --session sess_c7d2f0a4e8
Session   sess_c7d2f0a4e8
Current   sha256:8f21c47a...
Version   0.1.0 -> 0.2.0

+ 30 days of uptime history render for each service.
```

```bash
telos apply SPEC.md --session sess_c7d2f0a4e8
```

Agents reconcile the change incrementally. The session, its history, and the
service URL stay stable.

## Bounded work

`apply` is for a Goal that should persist. For one bounded piece of work that
should stop at a limit, use `run`:

```bash
telos run SPEC.md --workspace . --until 3
```

It stops at the bound and returns evidence. It does not create a durable
Goal — only `apply` does.

## Next

- [The Goal lifecycle](lifecycle.md) — states, revisions, and what Ready proves
- [Goals and specifications](goals.md) — writing the contract
- [Telos Cloud](cloud.md) — contexts, teams, and managed deployments
- [Models and inference](inference.md) — managed tiers or your own subscription
- [Troubleshooting](troubleshooting.md) — when something does not reach Ready

---
title: Use Telos
description: You work with a coding agent. It uses Telos to run and verify background agents.
---

## Install

Your coding agent can install the CLI. `telos login` pauses for your browser
approval.

```bash
curl -fsSL https://usetelos.ai/install.sh | sh
telos login
```

## Specify

You and your coding agent agree on what should remain true. The agent writes it
as a verifiable `SPEC.md`.

```markdown
---
name: hello-service
version: 0.1.0
platform: cloud
---

# Goal

Keep `/healthz` available with persistent Postgres storage.
```

## Apply

```bash
telos plan SPEC.md
telos apply SPEC.md
```

The agent runs `plan`; you approve the contract; the agent runs `apply`. Telos
then delegates the background work.

## Ready

```bash
telos describe SESSION_ID
telos logs SESSION_ID
```

Ready means the exact revision passed verification and is running. Your coding
agent follows the session and returns the evidence.

## Local runs

```bash
telos run SPEC.md --workspace . --until 3
```

Your coding agent can use `run` for a bounded subgoal. It stops; `apply`
persists.

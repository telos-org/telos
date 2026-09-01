---
title: Install Telos
description: Install the CLI, verify the release, and prepare Cloud or local authentication.
group: Getting started
---

# Install Telos

The default installer resolves the current promoted release:

```bash
curl -fsSL https://usetelos.ai/install.sh | sh
telos --version
telos --help
```

It installs `telos` and `telosd` under
`${TELOS_INSTALL_DIR:-$HOME/.local/bin}` and this skill under
`${TELOS_AGENT_SKILLS_DIR:-$HOME/.agents/skills}/telos-cli`.

For Cloud work, sign in and confirm the target context:

```bash
telos login
telos config
```

Then continue with [Use Telos](use-telos.md).

For a local run, install `pi` if the installer reports it missing and
authenticate the intended provider with `pi` → `/login`. Then follow
[Bounded runs](bounded-runs.md). Managed Cloud deployments do not use the
workstation's local model credentials.

## Install an exact release

Prefix the shell receiving the installer pipe:

```bash
curl -fsSL https://usetelos.ai/install.sh | TELOS_INSTALL_VERSION=v0.1.2 sh
```

## Repair the command path

If the shell cannot find `telos`, add `$HOME/.local/bin` to `PATH` or set
`TELOS_INSTALL_DIR` before installing.

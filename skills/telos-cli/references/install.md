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

It installs `telos` under `${TELOS_INSTALL_DIR:-$HOME/.local/bin}` and the
CLI skill and documentation under
`${TELOS_AGENT_SKILLS_DIR:-$HOME/.agents/skills}/telos-cli`.

Cloud use does not require `telosd` or `pi` on your machine, including when you
use Claude Code or Codex to operate Telos Cloud. The managed runtime is
installed and updated by Cloud automation.

For first-time interactive Cloud setup, sign in and confirm the target context:

```bash
telos login
telos config
```

Agents and CI jobs can use an existing token supplied as `TELOS_AUTH_TOKEN`
and run Cloud commands without a browser login step. See
[Cloud authentication](cloud.md#authenticate) for token setup and a CI example.

Then continue with [Use Telos](use-telos.md).

## Add local execution

For harness development, benchmarking, or other local execution, include
`telosd` explicitly:

```bash
curl -fsSL https://usetelos.ai/install.sh | TELOS_INSTALL_LOCAL=1 sh
```

This installs the CLI, skill, and local runtime from the same release. Then
install `pi` if needed and authenticate your provider with `pi` → `/login`.
Follow [Bounded runs](bounded-runs.md). Managed Cloud deployments do not use
your local model credentials.

Rerunning the installer retains and updates an existing `telosd` beside the
CLI, even without `TELOS_INSTALL_LOCAL=1`. It also remembers the skill directory
from the previous installation; `TELOS_AGENT_SKILLS_DIR` selects a different
directory when supplied.

## Install an exact release

Prefix the shell receiving the installer pipe:

```bash
curl -fsSL https://usetelos.ai/install.sh | TELOS_INSTALL_VERSION=v0.1.2 sh
```

## Update the CLI

Update the currently running CLI to the latest promoted release, or choose
an exact release:

```bash
telos update
telos update latest
telos update v0.1.5+master.db65a24fa89d
telos --version
```

An explicit older version rolls the CLI back. If you already have the selected
version, the command makes no changes. Exact versions accept an optional `v`
prefix; a `+master.<commit>` suffix identifies an immutable build, not a new
semantic-version tag.

The updater downloads the matching macOS or Linux binary, checks its SHA-256
checksum, and atomically replaces the executable you invoked. A failed download
or checksum check leaves the existing CLI untouched. Its directory must be
writable; the updater does not request elevated privileges. Symlinks continue
to point at the updated executable.

Only `telos` changes. Your configuration, credentials, `telosd`, installed skill
bundle, and deployed sessions are unchanged. To update the full local
installation, rerun the installer above instead. Older CLIs without `update`
also need the installer once to get this command.

If you installed Telos with a package manager, update through that manager.
The updater refuses recognized Homebrew, Nix, Snap, and MacPorts paths.
Development builds must be rebuilt from source.

## Repair the command path

If the shell cannot find `telos`, add `$HOME/.local/bin` to `PATH` or set
`TELOS_INSTALL_DIR` before installing.

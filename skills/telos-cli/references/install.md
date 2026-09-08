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

Cloud-only use does not require `pi` on your machine, including when you use
Claude Code or Codex to operate Telos Cloud. The installer's `pi` setup
instructions apply only to local Telos runs.

For Cloud work, sign in and confirm the target context:

```bash
telos login
telos config
```

Then continue with [Use Telos](use-telos.md).

For a local run, install `pi` if it is not already installed and
authenticate the intended provider with `pi` → `/login`. Then follow
[Bounded runs](bounded-runs.md). Managed Cloud deployments do not use the
workstation's local model credentials.

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

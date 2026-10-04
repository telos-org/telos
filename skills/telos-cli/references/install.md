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

## Update your installation

Update your installed Telos components to the latest promoted release, or
choose an exact release:

```bash
telos update
telos update latest
telos update v0.1.5+master.db65a24fa89d
telos --version
```

An explicit older version rolls the installed components back. If all components
already match the selected release, none are replaced. An update also repairs
outdated or missing skill files and an outdated companion runtime when the CLI
itself is already current. Exact versions accept an optional `v`
prefix; a `+master.<commit>` suffix identifies an immutable build, not a new
semantic-version tag.

The updater targets the executable you invoked, the installed CLI skill, and
any `telosd` installed beside that executable. A Cloud client installation
stays lightweight: an update does not add `telosd`. The skill directory is
remembered by the installer; `TELOS_AGENT_SKILLS_DIR` overrides it. For an older
installation without that record, the default is `~/.agents/skills/telos-cli`.
Set `TELOS_AGENT_SKILLS_DIR` once if you previously installed the skill elsewhere.

Every artifact comes from one immutable release and passes SHA-256 verification
before any installed component is replaced. A failed download, checksum check,
or skill extraction leaves existing components intact. Each binary replacement
is atomic, and a replacement error rolls back the components already replaced.
The binary and skill directories must be writable; the updater does not request
elevated privileges. Binary and skill symlinks continue to point at their updated
targets. The bundled skill directory is replaced, including removal of obsolete
files, so keep personal skills in separate directories.

Your configuration and credentials remain in place. Running local sessions
continue with their current processes; new processes use the updated runtime.
Cloud deployment runtimes are rolled out by Cloud automation independently of
your workstation update. Older CLIs without `update` need the installer once to
get this command.

If you installed Telos with a package manager, update through that manager.
The updater refuses recognized Homebrew, Nix, Snap, and MacPorts paths.
Development builds must be rebuilt from source.

## Repair the command path

If the shell cannot find `telos`, add `$HOME/.local/bin` to `PATH` or set
`TELOS_INSTALL_DIR` before installing.

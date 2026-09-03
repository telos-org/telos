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
`${TELOS_INSTALL_DIR:-$HOME/.local/bin}`. It also installs two coding-agent
skills:

- `telos-spec` helps you create, improve, and review `SPEC.md` through a
  conversation with your coding agent.
- `telos-cli` helps your agent plan, apply, run, and inspect an existing Goal.

By default, both skills are installed under `$HOME/.agents/skills` for Codex
and `$HOME/.claude/skills` for Claude Code. Invoke the authoring skill explicitly
with `$telos-spec` in Codex or `/telos-spec` in Claude Code. Set
`TELOS_INSTALL_CLAUDE_SKILLS=0` when you want the Codex-compatible installation
and binaries without changing the Claude skill directory.

If you already set `TELOS_AGENT_SKILLS_DIR`, that remains the only skill
destination. Set `TELOS_CLAUDE_SKILLS_DIR` as well when you want a second,
custom Claude Code destination. Existing recognized Telos skills are replaced
as one transaction and marked as installer-managed. Upgrades replace the full
contents of managed skill directories, so keep personal changes in a separate
skill. The installer preserves a per-skill symbolic link only when it points to
the other configured skill root or its target already matches the release being
installed; a stale linked skill stops installation instead of silently leaving
different agent versions. An unmanaged directory at either reserved skill name
is never overwritten.

The first upgrade of a legacy, unmarked `telos-cli` installation happens
automatically. Its previous directory is retained outside agent discovery under
the adjacent `.telos-skill-backups` directory, and the installer prints its
exact path. After checking that the new skill works and recovering any local
customization, you can delete that printed backup directory.

Only one installer runs per home directory at a time. If a machine loses power
or an installer is killed before cleanup, confirm no Telos installer is still
running and remove `$HOME/.telos-install.lock` before retrying.
For a read-only home directory or shared custom destinations, set
`TELOS_INSTALL_LOCK_DIR` to one writable lock path used by every installer that
can modify those destinations.

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

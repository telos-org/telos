# Install Telos

The default installer resolves the current promoted release:

```bash
curl -fsSL https://usetelos.ai/install.sh | sh
```

Then verify the installed binary:

```bash
telos --version
telos --help
```

The installer places `telos` and `telosd` in `${TELOS_INSTALL_DIR:-$HOME/.local/bin}`
and installs this skill under
`${TELOS_AGENT_SKILLS_DIR:-$HOME/.agents/skills}/telos-cli`.

To install an exact prior release through the website installer, prefix the
shell receiving the pipe:

```bash
curl -fsSL https://usetelos.ai/install.sh | TELOS_INSTALL_VERSION=v0.1.2 sh
```

If the shell cannot find `telos`, add `$HOME/.local/bin` to `PATH` or set
`TELOS_INSTALL_DIR` before installing.

Local runs use the `pi` coding-agent runtime. If it is absent, follow the
installer's current setup message and authenticate the intended model provider
before launching a local goal. Managed Cloud deployments do not use the
workstation's local model credentials.
